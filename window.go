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

// shutDown closes the journal window for good before an update starts the new version. If the old window were
// still around, the new version's window would join it (same window profile) and you'd see two.
func (w *appWindow) shutDown() {
	var pid uint32
	if w != nil && w.cmd != nil && w.cmd.Process != nil {
		pid = uint32(w.cmd.Process.Pid)
	}
	owners := closeJournalWindows(pid) // Windows: ask nicely first
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		alive := false
		if w != nil && w.done != nil {
			select {
			case <-w.done:
			default:
				alive = true
			}
		}
		if !alive && !anyJournalWindow(pid) {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	w.close() // whatever is left of the process PropBook started
	killTree(pid)
	for _, o := range owners { // and any browser process the window was handed to
		if o != pid {
			killTree(o)
		}
	}
	waitProfileFree(6 * time.Second)
}

// waitProfileFree waits until no browser is using PropBook's window profile, so the next window opens fresh.
func waitProfileFree(max time.Duration) {
	dir := windowProfileDir()
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		if runtime.GOOS == "windows" {
			// Chromium keeps "lockfile" open while running; it can only be deleted once the browser has exited.
			if err := os.Remove(filepath.Join(dir, "lockfile")); err == nil || os.IsNotExist(err) {
				return
			}
		} else if _, err := os.Lstat(filepath.Join(dir, "SingletonLock")); os.IsNotExist(err) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// closeLeftoverWindows runs in a freshly updated PropBook. Older versions could leave their window open when they
// restarted; close it so the new window doesn't open beside it.
func closeLeftoverWindows() {
	owners := closeJournalWindows(0)
	if len(owners) > 0 {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) && anyJournalWindow(0) {
			time.Sleep(150 * time.Millisecond)
		}
		for _, o := range owners {
			killTree(o)
		}
	}
	waitProfileFree(8 * time.Second)
}
