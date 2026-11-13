package main

// "Open PropBook when I sign in" on Windows: a value under the current user's Run key, the
// same place Settings → Apps → Startup lists. No admin rights needed.

import (
	"syscall"
	"unsafe"
)

var (
	advapi32          = syscall.NewLazyDLL("advapi32.dll")
	pRegCreateKeyExW  = advapi32.NewProc("RegCreateKeyExW")
	pRegSetValueExW   = advapi32.NewProc("RegSetValueExW")
	pRegQueryValueExW = advapi32.NewProc("RegQueryValueExW")
	pRegDeleteValueW  = advapi32.NewProc("RegDeleteValueW")
	pRegCloseKey      = advapi32.NewProc("RegCloseKey")
)

const (
	hkeyCurrentUser = 0x80000001
	keyAllAccess    = 0xF003F
	regSZ           = 1
	runKey          = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue        = "PropBook"
)

func openRunKey() (uintptr, bool) {
	k, _ := syscall.UTF16PtrFromString(runKey)
	var h uintptr
	if r(pRegCreateKeyExW.Call(hkeyCurrentUser, uintptr(unsafe.Pointer(k)), 0, 0, 0, keyAllAccess, 0, uintptr(unsafe.Pointer(&h)), 0)) != 0 {
		return 0, false
	}
	return h, true
}

func startupCommand() (string, string) {
	exe, why := startupTarget()
	if why != "" {
		return "", why
	}
	return `"` + exe + `" --startup`, ""
}

// startupState reports the current setting; the Run value counts only if it starts PropBook.
func startupState() (bool, string) {
	h, ok := openRunKey()
	if !ok {
		return false, ""
	}
	defer pRegCloseKey.Call(h)
	name, _ := syscall.UTF16PtrFromString(runValue)
	buf := make([]uint16, 1024)
	n := uint32(len(buf) * 2)
	if r(pRegQueryValueExW.Call(h, uintptr(unsafe.Pointer(name)), 0, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))) != 0 {
		return false, ""
	}
	return syscall.UTF16ToString(buf) != "", ""
}

func setStartup(on bool) error {
	h, ok := openRunKey()
	if !ok {
		return errStartup("Windows didn't let PropBook change its startup setting")
	}
	defer pRegCloseKey.Call(h)
	name, _ := syscall.UTF16PtrFromString(runValue)
	if !on {
		pRegDeleteValueW.Call(h, uintptr(unsafe.Pointer(name)))
		return nil
	}
	cmd, why := startupCommand()
	if why != "" {
		return errStartup(why)
	}
	v, _ := syscall.UTF16FromString(cmd)
	if r(pRegSetValueExW.Call(h, uintptr(unsafe.Pointer(name)), 0, regSZ, uintptr(unsafe.Pointer(&v[0])), uintptr(len(v)*2))) != 0 {
		return errStartup("Windows didn't let PropBook change its startup setting")
	}
	return nil
}
