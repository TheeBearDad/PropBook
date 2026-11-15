package main

// News tab: reads the public RSS / Atom feeds news sites publish, the ForexFactory weekly
// calendar export, and hosts TradingView's official embeddable widgets.
//
// Feeds are fetched by the app (not the page) because browsers block a page from reading other
// sites. Results are cached so a busy refresh button never hammers anyone, and the last good copy
// is kept on disk so the tab still has something to show offline.

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type newsItem struct {
	ID      string `json:"id"`
	Feed    string `json:"feed"`
	Source  string `json:"source"`
	Title   string `json:"title"`
	Link    string `json:"link"`
	Summary string `json:"summary,omitempty"`
	Time    int64  `json:"time"` // unix ms; 0 when the feed gave no date
}

type feedResult struct {
	at    time.Time
	items []newsItem
	title string
	err   string
}

var (
	newsMu     sync.Mutex
	newsCache  = map[string]*feedResult{}
	newsClient = &http.Client{Timeout: 15 * time.Second}
	widgetBase string
)

const (
	feedFresh    = 3 * time.Minute  // reuse a feed this recent on a normal load
	feedMinGap   = 45 * time.Second // even "Refresh" waits this long between hits to one feed
	calFresh     = 30 * time.Minute
	calMinGap    = 5 * time.Minute // ForexFactory blocks clients that ask too often
	maxFeedBytes = 6 << 20
)

func userAgent() string {
	return "PropBook/" + version + " (news reader; +https://github.com/" + updateRepo + ")"
}

// ---------- feed parsing ----------

type rssDoc struct {
	XMLName xml.Name
	Channel struct {
		Title string    `xml:"title"`
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
	Items   []rssItem   `xml:"item"` // RSS 1.0 / RDF puts items beside the channel
	Title   string      `xml:"title"`
	Entries []atomEntry `xml:"entry"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	Description string `xml:"description"`
	Content     string `xml:"http://purl.org/rss/1.0/modules/content/ encoded"`
	PubDate     string `xml:"pubDate"`
	DCDate      string `xml:"http://purl.org/dc/elements/1.1/ date"`
	Source      string `xml:"source"`
}

type atomEntry struct {
	Title string `xml:"title"`
	Links []struct {
		Href string `xml:"href,attr"`
		Rel  string `xml:"rel,attr"`
	} `xml:"link"`
	ID        string `xml:"id"`
	Summary   string `xml:"summary"`
	Content   string `xml:"content"`
	Updated   string `xml:"updated"`
	Published string `xml:"published"`
}

var (
	tagRe   = regexp.MustCompile(`(?s)<[^>]*>`)
	spaceRe = regexp.MustCompile(`\s+`)
)

func plainText(s string, max int) string {
	s = tagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(html.UnescapeString(s)) // some feeds escape twice
	s = strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
	if max > 0 && len([]rune(s)) > max {
		r := []rune(s)[:max]
		s = strings.TrimSpace(string(r)) + "…"
	}
	return s
}

var dateLayouts = []string{
	time.RFC1123Z, time.RFC1123, time.RFC3339, time.RFC3339Nano,
	"Mon, 2 Jan 2006 15:04:05 -0700", "Mon, 2 Jan 2006 15:04:05 MST", "Mon, 02 Jan 2006 15:04 -0700",
	"Mon, 2 Jan 2006 15:04 MST", "2 Jan 2006 15:04:05 -0700", "Mon, 02 Jan 06 15:04:05 -0700",
	"2006-01-02T15:04:05", "2006-01-02 15:04:05", "Mon, 2 January 2006 15:04:05 -0700",
	"Mon Jan 2 15:04:05 MST 2006", "Mon, 02 Jan 2006 15:04:05 Z",
}

func parseFeedTime(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	// "EST"/"EDT"-style zones parse with a zero offset; correct the common US ones.
	fix := map[string]int{"EST": -5, "EDT": -4, "CST": -6, "CDT": -5, "PST": -8, "PDT": -7, "GMT": 0, "UTC": 0}
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			name, off := t.Zone()
			if h, ok := fix[name]; ok && off == 0 && h != 0 {
				t = t.Add(-time.Duration(h) * time.Hour)
			}
			return t.UnixMilli()
		}
	}
	return 0
}

// latin1Reader lets Go's XML decoder read feeds that aren't UTF-8 (ISO-8859-1 / Windows-1252).
func charsetReader(label string, in io.Reader) (io.Reader, error) {
	switch strings.ToLower(label) {
	case "utf-8", "utf8", "us-ascii", "ascii":
		return in, nil
	case "iso-8859-1", "latin1", "latin-1", "windows-1252", "cp1252", "iso-8859-15":
		b, err := io.ReadAll(in)
		if err != nil {
			return nil, err
		}
		r := make([]rune, len(b))
		for i, c := range b {
			r[i] = rune(c)
		}
		return strings.NewReader(string(r)), nil
	}
	return nil, fmt.Errorf("unsupported text encoding %q", label)
}

func itemID(link, title string) string {
	h := sha1.Sum([]byte(strings.ToLower(strings.TrimSpace(link)) + "|" + strings.ToLower(strings.TrimSpace(title))))
	return hex.EncodeToString(h[:8])
}

func safeLink(s string) string {
	s = strings.TrimSpace(s)
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return u.String()
}

func parseFeed(body []byte, feedID string) (string, []newsItem, error) {
	var doc rssDoc
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = false
	dec.Entity = xml.HTMLEntity
	dec.CharsetReader = charsetReader
	if err := dec.Decode(&doc); err != nil {
		return "", nil, errors.New("this address didn't return a news feed")
	}
	var out []newsItem
	title := plainText(doc.Channel.Title, 80)
	add := func(t, link, summary string, when int64, src string) {
		t = plainText(t, 300)
		link = safeLink(link)
		if t == "" || link == "" {
			return
		}
		s := plainText(summary, 420)
		if strings.EqualFold(s, t) {
			s = ""
		}
		out = append(out, newsItem{ID: itemID(link, t), Feed: feedID, Source: src, Title: t, Link: link, Summary: s, Time: when})
	}
	items := append(doc.Channel.Items, doc.Items...)
	for _, it := range items {
		link := it.Link
		if link == "" && strings.HasPrefix(it.GUID, "http") {
			link = it.GUID
		}
		d := it.PubDate
		if d == "" {
			d = it.DCDate
		}
		sum := it.Description
		if sum == "" {
			sum = it.Content
		}
		add(it.Title, link, sum, parseFeedTime(d), plainText(it.Source, 60))
	}
	if len(doc.Entries) > 0 {
		if title == "" {
			title = plainText(doc.Title, 80)
		}
		for _, e := range doc.Entries {
			link := ""
			for _, l := range e.Links {
				if l.Rel == "" || l.Rel == "alternate" {
					link = l.Href
					break
				}
			}
			d := e.Published
			if d == "" {
				d = e.Updated
			}
			sum := e.Summary
			if sum == "" {
				sum = e.Content
			}
			add(e.Title, link, sum, parseFeedTime(d), "")
		}
	}
	if doc.XMLName.Local != "rss" && doc.XMLName.Local != "feed" && doc.XMLName.Local != "RDF" && len(out) == 0 {
		return "", nil, errors.New("this address didn't return a news feed")
	}
	return title, out, nil
}

// ---------- fetching ----------

func fetchURL(u string, accept string) ([]byte, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent())
	req.Header.Set("Accept", accept)
	resp, err := newsClient.Do(req)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return nil, errors.New("the site took too long to answer")
		}
		return nil, errors.New("couldn't reach the site. Check your internet connection")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 403 || resp.StatusCode == 401 {
		return nil, errors.New("the site refused the request (it may block feed readers)")
	}
	if resp.StatusCode == 429 {
		return nil, errors.New("the site asked PropBook to slow down. Try again in a few minutes")
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("the site answered with error %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxFeedBytes))
}

func getFeed(id, u string, force bool) *feedResult {
	newsMu.Lock()
	c := newsCache[u]
	newsMu.Unlock()
	if c != nil {
		age := time.Since(c.at)
		if age < feedMinGap || (!force && age < feedFresh) {
			return c
		}
	}
	res := &feedResult{at: time.Now()}
	body, err := fetchURL(u, "application/rss+xml, application/atom+xml, application/xml;q=0.9, text/xml;q=0.9, */*;q=0.5")
	if err == nil {
		res.title, res.items, err = parseFeed(body, id)
	}
	if err != nil {
		res.err = err.Error()
		if c != nil && len(c.items) > 0 { // keep showing the last good copy
			res.items, res.title = c.items, c.title
		}
	}
	for i := range res.items {
		res.items[i].Feed = id
	}
	newsMu.Lock()
	newsCache[u] = res
	newsMu.Unlock()
	return res
}

type feedReq struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

func handleNews(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	var body struct {
		Feeds   []feedReq `json:"feeds"`
		Refresh bool      `json:"refresh"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "bad request", 400)
		return
	}
	if len(body.Feeds) > 40 {
		body.Feeds = body.Feeds[:40]
	}
	type feedStatus struct {
		Title string `json:"title,omitempty"`
		Error string `json:"error,omitempty"`
		Count int    `json:"count"`
		At    int64  `json:"at"`
	}
	status := map[string]feedStatus{}
	var all []newsItem
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for _, f := range body.Feeds {
		f := f
		if safeLink(f.URL) == "" {
			status[f.ID] = feedStatus{Error: "the address must start with http:// or https://"}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			res := getFeed(f.ID, f.URL, body.Refresh)
			<-sem
			mu.Lock()
			status[f.ID] = feedStatus{Title: res.title, Error: res.err, Count: len(res.items), At: res.at.UnixMilli()}
			all = append(all, res.items...)
			mu.Unlock()
		}()
	}
	wg.Wait()
	// one story can come from several feeds (Google News repeats others); keep the first copy
	seen := map[string]bool{}
	seenTitle := map[string]bool{}
	out := all[:0]
	sort.SliceStable(all, func(i, j int) bool { return all[i].Time > all[j].Time })
	for _, it := range all {
		tk := strings.ToLower(it.Title)
		if seen[it.ID] || seenTitle[tk] {
			continue
		}
		seen[it.ID], seenTitle[tk] = true, true
		out = append(out, it)
	}
	if len(out) > 600 {
		out = out[:600]
	}
	saveNewsCache(out)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"items": out, "feeds": status, "fetchedAt": time.Now().UnixMilli()})
}

func newsDir() string { return filepath.Join(dataDir, "news") }

func saveNewsCache(items []newsItem) {
	if len(items) == 0 {
		return
	}
	os.MkdirAll(newsDir(), 0o755)
	b, _ := json.Marshal(map[string]any{"items": items, "savedAt": time.Now().UnixMilli()})
	os.WriteFile(filepath.Join(newsDir(), "headlines.json"), b, 0o644)
}

// handleNewsCached returns the last saved headlines instantly, before any fetching.
func handleNewsCached(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	b, err := os.ReadFile(filepath.Join(newsDir(), "headlines.json"))
	if err != nil || !json.Valid(b) {
		w.Write([]byte(`{"items":[]}`))
		return
	}
	w.Write(b)
}

// ---------- economic calendar (ForexFactory weekly export) ----------

var (
	calMu    sync.Mutex
	calCache struct {
		at     time.Time
		events []map[string]any
		err    string
	}
)

func calendarURLs() []string {
	if u := os.Getenv("PROPBOOK_CAL_URL"); u != "" { // tests
		return []string{u}
	}
	return []string{
		"https://nfs.faireconomy.media/ff_calendar_thisweek.json",
		"https://nfs.faireconomy.media/ff_calendar_nextweek.json",
	}
}

func handleCalendar(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("refresh") == "1"
	calMu.Lock()
	defer calMu.Unlock()
	note := ""
	age := time.Since(calCache.at)
	if calCache.at.IsZero() {
		loadCalendarDisk()
		age = time.Since(calCache.at)
	}
	if calCache.events == nil || age > calFresh || (force && age > calMinGap) {
		var events []map[string]any
		var firstErr error
		for i, u := range calendarURLs() {
			body, err := fetchURL(u, "application/json")
			var list []map[string]any
			if err == nil {
				if err = json.Unmarshal(body, &list); err != nil {
					err = errors.New("the calendar came back in a form PropBook couldn't read")
				}
			}
			if err != nil {
				if i == 0 {
					firstErr = err
				}
				continue // next week's file isn't always published
			}
			events = append(events, list...)
		}
		if firstErr == nil && len(events) > 0 {
			calCache.events, calCache.at, calCache.err = dedupeEvents(events), time.Now(), ""
			os.MkdirAll(newsDir(), 0o755)
			b, _ := json.Marshal(map[string]any{"events": calCache.events, "at": calCache.at.UnixMilli()})
			os.WriteFile(filepath.Join(newsDir(), "calendar.json"), b, 0o644)
		} else if firstErr != nil {
			calCache.err = firstErr.Error()
		}
	} else if force {
		note = "ForexFactory only allows a refresh every few minutes, so this is the copy from " + calCache.at.Format("3:04 PM") + "."
	}
	w.Header().Set("Content-Type", "application/json")
	at := int64(0)
	if !calCache.at.IsZero() {
		at = calCache.at.UnixMilli()
	}
	json.NewEncoder(w).Encode(map[string]any{"events": calCache.events, "at": at, "error": calCache.err, "note": note})
}

func dedupeEvents(in []map[string]any) []map[string]any {
	seen := map[string]bool{}
	var out []map[string]any
	for _, e := range in {
		k := fmt.Sprint(e["date"], "|", e["country"], "|", e["title"])
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, e)
	}
	return out
}

func loadCalendarDisk() {
	b, err := os.ReadFile(filepath.Join(newsDir(), "calendar.json"))
	if err != nil {
		return
	}
	var d struct {
		Events []map[string]any `json:"events"`
		At     int64            `json:"at"`
	}
	if json.Unmarshal(b, &d) == nil && len(d.Events) > 0 {
		calCache.events, calCache.at = d.Events, time.UnixMilli(d.At)
	}
}

// ---------- TradingView widgets ----------
// The widgets are served from a second local port. Being a different web origin, the TradingView
// code inside can't read the journal page or its key.

var tvScripts = map[string]string{
	"chart":    "https://s3.tradingview.com/external-embedding/embed-widget-advanced-chart.js",
	"heatmap":  "https://s3.tradingview.com/external-embedding/embed-widget-stock-heatmap.js",
	"calendar": "https://s3.tradingview.com/external-embedding/embed-widget-events.js",
	"news":     "https://s3.tradingview.com/external-embedding/embed-widget-timeline.js",
}

var symRe = regexp.MustCompile(`^[A-Za-z0-9_:.!\-]{1,40}$`)

func widgetConfig(q url.Values) (string, map[string]any, bool) {
	kind := q.Get("w")
	theme := "dark"
	if q.Get("theme") == "light" {
		theme = "light"
	}
	switch kind {
	case "chart":
		sym := q.Get("symbol")
		if !symRe.MatchString(sym) {
			sym = "CME_MINI:NQ1!"
		}
		iv := q.Get("interval")
		if !regexp.MustCompile(`^(1|3|5|15|30|60|120|240|D|W|M)$`).MatchString(iv) {
			iv = "15"
		}
		return kind, map[string]any{"autosize": true, "symbol": sym, "interval": iv, "timezone": "America/New_York",
			"theme": theme, "style": "1", "locale": "en", "allow_symbol_change": true, "calendar": false,
			"hide_volume": false, "support_host": "https://www.tradingview.com"}, true
	case "heatmap":
		src := q.Get("source")
		if !regexp.MustCompile(`^(SPX500|NASDAQ100|DJDJI|AllUSA)$`).MatchString(src) {
			src = "SPX500"
		}
		return kind, map[string]any{"exchanges": []string{}, "dataSource": src, "grouping": "sector", "blockSize": "market_cap_basic",
			"blockColor": "change", "locale": "en", "symbolUrl": "", "colorTheme": theme, "hasTopBar": true,
			"isDataSetEnabled": true, "isZoomEnabled": true, "hasSymbolTooltip": true, "isMonoSize": false,
			"width": "100%", "height": "100%"}, true
	case "calendar":
		imp := "0,1"
		if q.Get("high") == "1" {
			imp = "1"
		}
		return kind, map[string]any{"colorTheme": theme, "isTransparent": false, "width": "100%", "height": "100%",
			"locale": "en", "importanceFilter": imp, "countryFilter": "us"}, true
	case "news":
		return kind, map[string]any{"feedMode": "market", "market": "stock", "isTransparent": false, "displayMode": "regular",
			"width": "100%", "height": "100%", "colorTheme": theme, "locale": "en"}, true
	}
	return "", nil, false
}

func serveWidgets() {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return
	}
	widgetBase = "http://" + ln.Addr().String()
	mux := http.NewServeMux()
	mux.HandleFunc("/tv", func(w http.ResponseWriter, r *http.Request) {
		kind, cfg, ok := widgetConfig(r.URL.Query())
		if !ok {
			http.NotFound(w, r)
			return
		}
		js, _ := json.Marshal(cfg) // escapes < > & so the config can't end the script tag
		bg := "#131722"
		if cfg["theme"] == "light" || cfg["colorTheme"] == "light" {
			bg = "#ffffff"
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>TradingView</title>
<style>html,body{margin:0;height:100%%;background:%s}.tradingview-widget-container{height:100%%;width:100%%}
.tradingview-widget-container__widget{height:calc(100%% - 28px);width:100%%}.tradingview-widget-copyright{font:12px system-ui,sans-serif;text-align:center;line-height:28px}
.tradingview-widget-copyright a{color:#2962ff;text-decoration:none}.off{font:14px system-ui,sans-serif;color:#888;padding:24px;text-align:center}</style></head><body>
<div class="tradingview-widget-container"><div class="tradingview-widget-container__widget"></div>
<div class="tradingview-widget-copyright"><a href="https://www.tradingview.com/" rel="noopener nofollow" target="_blank">Track all markets on TradingView</a></div>
<script type="text/javascript" src="%s" async onerror="document.body.innerHTML='<div class=off>TradingView couldn\'t be reached. Check your internet connection.</div>'">%s</script></div>
</body></html>`, bg, tvScripts[kind], js)
	})
	go http.Serve(ln, mux)
}

func handleNewsInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"widgetBase": widgetBase})
}

// openExternal opens a link in the person's normal browser. Only web addresses are allowed.
func handleOpenURL(w http.ResponseWriter, r *http.Request) {
	u := safeLink(r.URL.Query().Get("u"))
	if u == "" {
		http.Error(w, "not a web address", 400)
		return
	}
	openExternal(u)
	w.WriteHeader(204)
}
