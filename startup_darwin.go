package main

// "Open PropBook when I sign in" on macOS: a LaunchAgent in ~/Library/LaunchAgents that opens
// the app bundle at login.

import (
	"html"
	"os"
	"path/filepath"
	"strings"
)

func agentPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", "app.propbook.desktop.plist")
}

func startupState() (bool, string) {
	_, err := os.Stat(agentPath())
	return err == nil, ""
}

func setStartup(on bool) error {
	if !on {
		if err := os.Remove(agentPath()); err != nil && !os.IsNotExist(err) {
			return errStartup("couldn't remove the login item")
		}
		return nil
	}
	exe, why := startupTarget()
	if why != "" {
		return errStartup(why)
	}
	bundle := filepath.Dir(filepath.Dir(filepath.Dir(exe)))
	if !strings.HasSuffix(bundle, ".app") {
		return errStartup("PropBook isn't running as an app")
	}
	if strings.Contains(bundle, "/AppTranslocation/") {
		return errStartup("move PropBook into your Applications folder first")
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>app.propbook.desktop</string>
	<key>ProgramArguments</key>
	<array><string>/usr/bin/open</string><string>-a</string><string>` + html.EscapeString(bundle) + `</string><string>--args</string><string>--startup</string></array>
	<key>RunAtLoad</key><true/>
</dict>
</plist>
`
	os.MkdirAll(filepath.Dir(agentPath()), 0o755)
	if err := os.WriteFile(agentPath(), []byte(plist), 0o644); err != nil {
		return errStartup("couldn't add the login item")
	}
	return nil
}
