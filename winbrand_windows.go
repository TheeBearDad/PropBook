package main

// Gives the journal window PropBook's own taskbar identity. The window belongs to Edge, Chrome
// or Brave, so without this Windows groups it with the browser, and pinning it pins the browser
// (with the browser's icon) pointed at a one-time address that stops working. Setting the
// window's AppUserModel properties makes the taskbar treat it as PropBook: its own button, the
// PropBook icon, and a pin that starts PropBook.exe.

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const appUserModelID = "TheeBearDad.PropBook"

var (
	user32  = syscall.NewLazyDLL("user32.dll")
	shell32 = syscall.NewLazyDLL("shell32.dll")
	ole32   = syscall.NewLazyDLL("ole32.dll")

	pEnumWindows              = user32.NewProc("EnumWindows")
	pGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	pGetClassNameW            = user32.NewProc("GetClassNameW")
	pGetWindowTextW           = user32.NewProc("GetWindowTextW")
	pIsWindowVisible          = user32.NewProc("IsWindowVisible")
	pGetWindow                = user32.NewProc("GetWindow")
	pSendMessageW             = user32.NewProc("SendMessageW")
	pIsWindow                 = user32.NewProc("IsWindow")
	pPostMessageW             = user32.NewProc("PostMessageW")

	pSHGetPropertyStoreForWindow             = shell32.NewProc("SHGetPropertyStoreForWindow")
	pExtractIconExW                          = shell32.NewProc("ExtractIconExW")
	pSetCurrentProcessExplicitAppUserModelID = shell32.NewProc("SetCurrentProcessExplicitAppUserModelID")

	pCoInitializeEx = ole32.NewProc("CoInitializeEx")
)

type guid struct {
	d1     uint32
	d2, d3 uint16
	d4     [8]byte
}

// comObject is a COM interface pointer's target: a pointer to its method table. IPropertyStore's
// methods are QueryInterface, AddRef, Release, GetCount, GetAt, GetValue, SetValue, Commit.
type comObject struct {
	vtbl *[8]uintptr
}

type propertyKey struct {
	fmtid guid
	pid   uint32
}

// propVariant is PROPVARIANT holding a VT_LPWSTR: 16 bytes on 386, 24 on amd64/arm64.
type propVariant struct {
	vt  uint16
	_   [3]uint16
	val uintptr
	_   uintptr
}

var (
	iidIPropertyStore   = guid{0x886D8EEB, 0x8CF2, 0x4446, [8]byte{0x8D, 0x02, 0xCD, 0xBA, 0x1D, 0xBD, 0xCF, 0x99}}
	appUserModelFmtID   = guid{0x9F4C2855, 0x9F79, 0x4B39, [8]byte{0xA8, 0xD0, 0xE1, 0xD4, 0x2D, 0xE1, 0xD5, 0xF3}}
	pkeyRelaunchCommand = propertyKey{appUserModelFmtID, 2}
	pkeyRelaunchIcon    = propertyKey{appUserModelFmtID, 3}
	pkeyRelaunchName    = propertyKey{appUserModelFmtID, 4}
	pkeyID              = propertyKey{appUserModelFmtID, 5}
)

const (
	vtLPWSTR  = 31
	wmSetIcon = 0x0080
	wmClose   = 0x0010
	iconSmall = 0
	iconBig   = 1
	gwOwner   = 4
)

func init() {
	// PropBook's own process (it has no window, but keep the identity consistent).
	if id, err := syscall.UTF16PtrFromString(appUserModelID); err == nil {
		pSetCurrentProcessExplicitAppUserModelID.Call(uintptr(unsafe.Pointer(id)))
	}
}

// brandWindow finds the journal window and keeps PropBook's identity on it while it's open.
// The browser can rewrite these properties, so they are reapplied now and then.
func brandWindow(w *appWindow) {
	if w == nil || w.cmd == nil || w.cmd.Process == nil {
		return
	}
	go func() {
		runtime.LockOSThread()
		pCoInitializeEx.Call(0, 2) // COINIT_APARTMENTTHREADED
		pid := uint32(w.cmd.Process.Pid)
		exe, err := os.Executable()
		if err != nil {
			return
		}
		if p, err := filepath.EvalSymlinks(exe); err == nil {
			exe = p
		}
		bigIcon, smallIcon := loadIcons(exe)
		var hwnd uintptr
		deadline := time.Now().Add(60 * time.Second)
		for {
			select {
			case <-w.done:
				return
			default:
			}
			if hwnd == 0 || r(pIsWindow.Call(hwnd)) == 0 {
				hwnd = findJournalWindow(pid)
				if hwnd == 0 && time.Now().After(deadline) {
					return // never found it (handed off to another browser process); nothing to do
				}
			}
			if hwnd != 0 {
				setWindowIdentity(hwnd, exe)
				if bigIcon != 0 {
					pSendMessageW.Call(hwnd, wmSetIcon, iconBig, bigIcon)
				}
				if smallIcon != 0 {
					pSendMessageW.Call(hwnd, wmSetIcon, iconSmall, smallIcon)
				}
				time.Sleep(4 * time.Second)
			} else {
				time.Sleep(300 * time.Millisecond)
			}
		}
	}()
}

func r(v uintptr, _ uintptr, _ error) uintptr { return v }

func loadIcons(exe string) (uintptr, uintptr) {
	p, err := syscall.UTF16PtrFromString(exe)
	if err != nil {
		return 0, 0
	}
	var big, small uintptr
	pExtractIconExW.Call(uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(&big)), uintptr(unsafe.Pointer(&small)), 1)
	return big, small
}

// findJournalWindow returns the visible top-level browser window that belongs to the browser
// process PropBook started, or failing that, one titled exactly "PropBook".
// enumMu guards the shared search state: the window search runs from the branding loop and when updating.
var (
	enumMu       sync.Mutex
	findPid      uint32
	foundByPid   uintptr
	foundByTitle uintptr
	foundAll     []uintptr
	enumCallback = syscall.NewCallback(enumProc) // made once: Windows callbacks are never freed
)

func enumProc(h, _ uintptr) uintptr {
	if r(pIsWindowVisible.Call(h)) == 0 || r(pGetWindow.Call(h, gwOwner)) != 0 {
		return 1
	}
	cls := make([]uint16, 64)
	pGetClassNameW.Call(h, uintptr(unsafe.Pointer(&cls[0])), 64)
	if !strings.HasPrefix(syscall.UTF16ToString(cls), "Chrome_WidgetWin_") {
		return 1
	}
	var wp uint32
	pGetWindowThreadProcessId.Call(h, uintptr(unsafe.Pointer(&wp)))
	t := make([]uint16, 128)
	pGetWindowTextW.Call(h, uintptr(unsafe.Pointer(&t[0])), 128)
	byPid, byTitle := wp == findPid && findPid != 0, syscall.UTF16ToString(t) == "PropBook"
	if byPid && foundByPid == 0 {
		foundByPid = h
	}
	if byTitle && foundByTitle == 0 {
		foundByTitle = h
	}
	if byPid || byTitle {
		foundAll = append(foundAll, h)
	}
	return 1
}

// journalWindows lists every open PropBook window: the one PropBook's browser process owns, and any the browser
// handed to another of its processes (Edge often does this right after starting).
func journalWindows(pid uint32) []uintptr {
	enumMu.Lock()
	defer enumMu.Unlock()
	findPid, foundByPid, foundByTitle, foundAll = pid, 0, 0, nil
	pEnumWindows.Call(enumCallback, 0)
	return append([]uintptr(nil), foundAll...)
}

// closeJournalWindows asks each PropBook window to close the normal way, as if its X were clicked, and returns
// the browser processes that own them so they can be stopped if they don't.
func closeJournalWindows(pid uint32) []uint32 {
	var owners []uint32
	for _, h := range journalWindows(pid) {
		var wp uint32
		pGetWindowThreadProcessId.Call(h, uintptr(unsafe.Pointer(&wp)))
		owners = append(owners, wp)
		pPostMessageW.Call(h, wmClose, 0, 0)
	}
	return owners
}

func anyJournalWindow(pid uint32) bool { return len(journalWindows(pid)) > 0 }

// killTree stops a browser process and all of its helper processes.
func killTree(pid uint32) {
	if pid == 0 {
		return
	}
	c := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(int(pid)))
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // no console window
	c.Run()
}

func findJournalWindow(pid uint32) uintptr {
	enumMu.Lock()
	defer enumMu.Unlock()
	findPid, foundByPid, foundByTitle, foundAll = pid, 0, 0, nil
	pEnumWindows.Call(enumCallback, 0)
	if foundByPid != 0 {
		return foundByPid
	}
	return foundByTitle
}

func setWindowIdentity(hwnd uintptr, exe string) {
	var obj *comObject
	if hr := r(pSHGetPropertyStoreForWindow.Call(hwnd, uintptr(unsafe.Pointer(&iidIPropertyStore)), uintptr(unsafe.Pointer(&obj)))); hr != 0 || obj == nil {
		return
	}
	store := uintptr(unsafe.Pointer(obj))
	setValue, commit, release := obj.vtbl[6], obj.vtbl[7], obj.vtbl[2]
	set := func(key *propertyKey, s string) {
		p, err := syscall.UTF16PtrFromString(s)
		if err != nil {
			return
		}
		v := propVariant{vt: vtLPWSTR, val: uintptr(unsafe.Pointer(p))}
		syscall.SyscallN(setValue, store, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&v)))
		runtime.KeepAlive(p)
	}
	// The relaunch command and its name and icon go in before the ID, as Windows expects.
	set(&pkeyRelaunchCommand, `"`+exe+`"`)
	set(&pkeyRelaunchName, "PropBook")
	set(&pkeyRelaunchIcon, exe+",0")
	set(&pkeyID, appUserModelID)
	syscall.SyscallN(commit, store)
	syscall.SyscallN(release, store)
}
