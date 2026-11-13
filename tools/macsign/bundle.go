package main

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type bundleInfo struct {
	root, contents, plistPath, exePath, ident string
}

var plistString = func(xml, key string) string {
	re := regexp.MustCompile(`<key>` + regexp.QuoteMeta(key) + `</key>\s*<string>([^<]*)</string>`)
	if m := re.FindStringSubmatch(xml); m != nil {
		return m[1]
	}
	return ""
}

func openBundle(app string) (*bundleInfo, error) {
	b := &bundleInfo{root: app, contents: filepath.Join(app, "Contents")}
	b.plistPath = filepath.Join(b.contents, "Info.plist")
	raw, err := os.ReadFile(b.plistPath)
	if err != nil {
		return nil, fmt.Errorf("reading Info.plist: %w", err)
	}
	exe := plistString(string(raw), "CFBundleExecutable")
	b.ident = plistString(string(raw), "CFBundleIdentifier")
	if exe == "" || b.ident == "" {
		return nil, errors.New("Info.plist needs CFBundleExecutable and CFBundleIdentifier")
	}
	b.exePath = filepath.Join(b.contents, "MacOS", exe)
	if _, err := os.Stat(b.exePath); err != nil {
		return nil, fmt.Errorf("main executable: %w", err)
	}
	return b, nil
}

// resourceFiles lists the files sealed by CodeResources, relative to Contents/.
// Info.plist and PkgInfo are omitted by the default rules (Info.plist is bound through
// its own special slot), the main executable is the signed code itself, and nested code
// (other files under MacOS/) isn't supported here.
func (b *bundleInfo) resourceFiles() ([]string, error) {
	var out []string
	err := filepath.WalkDir(b.contents, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(b.contents, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "_CodeSignature" {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s: symlinks are not supported", rel)
		}
		switch {
		case rel == "Info.plist" || rel == "PkgInfo" || strings.HasSuffix(rel, ".DS_Store"):
			return nil
		case p == b.exePath:
			return nil
		case strings.HasPrefix(rel, "MacOS/"):
			return fmt.Errorf("%s: extra files in MacOS/ would need their own signature", rel)
		}
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out, err
}

// Default rules from `codesign` for a bundle with a Resources/ directory.
const rulesXML = `	<key>rules</key>
	<dict>
		<key>^Resources/</key>
		<true/>
		<key>^Resources/.*\.lproj/</key>
		<dict>
			<key>optional</key>
			<true/>
			<key>weight</key>
			<real>1000</real>
		</dict>
		<key>^Resources/.*\.lproj/locversion.plist$</key>
		<dict>
			<key>omit</key>
			<true/>
			<key>weight</key>
			<real>1100</real>
		</dict>
		<key>^Resources/Base\.lproj/</key>
		<dict>
			<key>weight</key>
			<real>1010</real>
		</dict>
		<key>^version.plist$</key>
		<true/>
	</dict>
	<key>rules2</key>
	<dict>
		<key>.*\.dSYM($|/)</key>
		<dict>
			<key>weight</key>
			<real>11</real>
		</dict>
		<key>^(.*/)?\.DS_Store$</key>
		<dict>
			<key>omit</key>
			<true/>
			<key>weight</key>
			<real>2000</real>
		</dict>
		<key>^(Frameworks|SharedFrameworks|PlugIns|Plug-ins|XPCServices|Helpers|MacOS|Library/(Automator|Spotlight|LoginItems))/</key>
		<dict>
			<key>nested</key>
			<true/>
			<key>weight</key>
			<real>10</real>
		</dict>
		<key>^.*</key>
		<true/>
		<key>^Info\.plist$</key>
		<dict>
			<key>omit</key>
			<true/>
			<key>weight</key>
			<real>20</real>
		</dict>
		<key>^PkgInfo$</key>
		<dict>
			<key>omit</key>
			<true/>
			<key>weight</key>
			<real>20</real>
		</dict>
		<key>^Resources/</key>
		<dict>
			<key>weight</key>
			<real>20</real>
		</dict>
		<key>^Resources/.*\.lproj/</key>
		<dict>
			<key>optional</key>
			<true/>
			<key>weight</key>
			<real>1000</real>
		</dict>
		<key>^Resources/.*\.lproj/locversion.plist$</key>
		<dict>
			<key>omit</key>
			<true/>
			<key>weight</key>
			<real>1100</real>
		</dict>
		<key>^Resources/Base\.lproj/</key>
		<dict>
			<key>weight</key>
			<real>1010</real>
		</dict>
		<key>^[^/]+$</key>
		<dict>
			<key>nested</key>
			<true/>
			<key>weight</key>
			<real>10</real>
		</dict>
		<key>^embedded\.provisionprofile$</key>
		<dict>
			<key>weight</key>
			<real>20</real>
		</dict>
		<key>^version\.plist$</key>
		<dict>
			<key>weight</key>
			<real>20</real>
		</dict>
	</dict>
`

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func codeResources(b *bundleInfo) ([]byte, error) {
	files, err := b.resourceFiles()
	if err != nil {
		return nil, err
	}
	var v1, v2 strings.Builder
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(b.contents, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		h1 := sha1.Sum(data)
		h2 := sha256.Sum256(data)
		k := xmlEscape(rel)
		// v1 rules seal only files matching them (^Resources/, ^version.plist$).
		if strings.HasPrefix(rel, "Resources/") || rel == "version.plist" {
			fmt.Fprintf(&v1, "\t\t<key>%s</key>\n\t\t<data>\n\t\t%s\n\t\t</data>\n", k, base64.StdEncoding.EncodeToString(h1[:]))
		}
		fmt.Fprintf(&v2, "\t\t<key>%s</key>\n\t\t<dict>\n\t\t\t<key>hash</key>\n\t\t\t<data>\n\t\t\t%s\n\t\t\t</data>\n\t\t\t<key>hash2</key>\n\t\t\t<data>\n\t\t\t%s\n\t\t\t</data>\n\t\t</dict>\n",
			k, base64.StdEncoding.EncodeToString(h1[:]), base64.StdEncoding.EncodeToString(h2[:]))
	}
	doc := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>files</key>
	<dict>
` + v1.String() + `	</dict>
	<key>files2</key>
	<dict>
` + v2.String() + `	</dict>
` + rulesXML + `</dict>
</plist>
`
	return []byte(doc), nil
}

func signBundle(app string) error {
	b, err := openBundle(app)
	if err != nil {
		return err
	}
	cr, err := codeResources(b)
	if err != nil {
		return err
	}
	sigDir := filepath.Join(b.contents, "_CodeSignature")
	if err := os.MkdirAll(sigDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(sigDir, "CodeResources"), cr, 0o644); err != nil {
		return err
	}
	plist, err := os.ReadFile(b.plistPath)
	if err != nil {
		return err
	}
	sp := specials{info: sha256.Sum256(plist), requirements: sha256.Sum256(emptyRequirementSet()), resources: sha256.Sum256(cr)}

	exe, err := os.ReadFile(b.exePath)
	if err != nil {
		return err
	}
	archs, fat, err := readSlices(exe)
	if err != nil {
		return err
	}
	for i := range archs {
		signed, err := signSlice(archs[i].data, b.ident, sp)
		if err != nil {
			return fmt.Errorf("%s slice: %w", archName(archs[i].cputype), err)
		}
		archs[i].data = signed
	}
	out := archs[0].data
	if fat {
		out = writeFat(archs)
	}
	return os.WriteFile(b.exePath, out, 0o755)
}
