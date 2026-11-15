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

func TestCalendarWidgetCountries(t *testing.T) {
	_, cfg, _ := widgetConfig(url.Values{"w": {"calendar"}, "countries": {"us,ca,gb,<x>,EVIL"}})
	if cfg["countryFilter"] != "us,ca,gb" {
		t.Fatalf("countries %v", cfg["countryFilter"])
	}
	_, cfg, _ = widgetConfig(url.Values{"w": {"calendar"}})
	if cfg["countryFilter"] != "us" {
		t.Fatalf("default countries %v", cfg["countryFilter"])
	}
}

func TestParseTagsImagesAndHot(t *testing.T) {
	body := []byte(`<?xml version="1.0"?><rss version="2.0" xmlns:media="http://search.yahoo.com/mrss/"><channel><title>FJ</title>
<item><title>Suspected Yen Intervention</title><link>https://www.financialjuice.com/News/1/a.aspx</link>
<category>JPY</category><category>Market Moving</category><enclosure url="https://www.financialjuice.com/img/chart1.png" type="image/png" length="0"/></item>
<item><title>FAA issues national ground stop - FAA</title><link>https://www.financialjuice.com/News/2/b.aspx</link>
<category>US Bonds</category><category>US Indexes</category><category>USD</category><category>US Indexes</category>
<description>&lt;p&gt;See chart &lt;img src="https://cdn.example.com/c.jpg"&gt;&lt;/p&gt;</description></item>
<item><title>MOC Imbalance
S&amp;P 500: +2404 mln
Nasdaq 100: +1786 mln</title><link>https://www.financialjuice.com/News/3/c.aspx</link><media:content url="https://e.com/m.webp" medium="image"/></item>
<item><title>BREAKING: Fed cuts rates</title><link>https://e.com/4</link><enclosure url="javascript:alert(1)" type="image/png"/></item>
</channel></rss>`)
	_, items, err := parseFeed(body, "fj")
	if err != nil || len(items) != 4 {
		t.Fatalf("items %d err %v", len(items), err)
	}
	if !items[0].Hot || items[0].Image != "https://www.financialjuice.com/img/chart1.png" || strings.Join(items[0].Tags, ",") != "JPY,Market Moving" {
		t.Fatalf("market moving item: %+v", items[0])
	}
	if items[1].Hot || items[1].Image != "https://cdn.example.com/c.jpg" || len(items[1].Tags) != 3 {
		t.Fatalf("tags/description image: %+v", items[1])
	}
	if items[2].Title != "MOC Imbalance" || items[2].Summary != "S&P 500: +2404 mln\nNasdaq 100: +1786 mln" || items[2].Image != "https://e.com/m.webp" {
		t.Fatalf("multi-line title: %q / %q / %q", items[2].Title, items[2].Summary, items[2].Image)
	}
	if !items[3].Hot || items[3].Image != "" {
		t.Fatalf("breaking headline / unsafe image: %+v", items[3])
	}
}
