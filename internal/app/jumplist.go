package app

import (
	"log"
	"os"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"

	"intra/internal/config"
)

// appUserModelID ties the running window, the taskbar button and the jump
// list together. Without it, a click on a jump-list link can open a second
// taskbar button.
const appUserModelID = "IntraWebApps.Intra"

const (
	clsctxInprocServer   = 1
	coinitApartment      = 2
	coinitDisableOle1DDE = 4
	vtLPWSTR             = 31
	swShowNormalLink     = 1
	mfByCommand          = 0x0000
	mfByPosition         = 0x0400
	mfSeparator          = 0x0800
	mfString             = 0x0000
)

var (
	ole32                                    = windows.NewLazySystemDLL("ole32.dll")
	pCoInitializeEx                          = ole32.NewProc("CoInitializeEx")
	pCoUninitialize                          = ole32.NewProc("CoUninitialize")
	pCoCreateInstance                        = ole32.NewProc("CoCreateInstance")
	pSetCurrentProcessExplicitAppUserModelID = shell32.NewProc("SetCurrentProcessExplicitAppUserModelID")
	pSHGetPropertyStoreForWindow             = shell32.NewProc("SHGetPropertyStoreForWindow")

	clsidShellLink = windows.GUID{Data1: 0x00021401, Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
	iidShellLinkW  = windows.GUID{Data1: 0x000214F9, Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}

	clsidDestinationList     = windows.GUID{Data1: 0x77f10cf0, Data2: 0x3db5, Data3: 0x4966, Data4: [8]byte{0xB5, 0x20, 0xB7, 0xC5, 0x4F, 0xD3, 0x5E, 0xD6}}
	iidCustomDestinationList = windows.GUID{Data1: 0x6332debf, Data2: 0x87b5, Data3: 0x4670, Data4: [8]byte{0x90, 0xC0, 0x5E, 0x57, 0xB4, 0x08, 0xA4, 0x9E}}

	clsidEnumerableObjectCollection = windows.GUID{Data1: 0x2d3468c1, Data2: 0x36a7, Data3: 0x43b6, Data4: [8]byte{0xac, 0x24, 0xd3, 0xf0, 0x2f, 0xd9, 0x60, 0x7a}}
	iidObjectCollection             = windows.GUID{Data1: 0x5632b1a4, Data2: 0xe38a, Data3: 0x400a, Data4: [8]byte{0x92, 0x8a, 0xd4, 0xcd, 0x63, 0x23, 0x02, 0x95}}
	iidObjectArray                  = windows.GUID{Data1: 0x92ca9dcd, Data2: 0x5622, Data3: 0x4bba, Data4: [8]byte{0xa8, 0x05, 0x5e, 0x9f, 0x54, 0x1b, 0xd8, 0xc9}}

	iidPropertyStore = windows.GUID{Data1: 0x886d8eeb, Data2: 0x8cf2, Data3: 0x4446, Data4: [8]byte{0x8d, 0x02, 0xcd, 0xba, 0x1d, 0xbd, 0xcf, 0x99}}

	pkeyTitle = propertyKey{
		fmtid: windows.GUID{Data1: 0xF29F85E0, Data2: 0x4FF9, Data3: 0x1068, Data4: [8]byte{0xAB, 0x91, 0x08, 0x00, 0x2B, 0x27, 0xB3, 0xD9}},
		pid:   2,
	}
	pkeyAppUserModelID = propertyKey{
		fmtid: windows.GUID{Data1: 0x9F4C2855, Data2: 0x9F79, Data3: 0x4B39, Data4: [8]byte{0xA8, 0xD0, 0xE1, 0xD4, 0x2D, 0xE1, 0xD5, 0xF3}},
		pid:   5,
	}
)

type propertyKey struct {
	fmtid windows.GUID
	pid   uint32
}

// propVariant is a PROPVARIANT. vt is at 0; the pointer payload is at offset 8.
type propVariant struct {
	vt  uint16
	_   uint16
	_   uint16
	_   uint16
	val uintptr
	_   uint64
}

// setProcessAppID must run before any window is created.
func setProcessAppID() {
	pSetCurrentProcessExplicitAppUserModelID.Call(ptr(u16(appUserModelID)))
}

// setWindowAppID pins this top-level window to the same taskbar identity.
func setWindowAppID(hwnd uintptr) {
	ensureCOM()
	var store uintptr
	iid := iidPropertyStore
	hr, _, _ := pSHGetPropertyStoreForWindow.Call(hwnd, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&store)))
	if hr != 0 || store == 0 {
		return
	}
	defer comCall(store, 2)
	setPropString(store, pkeyAppUserModelID, appUserModelID)
}

func ensureCOM() { pCoInitializeEx.Call(0, coinitApartment) }

func coCreate(clsid, iid windows.GUID) uintptr {
	ensureCOM()
	var obj uintptr
	hr, _, _ := pCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsid)),
		0,
		clsctxInprocServer,
		uintptr(unsafe.Pointer(&iid)),
		uintptr(unsafe.Pointer(&obj)),
	)
	if hr != 0 {
		return 0
	}
	return obj
}

func setPropString(store uintptr, key propertyKey, value string) bool {
	p, err := windows.UTF16PtrFromString(value)
	if err != nil {
		return false
	}
	pv := propVariant{vt: vtLPWSTR, val: uintptr(unsafe.Pointer(p))}
	hr := comCall(store, 6, uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(&pv)))
	runtime.KeepAlive(p)
	if hr != 0 {
		return false
	}
	return comCall(store, 7) == 0
}

// jumpListPublisher updates the taskbar jump list on a dedicated STA thread so
// Shell COM calls and disk writes never block or pump messages on the UI thread.
type jumpListPublisher struct {
	mu      sync.Mutex
	cond    *sync.Cond
	pending []config.Bookmark
	dirty   bool
}

func newJumpListPublisher() *jumpListPublisher {
	j := &jumpListPublisher{}
	j.cond = sync.NewCond(&j.mu)
	go j.loop()
	return j
}

func (j *jumpListPublisher) publish(bs []config.Bookmark) {
	cp := make([]config.Bookmark, len(bs))
	copy(cp, bs)
	j.mu.Lock()
	j.pending = cp
	j.dirty = true
	j.mu.Unlock()
	j.cond.Signal()
}

func (j *jumpListPublisher) loop() {
	runtime.LockOSThread()
	ensureCOM()
	for {
		j.mu.Lock()
		for !j.dirty {
			j.cond.Wait()
		}
		bs := j.pending
		j.pending = nil
		j.dirty = false
		j.mu.Unlock()

		publishJumpList(bs)
	}
}

// syncLinkShortcuts republishes the current bookmarks on the taskbar jump
// list and on every window's system menu.
func (a *App) syncLinkShortcuts() {
	a.bookmarkCmds = append([]string(nil), bookmarkIDs(a.cfg.Bookmarks)...)
	if a.jumpList != nil {
		a.jumpList.publish(a.cfg.Bookmarks)
	}
	for _, w := range a.windows {
		if w.hwnd != 0 && !w.closing {
			w.fillBookmarkMenu(a.cfg.Bookmarks)
		}
	}
}

func bookmarkIDs(bs []config.Bookmark) []string {
	ids := make([]string, 0, len(bs))
	for _, b := range bs {
		if validOpenID(b.ID) {
			ids = append(ids, b.ID)
		}
	}
	return ids
}

func (a *App) bookmarkForCommand(cmd uintptr) (string, bool) {
	cmd &^= 0xF // WM_SYSCOMMAND uses the low 4 bits internally
	if cmd < scBookmarkBase || (cmd-scBookmarkBase)%16 != 0 {
		return "", false
	}
	i := int((cmd - scBookmarkBase) / 16)
	if i < 0 || i >= len(a.bookmarkCmds) {
		return "", false
	}
	return a.bookmarkCmds[i], true
}

func linkLabel(b config.Bookmark) string {
	name := strings.TrimSpace(b.Name)
	if name == "" {
		name = strings.TrimSpace(b.URL)
	}
	name = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == '\t' {
			return ' '
		}
		return r
	}, name)
	if rs := []rune(name); len(rs) > 80 {
		name = string(rs[:80])
	}
	if name == "" {
		name = "链接"
	}
	return name
}

// fillBookmarkMenu puts the bookmarks at the top of the window's system menu,
// which is what a right-click on the title bar shows.
func (w *Window) fillBookmarkMenu(bs []config.Bookmark) {
	menu, _, _ := pGetSystemMenu.Call(w.hwnd, 0)
	if menu == 0 {
		return
	}
	for i := 0; i < 64; i++ {
		pDeleteMenu.Call(menu, scBookmarkBase+uintptr(i)*16, mfByCommand)
	}
	pDeleteMenu.Call(menu, scBookmarkSep, mfByCommand)
	shown := 0
	for _, b := range bs {
		if !validOpenID(b.ID) || shown >= 64 {
			continue
		}
		pInsertMenuW.Call(menu, uintptr(shown), mfByPosition|mfString, scBookmarkBase+uintptr(shown)*16, ptr(u16(strings.ReplaceAll(linkLabel(b), "&", "&&"))))
		shown++
	}
	if shown > 0 {
		pInsertMenuW.Call(menu, uintptr(shown), mfByPosition|mfSeparator, scBookmarkSep, 0)
	}
}

// publishJumpList replaces the taskbar jump list with the current bookmarks.
// Each item launches this executable with --open <id>.
func publishJumpList(bs []config.Bookmark) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	list := coCreate(clsidDestinationList, iidCustomDestinationList)
	if list == 0 {
		return
	}
	defer comCall(list, 2)
	appID := u16(appUserModelID)
	comCall(list, 3, uintptr(unsafe.Pointer(appID)))  // SetAppID
	comCall(list, 10, uintptr(unsafe.Pointer(appID))) // DeleteList, so a removed item does not block the next publish
	runtime.KeepAlive(appID)

	items := make([]config.Bookmark, 0, len(bs))
	for _, b := range bs {
		if validOpenID(b.ID) {
			items = append(items, b)
		}
	}
	if len(items) == 0 {
		return
	}
	if len(items) > 40 {
		items = items[:40]
	}
	commitJumpList(list, exe, items, len(items))
}

func commitJumpList(list uintptr, exe string, items []config.Bookmark, n int) bool {
	var slots uint32
	var removed uintptr
	iid := iidObjectArray
	if comCall(list, 4, uintptr(unsafe.Pointer(&slots)), uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&removed))) != 0 {
		return false
	}
	if removed != 0 {
		comCall(removed, 2)
	}
	col := coCreate(clsidEnumerableObjectCollection, iidObjectCollection)
	if col == 0 {
		comCall(list, 11) // AbortList
		return false
	}
	defer comCall(col, 2)
	if slots > 0 && uint32(n) > slots {
		n = int(slots)
	}
	for i := 0; i < n; i++ {
		link := shellLink(exe, items[i])
		if link == 0 {
			continue
		}
		comCall(col, 5, link) // AddObject
		comCall(link, 2)
	}
	title := u16("收藏夹")
	hr := comCall(list, 5, uintptr(unsafe.Pointer(title)), col) // AppendCategory
	runtime.KeepAlive(title)
	if hr != 0 {
		comCall(list, 11)
		return false
	}
	if comCall(list, 8) != 0 { // CommitList
		log.Printf("jump list: commit failed")
		comCall(list, 11)
		return false
	}
	return true
}

func shellLink(exe string, b config.Bookmark) uintptr {
	link := coCreate(clsidShellLink, iidShellLinkW)
	if link == 0 {
		return 0
	}
	exeP := u16(exe)
	args := u16("--open " + b.ID)
	if comCall(link, 20, uintptr(unsafe.Pointer(exeP))) != 0 { // SetPath
		comCall(link, 2)
		return 0
	}
	comCall(link, 11, uintptr(unsafe.Pointer(args)))    // SetArguments
	comCall(link, 17, uintptr(unsafe.Pointer(exeP)), 0) // SetIconLocation
	comCall(link, 15, swShowNormalLink)                 // SetShowCmd
	runtime.KeepAlive(exeP)
	runtime.KeepAlive(args)
	store := queryInterface(link, iidPropertyStore)
	if store != 0 {
		setPropString(store, pkeyTitle, linkLabel(b))
		comCall(store, 2)
	}
	return link
}

func queryInterface(obj uintptr, iid windows.GUID) uintptr {
	var out uintptr
	if comCall(obj, 0, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&out))) != 0 {
		return 0
	}
	return out
}

// parseOpenArg reads --open <id> or --open=<id> from a command line.
func parseOpenArg(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--open" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, "--open=") {
			return strings.TrimPrefix(a, "--open=")
		}
	}
	return ""
}

func validOpenID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}
