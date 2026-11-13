package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// fatal shows a macOS alert (the app has no terminal) and exits.
func fatal(msg string) {
	safe := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(msg)
	script := `display alert "PropBook" message "` + safe + `" as critical buttons {"OK"} default button "OK"`
	if exec.Command("osascript", "-e", script).Run() != nil {
		fmt.Fprintln(os.Stderr, msg)
	}
	os.Exit(1)
}
