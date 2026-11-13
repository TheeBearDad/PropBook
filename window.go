package main

// Opens the journal in an app-style window (a Chromium-based browser in --app mode,
// with its own profile so it never touches the user's normal browsing), or in the
// default browser as a fallback.

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// appWindow is the browser process showing the journal. cmd is nil when the page
// was handed to the default browser, which we don't control.
type appWindow struct {
	cmd  *exec.Cmd
	done chan struct{} // closed when cmd exits
}

func browserCandidates() []string {
	if p := os.Getenv("PROPBOOK_BROWSER"); p != "" {
		return []string{p}
	}
	var c []string
	switch runtime.GOOS {
	case "windows":
		for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LOCALAPPDATA"} {
			base := os.Getenv(env)
			if base == "" {
				continue
			}
			c = append(c,
				filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"),
				filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"),
				filepath.Join(base, "BraveSoftware", "Brave-Browser", "Application", "brave.exe"))
		}
	case "darwin":
		home, _ := os.UserHomeDir()
		for _, root := range []string{"/Applications", filepath.Join(home, "Applications")} {
			c = append(c,
				filepath.Join(root, "Google Chrome.app", "Contents", "MacOS", "Google Chrome"),
				filepath.Join(root, "Brave Browser.app", "Contents", "MacOS", "Brave Browser"),
				filepath.Join(root, "Microsoft Edge.app", "Contents", "MacOS", "Microsoft Edge"),
				filepath.Join(root, "Chromium.app", "Contents", "MacOS", "Chromium"),
				filepath.Join(root, "Vivaldi.app", "Contents", "MacOS", "Vivaldi"))
		}
	default:
		c = append(c, "/usr/bin/microsoft-edge", "/usr/bin/google-chrome", "/usr/bin/brave-browser", "/usr/bin/chromium", "/usr/bin/chromium-browser")
	}
	return c
}

func findBrowser() string {
	for _, p := range browserCandidates() {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func windowProfileDir() string {
	base := os.Getenv("LOCALAPPDATA") // Windows
	if base == "" {
		if d, err := os.UserConfigDir(); err == nil {
			base = d // macOS: ~/Library/Application Support
		} else {
			base = dataDir
		}
	}
	return filepath.Join(base, "PropBook", "window")
}

func openWindow(url string) (*appWindow, bool) {
	if exe := findBrowser(); exe != "" {
		cmd := exec.Command(exe,
			"--app="+url,
			"--user-data-dir="+windowProfileDir(),
			"--window-size=1400,920",
			"--no-first-run", "--no-default-browser-check",
			"--disable-features=Translate,msEdgeSidebarV2,msHubApps",
		)
		if cmd.Start() == nil {
			w := &appWindow{cmd: cmd, done: make(chan struct{})}
			go func() { cmd.Wait(); close(w.done) }()
			return w, true
		}
	}
	// No Chromium browser: open in the default browser (e.g. Safari). The page's
	// heartbeat and goodbye signal still tell us when it's closed.
	var err error
	switch runtime.GOOS {
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	default:
		err = exec.Command("xdg-open", url).Start()
	}
	return &appWindow{}, err == nil
}

// close ends the browser process we started. Chrome on macOS keeps running after its
// last window closes, so without this it would linger in the Dock.
func (w *appWindow) close() {
	if w == nil || w.cmd == nil || w.cmd.Process == nil {
		return
	}
	select {
	case <-w.done:
		return
	default:
	}
	if runtime.GOOS == "windows" {
		w.cmd.Process.Kill()
	} else {
		w.cmd.Process.Signal(os.Interrupt)
		select {
		case <-w.done:
		case <-time.After(3 * time.Second):
			w.cmd.Process.Kill()
		}
	}
}
