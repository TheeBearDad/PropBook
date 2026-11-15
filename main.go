// PropBook desktop launcher.
// Serves the journal page on 127.0.0.1, stores everything in Documents\PropBook,
// and opens it in an app window (Microsoft Edge / Chrome app mode). Closing the window quits.
package main

import (
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed web/index.html
//go:embed web/seed.json
//go:embed web/icon.png
var webFS embed.FS

var (
	dataDir  string
	dataFile string
	token    string
	mu       sync.Mutex
	lastSeen = time.Now()
	byeAt    time.Time
	seenMu   sync.Mutex
	safeName = regexp.MustCompile(`[^A-Za-z0-9_.\-]`)
)

func main() {
	home, _ := os.UserHomeDir()
	docs := filepath.Join(home, "Documents")
	if st, err := os.Stat(docs); err != nil || !st.IsDir() {
		docs = home
	}
	dataDir = filepath.Join(docs, "PropBook")
	if v := os.Getenv("PROPBOOK_DIR"); v != "" {
		dataDir = v
	} else {
		migrateLegacyFolder(filepath.Join(docs, "Trade Journal"))
	}
	inboxDir = filepath.Join(dataDir, "TradesCSV")
	for _, d := range []string{"", "backups", "screenshots", "exports", "TradesCSV", filepath.Join("TradesCSV", "Imported"), filepath.Join("TradesCSV", "Not imported")} {
		os.MkdirAll(filepath.Join(dataDir, d), 0o755)
	}
	dataFile = filepath.Join(dataDir, "journal-data.json")
	if _, err := os.Stat(dataFile); os.IsNotExist(err) {
		seed, _ := webFS.ReadFile("web/seed.json")
		os.WriteFile(dataFile, seed, 0o644)
	}
	dailyBackup()
	ensureExtension()
	noteUpdatedFrom(os.Args[1:])
	if os.Getenv("PROPBOOK_NO_WINDOW") == "" {
		go refreshStartup()
	}
	go cleanupOld()
	go updateLoop()
	loadExtKey()
	loadPending()
	if dl := filepath.Join(home, "Downloads"); dirExists(dl) {
		// "Trade Journal" is where versions before the PropBook name saved CSV exports.
		downloadsDirs = []string{filepath.Join(dl, "PropBook"), filepath.Join(dl, "Trade Journal")}
	}
	go serveExtension()
	serveWidgets()

	b := make([]byte, 16)
	rand.Read(b)
	token = hex.EncodeToString(b)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal("Couldn't start the journal: " + err.Error())
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/", ln.Addr().(*net.TCPAddr).Port)

	mux := http.NewServeMux()
	mux.HandleFunc("/", serveIndex)
	mux.HandleFunc("/icon.png", serveIcon)
	mux.HandleFunc("/favicon.ico", serveIcon)
	mux.HandleFunc("/api/data", auth(handleData))
	mux.HandleFunc("/api/shot", auth(handleShot))
	mux.HandleFunc("/api/export", auth(handleExport))
	mux.HandleFunc("/api/open", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("dir") == "inbox" {
			openFolder(inboxDir)
		} else if r.URL.Query().Get("dir") == "ext" {
			openFolder(extDir)
		} else if r.URL.Query().Get("dir") == "release" {
			openURL(releasePage())
		} else {
			openFolder(dataDir)
		}
		w.WriteHeader(204)
	}))
	mux.HandleFunc("/api/extkey", auth(func(w http.ResponseWriter, r *http.Request) {
		extSeenMu.Lock()
		seen, ago := extSeenVersion, -1
		if !extSeenAt.IsZero() {
			ago = int(time.Since(extSeenAt).Seconds())
		}
		extSeenMu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"key": extKey, "port": extPort, "listening": extListening,
			"appVersion": version, "extDir": extDir, "extVersion": extBundledVersion, "extSeen": seen, "extSeenAgo": ago, "migratedFrom": migratedFrom})
	}))
	mux.HandleFunc("/api/startup", auth(handleStartup))
	mux.HandleFunc("/api/news", auth(handleNews))
	mux.HandleFunc("/api/news/cached", auth(handleNewsCached))
	mux.HandleFunc("/api/news/info", auth(handleNewsInfo))
	mux.HandleFunc("/api/calendar", auth(handleCalendar))
	mux.HandleFunc("/api/openurl", auth(handleOpenURL))
	mux.HandleFunc("/api/update", auth(handleUpdate))
	mux.HandleFunc("/api/update/check", auth(handleUpdate))
	mux.HandleFunc("/api/update/install", auth(handleUpdate))
	mux.HandleFunc("/api/lucid/pending", auth(handlePendingGet))
	mux.HandleFunc("/api/lucid/done", auth(handlePendingDone))
	mux.HandleFunc("/api/inbox", auth(handleInbox))
	mux.HandleFunc("/api/inbox/file", auth(handleInboxFile))
	mux.HandleFunc("/api/inbox/done", auth(handleInboxDone))
	mux.HandleFunc("/api/inbox/folders", auth(handleInboxFolders))
	mux.HandleFunc("/api/ping", auth(func(w http.ResponseWriter, r *http.Request) { touch(); w.WriteHeader(204) }))
	mux.HandleFunc("/api/bye", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") == token {
			seenMu.Lock()
			byeAt = time.Now()
			seenMu.Unlock()
		}
		w.WriteHeader(204)
	})
	mux.Handle("/shots/", http.StripPrefix("/shots/", http.FileServer(http.Dir(filepath.Join(dataDir, "screenshots")))))
	go http.Serve(ln, mux)

	if os.Getenv("PROPBOOK_NO_WINDOW") != "" { // test mode: no window; an update restarts the server
		fmt.Println(url, token)
		cmd := <-restartCh
		relaunch(cmd)
		return
	}

	start := time.Now()
	win, ok := openWindow(url)
	if !ok {
		fatal("Couldn't open the journal window. Install Google Chrome, Microsoft Edge or Brave, then open PropBook again.")
	}
	defer win.close()
	brandWindow(win)
	// Quit when the window closes. After an update, close this window first, then start the new
	// version, so the new one can open the same window profile.
	// Quit when the window process exits, or when the page says goodbye and doesn't come back.
	done := win.done // nil when the default browser was used
	tick := time.NewTicker(time.Second)
	for {
		select {
		case <-done:
			if time.Since(start) > 8*time.Second {
				time.Sleep(500 * time.Millisecond)
				return
			}
			done = nil // browser handed off to an existing process; rely on page signals
		case cmd := <-restartCh:
			win.close()
			relaunch(cmd)
			return
		case <-tick.C:
			seenMu.Lock()
			bye, seen := byeAt, lastSeen
			seenMu.Unlock()
			if !bye.IsZero() && bye.After(seen) && time.Since(bye) > 4*time.Second {
				return
			}
			if time.Since(seen) > 6*time.Minute {
				return
			}
		}
	}
}

func touch() {
	seenMu.Lock()
	lastSeen = time.Now()
	seenMu.Unlock()
}

func auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Token") != token {
			http.Error(w, "forbidden", 403)
			return
		}
		touch()
		h(w, r)
	}
}

func serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	touch()
	page, _ := webFS.ReadFile("web/index.html")
	head := `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<link rel="icon" type="image/png" href="/icon.png?v=` + iconVersion() + `">` +
		`<script>window.__DESKTOP__={token:"` + token + `"};</script>` +
		`<style>:root{color-scheme:light dark}body{margin:0;font-size:14px}img{max-width:100%}[hidden]{display:none!important}</style></head><body>`
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, head)
	w.Write(page)
	io.WriteString(w, "</body></html>")
}

// The window icon: Documents\PropBook\icon.png if the user put one there, otherwise the built-in candlesticks.
func customIcon() (string, os.FileInfo) {
	for _, n := range []string{"icon.png", "icon.jpg", "icon.jpeg", "icon.ico"} {
		p := filepath.Join(dataDir, n)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Size() > 0 {
			return p, st
		}
	}
	return "", nil
}

func iconVersion() string {
	if _, st := customIcon(); st != nil {
		return fmt.Sprint(st.ModTime().Unix())
	}
	return "1"
}

func serveIcon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	if p, _ := customIcon(); p != "" {
		http.ServeFile(w, r, p)
		return
	}
	b, _ := webFS.ReadFile("web/icon.png")
	w.Header().Set("Content-Type", "image/png")
	w.Write(b)
}

func handleData(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	defer mu.Unlock()
	switch r.Method {
	case "GET":
		b, err := os.ReadFile(dataFile)
		if err != nil {
			b = []byte(`{"version":1,"trades":[],"days":{},"settings":null}`)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	case "PUT":
		body, err := io.ReadAll(io.LimitReader(r.Body, 200<<20))
		if err != nil || !json.Valid(body) {
			http.Error(w, "bad data", 400)
			return
		}
		var pretty bytes.Buffer
		if json.Indent(&pretty, body, "", "  ") == nil {
			body = pretty.Bytes()
		}
		tmp := dataFile + ".tmp"
		if err := os.WriteFile(tmp, body, 0o644); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if err := os.Rename(tmp, dataFile); err != nil {
			os.Remove(dataFile)
			if err := os.Rename(tmp, dataFile); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		}
		w.WriteHeader(204)
	default:
		http.Error(w, "method", 405)
	}
}

func handleShot(w http.ResponseWriter, r *http.Request) {
	ext := strings.ToLower(filepath.Ext(r.URL.Query().Get("name")))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
	default:
		ext = ".png"
	}
	b := make([]byte, 4)
	rand.Read(b)
	name := time.Now().Format("2006-01-02_150405") + "_" + hex.EncodeToString(b) + ext
	f, err := os.Create(filepath.Join(dataDir, "screenshots", name))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer f.Close()
	if _, err := io.Copy(f, io.LimitReader(r.Body, 25<<20)); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"id": name})
}

func handleExport(w http.ResponseWriter, r *http.Request) {
	name := safeName.ReplaceAllString(r.URL.Query().Get("name"), "_")
	if name == "" {
		name = "trade-journal.csv"
	}
	p := filepath.Join(dataDir, "exports", name)
	f, err := os.Create(p)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	io.Copy(f, io.LimitReader(r.Body, 100<<20))
	f.Close()
	openFolder(filepath.Join(dataDir, "exports"))
	json.NewEncoder(w).Encode(map[string]string{"path": p})
}

// ---- TradesCSV watch folder ----
// A .csv counts as ready once its size and modified time have stayed the same
// for two seconds, so a file still being downloaded or copied isn't read half-written.

type seenFile struct {
	size  int64
	mod   time.Time
	since time.Time
}

var (
	inboxDir  string
	inboxMu   sync.Mutex
	inboxSeen = map[string]seenFile{}
)

func handleInbox(w http.ResponseWriter, r *http.Request) {
	inboxMu.Lock()
	defer inboxMu.Unlock()
	sweepDownloads()
	type cand struct {
		name string
		info os.FileInfo
	}
	var cands []cand
	collect := func(dir, prefix string) {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if !strings.EqualFold(filepath.Ext(e.Name()), ".csv") || strings.HasPrefix(e.Name(), "~$") {
				continue
			}
			if info, err := e.Info(); err == nil {
				cands = append(cands, cand{prefix + e.Name(), info})
			}
		}
	}
	collect(inboxDir, "")
	if subs, err := os.ReadDir(inboxDir); err == nil {
		for _, d := range subs {
			if d.IsDir() && !reservedSub(d.Name()) {
				collect(filepath.Join(inboxDir, d.Name()), d.Name()+"/")
			}
		}
	}
	ready := []map[string]any{}
	present := map[string]bool{}
	now := time.Now()
	for _, c := range cands {
		info, name := c.info, c.name
		present[name] = true
		prev, ok := inboxSeen[name]
		if !ok || prev.size != info.Size() || !prev.mod.Equal(info.ModTime()) {
			inboxSeen[name] = seenFile{info.Size(), info.ModTime(), now}
			continue
		}
		if info.Size() > 0 && now.Sub(prev.since) >= 2*time.Second {
			ready = append(ready, map[string]any{"name": name, "size": info.Size()})
		}
	}
	for n := range inboxSeen {
		if !present[n] {
			delete(inboxSeen, n)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i]["name"].(string) < ready[j]["name"].(string) })
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ready)
}

func reservedSub(n string) bool {
	return strings.EqualFold(n, "Imported") || strings.EqualFold(n, "Not imported")
}

// name is "file.csv" or "Account folder/file.csv" (one level below TradesCSV).
func inboxPath(r *http.Request) (string, bool) {
	name := r.URL.Query().Get("name")
	parts := strings.Split(name, "/")
	if name == "" || len(parts) > 2 {
		return "", false
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || p != filepath.Base(p) || strings.ContainsAny(p, `\:`) {
			return "", false
		}
	}
	if len(parts) == 2 && reservedSub(parts[0]) {
		return "", false
	}
	return filepath.Join(append([]string{inboxDir}, parts...)...), true
}

var folderBad = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)

func handleInboxFolders(w http.ResponseWriter, r *http.Request) {
	var names []string
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&names); err != nil {
		http.Error(w, "bad", 400)
		return
	}
	keep := map[string]bool{}
	for _, n := range names {
		n = strings.TrimSpace(folderBad.ReplaceAllString(n, ""))
		n = strings.TrimRight(n, ". ")
		if n == "" || n == "." || n == ".." || reservedSub(n) || len(n) > 80 {
			continue
		}
		keep[strings.ToLower(n)] = true
		os.MkdirAll(filepath.Join(inboxDir, n), 0o755)
	}
	// Folders of accounts that were renamed, merged or deleted go away, but only when empty.
	if entries, err := os.ReadDir(inboxDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() || reservedSub(e.Name()) || keep[strings.ToLower(e.Name())] {
				continue
			}
			dir := filepath.Join(inboxDir, e.Name())
			if inside, err := os.ReadDir(dir); err == nil && len(inside) == 0 {
				os.Remove(dir)
			}
		}
	}
	w.WriteHeader(204)
}

func handleInboxFile(w http.ResponseWriter, r *http.Request) {
	p, ok := inboxPath(r)
	if !ok {
		http.Error(w, "bad name", 400)
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(b)
}

func handleInboxDone(w http.ResponseWriter, r *http.Request) {
	p, ok := inboxPath(r)
	if !ok {
		http.Error(w, "bad name", 400)
		return
	}
	sub := "Imported"
	if r.URL.Query().Get("ok") != "1" {
		sub = "Not imported"
	}
	key := r.URL.Query().Get("name")
	name := filepath.Base(p)
	dest := filepath.Join(inboxDir, sub, name)
	if _, err := os.Stat(dest); err == nil {
		ext := filepath.Ext(name)
		dest = filepath.Join(inboxDir, sub, strings.TrimSuffix(name, ext)+" ("+time.Now().Format("2006-01-02 150405")+")"+ext)
	}
	if err := os.Rename(p, dest); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	inboxMu.Lock()
	delete(inboxSeen, key)
	inboxMu.Unlock()
	w.WriteHeader(204)
}

// One copy of the data file per day, keeping the newest 30.
func dailyBackup() {
	b, err := os.ReadFile(dataFile)
	if err != nil {
		return
	}
	dir := filepath.Join(dataDir, "backups")
	p := filepath.Join(dir, "journal-"+time.Now().Format("2006-01-02")+".json")
	if _, err := os.Stat(p); os.IsNotExist(err) {
		os.WriteFile(p, b, 0o644)
	}
	m, _ := filepath.Glob(filepath.Join(dir, "journal-*.json"))
	sort.Strings(m)
	for len(m) > 30 {
		os.Remove(m[0])
		m = m[1:]
	}
}

// openURL opens a web page in the person's normal browser (only ever the GitHub releases page).
func openURL(u string) {
	if !strings.HasPrefix(u, "https://github.com/") {
		return
	}
	openExternal(u)
}

// openExternal hands a web address to the person's default browser.
func openExternal(u string) {
	if os.Getenv("PROPBOOK_NO_WINDOW") != "" { // tests: record instead of opening a browser
		os.WriteFile(filepath.Join(dataDir, "last-opened-url.txt"), []byte(u), 0o644)
		return
	}
	switch runtime.GOOS {
	case "windows":
		exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	case "darwin":
		exec.Command("open", u).Start()
	default:
		exec.Command("xdg-open", u).Start()
	}
}

func openFolder(p string) {
	switch runtime.GOOS {
	case "windows":
		exec.Command("explorer.exe", p).Start()
	case "darwin":
		exec.Command("open", p).Start()
	default:
		exec.Command("xdg-open", p).Start()
	}
}
