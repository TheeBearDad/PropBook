//go:build !windows && !darwin

package main

// "Open PropBook when I sign in" on Linux desktops: an XDG autostart entry.

import (
	"os"
	"path/filepath"
)

func autostartPath() string {
	d, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		d = filepath.Join(home, ".config")
	}
	return filepath.Join(d, "autostart", "propbook.desktop")
}

func startupState() (bool, string) {
	_, err := os.Stat(autostartPath())
	return err == nil, ""
}

func setStartup(on bool) error {
	if !on {
		if err := os.Remove(autostartPath()); err != nil && !os.IsNotExist(err) {
			return errStartup("couldn't remove the autostart entry")
		}
		return nil
	}
	exe, why := startupTarget()
	if why != "" {
		return errStartup(why)
	}
	entry := "[Desktop Entry]\nType=Application\nName=PropBook\nExec=\"" + exe + "\" --startup\nX-GNOME-Autostart-enabled=true\n"
	os.MkdirAll(filepath.Dir(autostartPath()), 0o755)
	if err := os.WriteFile(autostartPath(), []byte(entry), 0o644); err != nil {
		return errStartup("couldn't add the autostart entry")
	}
	return nil
}
