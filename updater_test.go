package main

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"3.3.0", "3.2.0", true},
		{"v3.2.10", "3.2.9", true},
		{"3.2.9", "3.2.10", false},
		{"3.2.0", "3.2.0", false},
		{"4.0.0", "3.99.99", true},
		{"3.3.0", "dev", false},
		{"garbage", "3.2.0", false},
		{"3.3", "3.2.0", false},
	}
	for _, c := range cases {
		if got := newer(c.a, c.b); got != c.want {
			t.Errorf("newer(%q,%q)=%v want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestSumFor(t *testing.T) {
	sums := []byte("ABC123  PropBook-3.3.0-Windows.zip\ndef456 *PropBook-3.3.0-macOS.zip\n")
	if got := sumFor(sums, "PropBook-3.3.0-Windows.zip"); got != "abc123" {
		t.Fatalf("windows sum %q", got)
	}
	if got := sumFor(sums, "PropBook-3.3.0-macOS.zip"); got != "def456" {
		t.Fatalf("mac sum %q", got)
	}
	if got := sumFor(sums, "PropBook-3.3.0-Linux.zip"); got != "" {
		t.Fatalf("missing should be empty, got %q", got)
	}
}

func TestVerifySumsRejectsWrongKey(t *testing.T) {
	_, other, _ := ed25519.GenerateKey(nil)
	msg := []byte("sums")
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(other, msg))
	if err := verifySums(msg, []byte(sig)); err == nil {
		t.Fatal("a signature from another key was accepted")
	}
	if err := verifySums(msg, []byte("not base64!!")); err == nil {
		t.Fatal("junk signature was accepted")
	}
}

// The real release key signs the real sums file; with PROPBOOK_SIGNING_KEY set, check the pair matches.
func TestVerifySumsWithReleaseKey(t *testing.T) {
	seed := os.Getenv("PROPBOOK_SIGNING_KEY")
	if seed == "" {
		t.Skip("PROPBOOK_SIGNING_KEY not set")
	}
	raw, err := base64.StdEncoding.DecodeString(seed)
	if err != nil {
		t.Fatal(err)
	}
	priv := ed25519.NewKeyFromSeed(raw)
	msg := []byte("abc  PropBook-1.0.0-Windows.zip\n")
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, msg))
	if err := verifySums(msg, []byte(sig+"\n")); err != nil {
		t.Fatalf("release key doesn't match the public key built into the app: %v", err)
	}
	if err := verifySums(append(msg, 'x'), []byte(sig)); err == nil {
		t.Fatal("tampered sums were accepted")
	}
}

type entry struct {
	name, body string
	mode       os.FileMode
}

func makeZip(t *testing.T, entries []entry) *zip.Reader {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		m := e.mode
		if m == 0 {
			m = 0o644
		}
		h.SetMode(m)
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(e.body))
	}
	w.Close()
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	return zr
}

func TestSwapFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "PropBook")
	os.WriteFile(target, []byte("old"), 0o755)
	zr := makeZip(t, []entry{{"PropBook/Read Me.txt", "hi", 0}, {"PropBook/PropBook", "new", 0o755}})
	if err := swapFile(zr, "PropBook/PropBook", target); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "new" {
		t.Fatalf("target has %q", b)
	}
	if b, _ := os.ReadFile(target + ".old"); string(b) != "old" {
		t.Fatalf(".old has %q", b)
	}
	if fi, _ := os.Stat(target); fi.Mode().Perm()&0o100 == 0 {
		t.Fatal("new program isn't executable")
	}
	// missing entry leaves the working version alone
	zr2 := makeZip(t, []entry{{"PropBook/Read Me.txt", "hi", 0}})
	if err := swapFile(zr2, "PropBook/PropBook", target); err == nil {
		t.Fatal("expected an error for a zip without the program")
	}
	if b, _ := os.ReadFile(target); string(b) != "new" {
		t.Fatalf("target changed to %q after a failed swap", b)
	}
}

func TestExtractTreeRefusesUnsafePaths(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "out")
	zr := makeZip(t, []entry{{"PropBook/PropBook.app/../../evil", "x", 0}})
	if err := extractTree(zr, "PropBook/PropBook.app/", dest); err == nil {
		t.Fatal("path traversal was accepted")
	}
	zr = makeZip(t, []entry{{"PropBook/PropBook.app/link", "/etc/passwd", os.ModeSymlink | 0o777}})
	if err := extractTree(zr, "PropBook/PropBook.app/", dest); err == nil {
		t.Fatal("symlink was accepted")
	}
}

func TestSwapBundle(t *testing.T) {
	dir := t.TempDir()
	bundle := filepath.Join(dir, "PropBook.app")
	os.MkdirAll(filepath.Join(bundle, "Contents", "MacOS"), 0o755)
	os.WriteFile(filepath.Join(bundle, "Contents", "MacOS", "PropBook"), []byte("old"), 0o755)
	os.WriteFile(filepath.Join(bundle, "Contents", "stale.txt"), []byte("gone"), 0o644)
	zr := makeZip(t, []entry{
		{"PropBook/Read Me.txt", "hi", 0},
		{"PropBook/PropBook.app/Contents/Info.plist", "<plist/>", 0},
		{"PropBook/PropBook.app/Contents/MacOS/PropBook", "new", 0o755},
	})
	if err := swapBundle(zr, bundle); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(bundle, "Contents", "MacOS", "PropBook")
	if b, _ := os.ReadFile(exe); string(b) != "new" {
		t.Fatalf("bundle program has %q", b)
	}
	if fi, _ := os.Stat(exe); fi.Mode().Perm()&0o100 == 0 {
		t.Fatal("bundle program isn't executable")
	}
	if _, err := os.Stat(filepath.Join(bundle, "Contents", "stale.txt")); !os.IsNotExist(err) {
		t.Fatal("old files leaked into the new bundle")
	}
	if b, _ := os.ReadFile(filepath.Join(bundle+".old", "Contents", "MacOS", "PropBook")); string(b) != "old" {
		t.Fatal("old bundle wasn't kept aside")
	}
	// a bad zip must leave the running bundle in place
	bad := makeZip(t, []entry{{"PropBook/PropBook.app/../x", "x", 0}})
	if err := swapBundle(bad, bundle); err == nil {
		t.Fatal("unsafe bundle accepted")
	}
	if b, _ := os.ReadFile(exe); string(b) != "new" {
		t.Fatal("failed swap damaged the bundle")
	}
}
