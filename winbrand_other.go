//go:build !windows

package main

// brandWindow only matters on Windows, where the taskbar would otherwise treat the journal
// window as the browser.
func brandWindow(*appWindow) {}

func closeJournalWindows(uint32) []uint32 { return nil }
func anyJournalWindow(uint32) bool        { return false }
func killTree(uint32)                     {}
