//go:build !windows

package main

// brandWindow only matters on Windows, where the taskbar would otherwise treat the journal
// window as the browser.
func brandWindow(*appWindow) {}
