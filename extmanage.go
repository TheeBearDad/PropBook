package main

// The app carries the browser extension inside it and keeps an up-to-date copy in
// Documents/PropBook/PropBook Sync. The extension is loaded (once) from that folder, so when an
// app update brings a new extension, the files on disk change and the extension reloads itself.

import (
	"embed"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed extension
var extFS embed.FS

var (
	extDir            string
	extBundledVersion string
	extSeenMu         sync.Mutex
	extSeenVersion    string
	extSeenAt         time.Time
	migratedFrom      string
)

func manifestVersion(b []byte) string {
	var m struct {
		Version string `json:"version"`
	}
	json.Unmarshal(b, &m)
	return m.Version
}

func ensureExtension() {
	extDir = filepath.Join(dataDir, "PropBook Sync")
	bundled, _ := extFS.ReadFile("extension/manifest.json")
	extBundledVersion = manifestVersion(bundled)
	onDisk, _ := os.ReadFile(filepath.Join(extDir, "manifest.json"))
	if manifestVersion(onDisk) == extBundledVersion {
		return
	}
	fs.WalkDir(extFS, "extension", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		dst := filepath.Join(extDir, filepath.FromSlash(strings.TrimPrefix(p, "extension")))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		if b, err := extFS.ReadFile(p); err == nil {
			os.WriteFile(dst, b, 0o644)
		}
		return nil
	})
}

func noteExtension(v string) {
	if v == "" {
		return
	}
	extSeenMu.Lock()
	extSeenVersion, extSeenAt = v, time.Now()
	extSeenMu.Unlock()
}

// migrateLegacyFolder moves Documents/Trade Journal (the app's old name) to Documents/PropBook.
func migrateLegacyFolder(legacy string) {
	if dirExists(dataDir) || !fileExists(filepath.Join(legacy, "journal-data.json")) {
		return
	}
	if err := os.Rename(legacy, dataDir); err != nil {
		if copyTree(legacy, dataDir) != nil {
			return
		}
	}
	migratedFrom = legacy
	// The old copy of the extension that people put inside the folder is replaced by PropBook Sync.
	old := filepath.Join(dataDir, "Trade Journal Sync")
	if b, err := os.ReadFile(filepath.Join(old, "manifest.json")); err == nil && strings.Contains(string(b), `"Trade Journal Sync"`) {
		os.RemoveAll(old)
	}
}

func fileExists(p string) bool { st, err := os.Stat(p); return err == nil && !st.IsDir() }

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		t := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(t, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(t, b, 0o644)
	})
}
