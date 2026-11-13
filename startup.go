package main

// Shared parts of "Open PropBook when I sign in". Each OS file provides startupState and setStartup.

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func errStartup(msg string) error { return errors.New(msg) }

// startupTarget is the program the login item starts.
func startupTarget() (string, string) {
	exe, err := os.Executable()
	if err != nil {
		return "", "PropBook couldn't find its own program file"
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	if strings.HasSuffix(exe, ".old") || strings.HasSuffix(exe, ".new") {
		return "", "restart PropBook, then try again"
	}
	return exe, ""
}

// refreshStartup keeps an enabled login item pointed at this copy, in case PropBook was moved.
func refreshStartup() {
	if on, _ := startupState(); on {
		setStartup(true)
	}
}

func handleStartup(w http.ResponseWriter, r *http.Request) {
	var msg string
	if r.Method == "POST" {
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if json.NewDecoder(r.Body).Decode(&body) == nil {
			if err := setStartup(body.Enabled); err != nil {
				msg = "Couldn't change it: " + err.Error() + "."
			}
		}
	}
	on, _ := startupState()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"enabled": on, "error": msg})
}
