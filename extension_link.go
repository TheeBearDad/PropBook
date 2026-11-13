package main

// Link between the browser extension and the app.
// The extension posts what it reads on the Lucid dashboard to a fixed local port,
// protected by a pairing key the user copies from the app's Account screen.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const extPort = 47321

var (
	extKey        string
	extListening  bool
	pendingMu     sync.Mutex
	pending       []json.RawMessage
	downloadsDirs []string
)

func dirExists(p string) bool { st, err := os.Stat(p); return err == nil && st.IsDir() }

func loadExtKey() {
	p := filepath.Join(dataDir, "extension-key.txt")
	if b, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(b))) >= 16 {
		extKey = strings.TrimSpace(string(b))
		return
	}
	b := make([]byte, 6)
	rand.Read(b)
	h := strings.ToUpper(hex.EncodeToString(b))
	extKey = h[0:4] + "-" + h[4:8] + "-" + h[8:12] + "-TJ" // short enough to type, 48 random bits
	if len(extKey) < 16 {
		extKey += "0000"
	}
	os.WriteFile(p, []byte(extKey), 0o600)
}

func pendingFile() string { return filepath.Join(dataDir, "lucid-pending.json") }

func loadPending() {
	b, err := os.ReadFile(pendingFile())
	if err == nil {
		json.Unmarshal(b, &pending)
	}
}

func savePendingLocked() {
	b, _ := json.Marshal(pending)
	os.WriteFile(pendingFile(), b, 0o644)
}

func handlePendingGet(w http.ResponseWriter, r *http.Request) {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if pending == nil {
		io.WriteString(w, "[]")
		return
	}
	json.NewEncoder(w).Encode(pending)
}

func handlePendingDone(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	pendingMu.Lock()
	if n > len(pending) {
		n = len(pending)
	}
	if n > 0 {
		pending = pending[n:]
		savePendingLocked()
	}
	pendingMu.Unlock()
	w.WriteHeader(204)
}

func extCORS(w http.ResponseWriter, r *http.Request) bool {
	o := r.Header.Get("Origin")
	if strings.HasPrefix(o, "chrome-extension://") || strings.HasPrefix(o, "moz-extension://") {
		w.Header().Set("Access-Control-Allow-Origin", o)
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Journal-Key, X-Ext-Version")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	}
	if r.Method == "OPTIONS" {
		w.WriteHeader(204)
		return false
	}
	if r.Header.Get("X-Journal-Key") != extKey {
		http.Error(w, "wrong key", 403)
		return false
	}
	return true
}

func serveExtension() {
	// Right after an update the previous version may still be closing, so keep trying for a while.
	var ln net.Listener
	var err error
	for i := 0; i < 30; i++ {
		if ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", extPort)); err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		return // another copy of the app is running, or something else uses the port
	}
	extListening = true
	mux := http.NewServeMux()
	mux.HandleFunc("/ext/status", func(w http.ResponseWriter, r *http.Request) {
		if !extCORS(w, r) {
			return
		}
		noteExtension(r.Header.Get("X-Ext-Version"))
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "app": "PropBook", "appVersion": version, "extVersion": extBundledVersion, "extDir": extDir})
	})
	mux.HandleFunc("/ext/lucid", func(w http.ResponseWriter, r *http.Request) {
		if !extCORS(w, r) {
			return
		}
		if r.Method != "POST" {
			http.Error(w, "method", 405)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 5<<20))
		if err != nil || !json.Valid(body) {
			http.Error(w, "bad data", 400)
			return
		}
		pendingMu.Lock()
		pending = append(pending, json.RawMessage(body))
		if len(pending) > 50 {
			pending = pending[len(pending)-50:]
		}
		savePendingLocked()
		pendingMu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	http.Serve(ln, mux)
}

// Files the extension routed into Downloads\PropBook are moved into TradesCSV
// once they've finished downloading (unchanged for two seconds).
var dlSeen = map[string]seenFile{}

func sweepDownloads() {
	for _, d := range downloadsDirs {
		sweepDownloadDir(d)
	}
}

func sweepDownloadDir(downloadsInbox string) {
	entries, err := os.ReadDir(downloadsInbox)
	if err != nil {
		return
	}
	now := time.Now()
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".csv") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		key := filepath.Join(downloadsInbox, e.Name())
		prev, ok := dlSeen[key]
		if !ok || prev.size != info.Size() || !prev.mod.Equal(info.ModTime()) {
			dlSeen[key] = seenFile{info.Size(), info.ModTime(), now}
			continue
		}
		if now.Sub(prev.since) < 2*time.Second || info.Size() == 0 {
			continue
		}
		src := filepath.Join(downloadsInbox, e.Name())
		dst := filepath.Join(inboxDir, e.Name())
		if _, err := os.Stat(dst); err == nil {
			ext := filepath.Ext(e.Name())
			dst = filepath.Join(inboxDir, strings.TrimSuffix(e.Name(), ext)+" ("+now.Format("150405")+")"+ext)
		}
		if os.Rename(src, dst) != nil {
			if b, err := os.ReadFile(src); err == nil && os.WriteFile(dst, b, 0o644) == nil {
				os.Remove(src)
			}
		}
		delete(dlSeen, key)
	}
}
