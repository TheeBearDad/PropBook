package main

import (
	"net/url"
	"strings"
	"testing"
)

func TestParseRSS(t *testing.T) {
	body := []byte(`<?xml version="1.0" encoding="UTF-8"?><rss version="2.0" xmlns:dc="http://purl.org/dc/elements/1.1/"><channel><title>FinancialJuice</title>
<item><title>US CPI MoM Actual 0.4% (Forecast 0.3%, Previous 0.2%)</title><link>https://www.financialjuice.com/News/1/x.aspx</link><pubDate>Fri, 13 Nov 2026 13:30:00 GMT</pubDate><description>&lt;p&gt;Hot &amp;amp; sticky&lt;/p&gt;</description></item>
<item><title>Bad link</title><link>javascript:alert(1)</link></item>
<item><title>No date</title><guid>https://example.com/a</guid></item>
<item><title>EST date</title><link>https://example.com/b</link><pubDate>Fri, 13 Nov 2026 08:30:00 EST</pubDate></item>
</channel></rss>`)
	title, items, err := parseFeed(body, "fj")
	if err != nil || title != "FinancialJuice" {
		t.Fatalf("title %q err %v", title, err)
	}
	if len(items) != 3 {
		t.Fatalf("want 3 items (javascript link dropped), got %d", len(items))
	}
	if items[0].Summary != "Hot & sticky" {
		t.Fatalf("summary %q", items[0].Summary)
	}
	if items[0].Time != 1794576600000 {
		t.Fatalf("time %d", items[0].Time)
	}
	if items[1].Link != "https://example.com/a" || items[1].Time != 0 {
		t.Fatalf("guid fallback %+v", items[1])
	}
	if items[2].Time != items[0].Time {
		t.Fatalf("EST not corrected: %d vs %d", items[2].Time, items[0].Time)
	}
}

func TestParseAtomAndLatin1(t *testing.T) {
	atom := []byte(`<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><title>Fed</title>
<entry><title>FOMC statement</title><link rel="alternate" href="https://www.federalreserve.gov/x.htm"/><updated>2026-11-04T19:00:00Z</updated><summary>Rates held</summary></entry></feed>`)
	title, items, err := parseFeed(atom, "fed")
	if err != nil || title != "Fed" || len(items) != 1 || items[0].Link != "https://www.federalreserve.gov/x.htm" || items[0].Time == 0 {
		t.Fatalf("atom: %q %+v %v", title, items, err)
	}
	lat := append([]byte(`<?xml version="1.0" encoding="ISO-8859-1"?><rss><channel><title>Caf`), 0xe9)
	lat = append(lat, []byte(`</title><item><title>Euro `)...)
	lat = append(lat, 0xe9)
	lat = append(lat, []byte(`t`)...)
	lat = append(lat, []byte(`</title><link>https://e.com/1</link></item></channel></rss>`)...)
	title, items, err = parseFeed(lat, "l")
	if err != nil || title != "Café" || len(items) != 1 || items[0].Title != "Euro ét" {
		t.Fatalf("latin1: %q %+v %v", title, items, err)
	}
}

func TestParseNotAFeed(t *testing.T) {
	if _, _, err := parseFeed([]byte(`<!doctype html><html><body>Just a page</body></html>`), "x"); err == nil {
		t.Fatal("an HTML page was accepted as a feed")
	}
	if _, _, err := parseFeed([]byte(`{"json":true}`), "x"); err == nil {
		t.Fatal("JSON was accepted as a feed")
	}
}

func TestPlainTextLimit(t *testing.T) {
	s := plainText(strings.Repeat("word ", 200), 50)
	if len([]rune(s)) > 51 || !strings.HasSuffix(s, "…") {
		t.Fatalf("not cut: %q", s)
	}
}

func TestWidgetConfigSanitises(t *testing.T) {
	q := url.Values{"w": {"chart"}, "symbol": {`"></script><script>alert(1)</script>`}, "interval": {"evil"}}
	_, cfg, ok := widgetConfig(q)
	if !ok || cfg["symbol"] != "CME_MINI:NQ1!" || cfg["interval"] != "15" {
		t.Fatalf("unsafe values passed through: %+v", cfg)
	}
	if _, _, ok := widgetConfig(url.Values{"w": {"nope"}}); ok {
		t.Fatal("unknown widget accepted")
	}
	_, cfg, _ = widgetConfig(url.Values{"w": {"chart"}, "symbol": {"COMEX:GC1!"}, "interval": {"60"}, "theme": {"light"}})
	if cfg["symbol"] != "COMEX:GC1!" || cfg["theme"] != "light" {
		t.Fatalf("good values dropped: %+v", cfg)
	}
}
