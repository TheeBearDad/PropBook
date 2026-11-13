package main

// Updates come from the GitHub releases of updateRepo. A release carries the platform zips plus
// SHA256SUMS.txt and SHA256SUMS.txt.sig, an ed25519 signature made with the release key. PropBook
// installs an update only when that signature checks out against updatePubKey and the zip's hash
// matches, so a tampered or half-downloaded file is never installed.

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	version    = "dev"                  // set at build time: -X main.version=3.3.0
	updateRepo = "TheeBearDad/propbook" // -X main.updateRepo=owner/name
)

// Public half of the release signing key (the private half is the PROPBOOK_SIGNING_KEY repository secret).
const updatePubKey = "j5Il2214IYaclEJ/KXkXajUhq+vJcWtvCAryCjRix/0="

type updateStatus struct {
	Current     string `json:"current"`
	Latest      string `json:"latest,omitempty"`
	Available   bool   `json:"available"`
	Notes       string `json:"notes,omitempty"`
	URL         string `json:"url,omitempty"`
	CanInstall  bool   `json:"canInstall"`
	Why         string `json:"why,omitempty"`   // why it has to be downloaded by hand
	Phase       string `json:"phase"`           // idle, checking, downloading, installing, restarting, error
	Error       string `json:"error,omitempty"` // last failure, in plain words
	CheckedAt   string `json:"checkedAt,omitempty"`
	UpdatedFrom string `json:"updatedFrom,omitempty"` // set right after an update restarts PropBook
}

type release struct {
	tag, notes, url string
	assets          map[string]string // name -> download URL
}

var (
	updMu     sync.Mutex
	upd       = updateStatus{Current: version, Phase: "idle"}
	latestRel *release
	restartCh = make(chan string, 1) // carries the command to start after this process closes its window
)

func apiBase() string {
	if v := os.Getenv("PROPBOOK_UPDATE_API"); v != "" { // used by the update tests
		return strings.TrimRight(v, "/")
	}
	return "https://api.github.com"
}

// semver compares "3.2.10" style versions; anything unparsable (a dev build) never counts as newer.
func parseVer(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func newer(candidate, current string) bool {
	a, ok1 := parseVer(candidate)
	b, ok2 := parseVer(current)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

var httpc = &http.Client{Timeout: 60 * time.Second}

func getJSON(url string, v any) error {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "PropBook/"+version)
	res, err := httpc.Do(req)
	if err != nil {
		return errors.New("couldn't reach GitHub. Check your internet connection")
	}
	defer res.Body.Close()
	if res.StatusCode == 404 {
		return errors.New("no releases have been published yet")
	}
	if res.StatusCode == 403 || res.StatusCode == 429 {
		return errors.New("GitHub is limiting update checks right now. Try again in an hour")
	}
	if res.StatusCode != 200 {
		return fmt.Errorf("GitHub answered %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(v)
}

func download(url string, limit int64) ([]byte, error) {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "PropBook/"+version)
	req.Header.Set("Accept", "application/octet-stream")
	c := &http.Client{Timeout: 10 * time.Minute}
	res, err := c.Do(req)
	if err != nil {
		return nil, errors.New("the download was interrupted. Check your internet connection")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("the download failed (%d)", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, errors.New("the download was interrupted. Check your internet connection")
	}
	if int64(len(b)) > limit {
		return nil, errors.New("the download is larger than expected")
	}
	return b, nil
}

func assetName(ver string) string {
	switch runtime.GOOS {
	case "windows":
		return "PropBook-" + ver + "-Windows.zip"
	case "darwin":
		return "PropBook-" + ver + "-macOS.zip"
	}
	return "PropBook-" + ver + "-Linux.zip"
}

func checkForUpdate() {
	updMu.Lock()
	if upd.Phase != "idle" && upd.Phase != "error" {
		updMu.Unlock()
		return
	}
	upd.Phase, upd.Error = "checking", ""
	updMu.Unlock()

	var r struct {
		Tag    string `json:"tag_name"`
		Body   string `json:"body"`
		URL    string `json:"html_url"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	err := getJSON(apiBase()+"/repos/"+updateRepo+"/releases/latest", &r)

	updMu.Lock()
	defer updMu.Unlock()
	upd.CheckedAt = time.Now().Format(time.RFC3339)
	upd.Phase = "idle"
	if err != nil {
		upd.Phase, upd.Error = "error", "Couldn't check for updates: "+err.Error()+"."
		return
	}
	rel := &release{tag: r.Tag, notes: r.Body, url: r.URL, assets: map[string]string{}}
	for _, a := range r.Assets {
		rel.assets[a.Name] = a.URL
	}
	latestRel = rel
	latest := strings.TrimPrefix(r.Tag, "v")
	upd.Latest, upd.Notes, upd.URL = latest, r.Body, r.URL
	upd.Available = newer(latest, version)
	upd.CanInstall, upd.Why = false, ""
	if !upd.Available {
		return
	}
	switch {
	case rel.assets[assetName(latest)] == "":
		upd.Why = "This release has no download for this computer."
	case rel.assets["SHA256SUMS.txt"] == "" || rel.assets["SHA256SUMS.txt.sig"] == "":
		upd.Why = "This release isn't signed, so it can't be installed automatically."
	default:
		if _, why := installTarget(); why != "" {
			upd.Why = why
		} else {
			upd.CanInstall = true
		}
	}
}

func updateLoop() {
	time.Sleep(4 * time.Second)
	for {
		checkForUpdate()
		time.Sleep(6 * time.Hour)
	}
}

// installTarget is what gets replaced: the .exe on Windows, the .app bundle on macOS.
func installTarget() (string, string) {
	exe, err := os.Executable()
	if err != nil {
		return "", "PropBook couldn't find its own program file."
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	target := exe
	if runtime.GOOS == "darwin" {
		bundle := filepath.Dir(filepath.Dir(filepath.Dir(exe)))
		if !strings.HasSuffix(bundle, ".app") {
			return "", "PropBook isn't running as an app, so it can't update itself."
		}
		if strings.Contains(bundle, "/AppTranslocation/") {
			return "", "Move PropBook into your Applications folder to turn on automatic updates."
		}
		target = bundle
	}
	probe := filepath.Join(filepath.Dir(target), ".propbook-write-test")
	if err := os.WriteFile(probe, []byte("x"), 0o644); err != nil {
		return "", "PropBook's folder can't be written to, so updates have to be installed by hand."
	}
	os.Remove(probe)
	return target, ""
}

func verifySums(sums, sig []byte) error {
	pub, err := base64.StdEncoding.DecodeString(updatePubKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("this copy of PropBook has no release key")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || !ed25519.Verify(ed25519.PublicKey(pub), sums, raw) {
		return errors.New("the update's signature doesn't match, so it wasn't installed")
	}
	return nil
}

func sumFor(sums []byte, name string) string {
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

func setPhase(p, e string) {
	updMu.Lock()
	upd.Phase, upd.Error = p, e
	updMu.Unlock()
}

// installUpdate downloads, verifies and swaps in the new version, then asks main to restart.
func installUpdate() {
	updMu.Lock()
	rel, st := latestRel, upd
	if !st.Available || !st.CanInstall || rel == nil || (st.Phase != "idle" && st.Phase != "error") {
		updMu.Unlock()
		return
	}
	upd.Phase, upd.Error = "downloading", ""
	updMu.Unlock()

	fail := func(msg string) { setPhase("error", "Couldn't update: "+msg+".") }
	name := assetName(st.Latest)
	sums, err := download(rel.assets["SHA256SUMS.txt"], 1<<20)
	if err != nil {
		fail(err.Error())
		return
	}
	sig, err := download(rel.assets["SHA256SUMS.txt.sig"], 4<<10)
	if err != nil {
		fail(err.Error())
		return
	}
	if err := verifySums(sums, sig); err != nil {
		fail(err.Error())
		return
	}
	want := sumFor(sums, name)
	if want == "" {
		fail("the release's checksum list doesn't include " + name)
		return
	}
	zipData, err := download(rel.assets[name], 300<<20)
	if err != nil {
		fail(err.Error())
		return
	}
	if h := sha256.Sum256(zipData); hex.EncodeToString(h[:]) != want {
		fail("the downloaded file is damaged (checksum mismatch). Try again")
		return
	}
	setPhase("installing", "")
	target, why := installTarget()
	if why != "" {
		fail(why)
		return
	}
	cmd, err := swapIn(zipData, target)
	if err != nil {
		fail(err.Error())
		return
	}
	setPhase("restarting", "")
	restartCh <- cmd
}

// swapIn replaces target with the matching item from the release zip and returns how to start it.
func swapIn(zipData []byte, target string) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return "", errors.New("the download isn't a valid zip file")
	}
	if runtime.GOOS == "darwin" {
		if err := swapBundle(zr, target); err != nil {
			return "", err
		}
		return target, nil
	}
	bin := "PropBook/PropBook"
	if runtime.GOOS == "windows" {
		bin = "PropBook/PropBook.exe"
	}
	if err := swapFile(zr, bin, target); err != nil {
		return "", err
	}
	return target, nil
}

// swapFile works on a running program: Windows and Linux allow renaming a file that's in use.
func swapFile(zr *zip.Reader, entry, target string) error {
	var f *zip.File
	for _, z := range zr.File {
		if z.Name == entry {
			f = z
		}
	}
	if f == nil {
		return errors.New("the download doesn't contain " + filepath.Base(entry))
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(rc, 300<<20))
	rc.Close()
	if err != nil {
		return errors.New("the download is damaged")
	}
	newPath, oldPath := target+".new", target+".old"
	if err := os.WriteFile(newPath, data, 0o755); err != nil {
		return errors.New("couldn't write the new version next to the current one")
	}
	os.Remove(oldPath)
	if err := os.Rename(target, oldPath); err != nil {
		os.Remove(newPath)
		return errors.New("couldn't move the current version aside")
	}
	if err := os.Rename(newPath, target); err != nil {
		os.Rename(oldPath, target) // put the working version back
		os.Remove(newPath)
		return errors.New("couldn't put the new version in place")
	}
	return nil
}

// swapBundle unpacks PropBook.app from the zip next to the running one, then swaps the two folders.
func swapBundle(zr *zip.Reader, bundle string) error {
	prefix := "PropBook/" + filepath.Base(bundle) + "/"
	if !hasPrefixEntry(zr, prefix) {
		prefix = "PropBook/PropBook.app/"
	}
	staging := bundle + ".new"
	os.RemoveAll(staging)
	if err := extractTree(zr, prefix, staging); err != nil {
		os.RemoveAll(staging)
		return err
	}
	old := bundle + ".old"
	os.RemoveAll(old)
	if err := os.Rename(bundle, old); err != nil {
		os.RemoveAll(staging)
		return errors.New("couldn't move the current version aside")
	}
	if err := os.Rename(staging, bundle); err != nil {
		os.Rename(old, bundle)
		os.RemoveAll(staging)
		return errors.New("couldn't put the new version in place")
	}
	return nil
}

func hasPrefixEntry(zr *zip.Reader, prefix string) bool {
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, prefix) {
			return true
		}
	}
	return false
}

// extractTree writes the entries under prefix into dest, keeping Unix permissions and refusing
// any path that would land outside dest.
func extractTree(zr *zip.Reader, prefix, dest string) error {
	n := 0
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, prefix) {
			continue
		}
		rel := strings.TrimPrefix(f.Name, prefix)
		if rel == "" {
			continue
		}
		clean := filepath.Clean(filepath.FromSlash(rel))
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.IsAbs(clean) {
			return errors.New("the download contains an unsafe path")
		}
		p := filepath.Join(dest, clean)
		mode := f.Mode()
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		}
		if mode&os.ModeSymlink != 0 {
			return errors.New("the download contains a link, which isn't allowed")
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		perm := os.FileMode(0o644)
		if mode.Perm()&0o111 != 0 {
			perm = 0o755
		}
		out, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, io.LimitReader(rc, 300<<20))
		rc.Close()
		out.Close()
		if err != nil {
			return errors.New("the download is damaged")
		}
		n++
	}
	if n == 0 {
		return errors.New("the download doesn't contain PropBook.app")
	}
	return nil
}

// relaunch starts the freshly installed version. Called after this process has closed its window,
// so the new one can open the same window profile.
func relaunch(target string) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", "-n", target, "--args", "--updated-from", version).Start()
	}
	c := exec.Command(target, "--updated-from", version)
	if os.Getenv("PROPBOOK_NO_WINDOW") != "" { // tests read the new version's address from its output
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
	}
	return c.Start()
}

// cleanupOld removes the previous version left behind by an update.
func cleanupOld() {
	target, _ := installTarget()
	if target == "" {
		return
	}
	for i := 0; i < 10; i++ { // on Windows the old .exe stays locked until its process has exited
		err := os.RemoveAll(target + ".old")
		if err == nil || os.IsNotExist(err) {
			return
		}
		time.Sleep(2 * time.Second)
	}
}

func handleUpdate(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/update/check":
		if r.Method == "POST" {
			checkForUpdate()
		}
	case "/api/update/install":
		if r.Method == "POST" {
			go installUpdate()
			time.Sleep(150 * time.Millisecond)
		}
	}
	updMu.Lock()
	st := upd
	updMu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(st)
}

// releasePage is where people can download PropBook by hand.
func releasePage() string {
	updMu.Lock()
	u := upd.URL
	updMu.Unlock()
	if strings.HasPrefix(u, "https://github.com/") {
		return u
	}
	return "https://github.com/" + updateRepo + "/releases/latest"
}

// noteUpdatedFrom reads the --updated-from flag the old version passes when it restarts PropBook.
func noteUpdatedFrom(args []string) {
	for i, a := range args {
		if a == "--updated-from" && i+1 < len(args) {
			updMu.Lock()
			upd.UpdatedFrom = args[i+1]
			updMu.Unlock()
		}
	}
}
