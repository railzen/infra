package app

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	dwmapi   = windows.NewLazySystemDLL("dwmapi.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")

	pRegisterClassExW         = user32.NewProc("RegisterClassExW")
	pCreateWindowExW          = user32.NewProc("CreateWindowExW")
	pDefWindowProcW           = user32.NewProc("DefWindowProcW")
	pShowWindow               = user32.NewProc("ShowWindow")
	pGetMessageW              = user32.NewProc("GetMessageW")
	pPeekMessageW             = user32.NewProc("PeekMessageW")
	pTranslateMessage         = user32.NewProc("TranslateMessage")
	pDispatchMessageW         = user32.NewProc("DispatchMessageW")
	pPostMessageW             = user32.NewProc("PostMessageW")
	pSendMessageW             = user32.NewProc("SendMessageW")
	pPostQuitMessage          = user32.NewProc("PostQuitMessage")
	pGetClientRect            = user32.NewProc("GetClientRect")
	pGetWindowRect            = user32.NewProc("GetWindowRect")
	pSetWindowPos             = user32.NewProc("SetWindowPos")
	pIsZoomed                 = user32.NewProc("IsZoomed")
	pIsIconic                 = user32.NewProc("IsIconic")
	pGetDpiForWindow          = user32.NewProc("GetDpiForWindow")
	pGetSystemMetrics         = user32.NewProc("GetSystemMetrics")
	pGetSystemMetricsForDpi   = user32.NewProc("GetSystemMetricsForDpi")
	pGetAsyncKeyState         = user32.NewProc("GetAsyncKeyState")
	pLoadImageW               = user32.NewProc("LoadImageW")
	pLoadCursorW              = user32.NewProc("LoadCursorW")
	pGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	pSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	pFindWindowW              = user32.NewProc("FindWindowW")
	pMessageBoxW              = user32.NewProc("MessageBoxW")
	pSetWindowTextW           = user32.NewProc("SetWindowTextW")
	pDestroyWindow            = user32.NewProc("DestroyWindow")
	pGetWindowPlacement       = user32.NewProc("GetWindowPlacement")
	pSetWindowPlacement       = user32.NewProc("SetWindowPlacement")
	pClientToScreen           = user32.NewProc("ClientToScreen")
	pMonitorFromWindow        = user32.NewProc("MonitorFromWindow")
	pMonitorFromPoint         = user32.NewProc("MonitorFromPoint")
	pGetMonitorInfoW          = user32.NewProc("GetMonitorInfoW")
	pGetSystemMenu            = user32.NewProc("GetSystemMenu")
	pInsertMenuW              = user32.NewProc("InsertMenuW")
	pDeleteMenu               = user32.NewProc("DeleteMenu")
	pTrackPopupMenu           = user32.NewProc("TrackPopupMenu")
	pEnableMenuItem           = user32.NewProc("EnableMenuItem")
	pSetMenuDefaultItem       = user32.NewProc("SetMenuDefaultItem")
	pGetCursorPos             = user32.NewProc("GetCursorPos")
	pSetProcessDpiAwarenessCx = user32.NewProc("SetProcessDpiAwarenessContext")
	pIsWindowVisible          = user32.NewProc("IsWindowVisible")
	pGetWindow                = user32.NewProc("GetWindow")
	pCreatePopupMenu          = user32.NewProc("CreatePopupMenu")
	pAppendMenuW              = user32.NewProc("AppendMenuW")
	pDestroyMenu              = user32.NewProc("DestroyMenu")
	pRegisterWindowMessageW   = user32.NewProc("RegisterWindowMessageW")
	pAllowSetForegroundWindow = user32.NewProc("AllowSetForegroundWindow")
	pGetDpiForSystem          = user32.NewProc("GetDpiForSystem")
	pSetTimer                 = user32.NewProc("SetTimer")
	pKillTimer                = user32.NewProc("KillTimer")
	pEnumChildWindows         = user32.NewProc("EnumChildWindows")
	pGetClassNameW            = user32.NewProc("GetClassNameW")
	pGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	pGetWindowLongPtrW        = user32.NewProc("GetWindowLongPtrW")
	pSetWindowRgn             = user32.NewProc("SetWindowRgn")
	pCreateRectRgn            = gdi32.NewProc("CreateRectRgn")
	pDeleteObject             = gdi32.NewProc("DeleteObject")
	pCreateSolidBrush         = gdi32.NewProc("CreateSolidBrush")
	pDwmSetWindowAttribute    = dwmapi.NewProc("DwmSetWindowAttribute")
	pGetModuleHandleW         = kernel32.NewProc("GetModuleHandleW")
	pShellExecuteW            = shell32.NewProc("ShellExecuteW")
)

var pMsgWaitForMultipleObjectsEx = user32.NewProc("MsgWaitForMultipleObjectsEx")

const (
	wmNull          = 0x0000
	wmDestroy       = 0x0002
	wmSize          = 0x0005
	wmMove          = 0x0003
	wmActivate      = 0x0006
	wmSetFocus      = 0x0007
	wmClose         = 0x0010
	wmShowWindow    = 0x0018
	wmCopyData      = 0x004A
	wmGetMinMaxInfo = 0x0024
	wmNCCalcSize    = 0x0083
	wmNCHitTest     = 0x0084
	wmSysCommand    = 0x0112
	wmMoving        = 0x0216
	wmTimer         = 0x0113
	wmEnterSizeMove = 0x0231
	wmExitSizeMove  = 0x0232
	wmCaptureChange = 0x0215
	wmMouseMove     = 0x0200
	wmLButtonDown   = 0x0201
	wmDpiChanged    = 0x02E0
	wmSettingChange = 0x001A
	wmLButtonUp     = 0x0202
	wmRButtonUp     = 0x0205
	wmApp           = 0x8000

	wsOverlappedWindow = 0x00CF0000
	wsPopup            = 0x80000000
	wsClipChildren     = 0x02000000
	wsExToolWindow     = 0x00000080
	wsExNoActivate     = 0x08000000
	wsExTransparent    = 0x00000020
	gwlExStyle         = ^uintptr(19) // GWL_EXSTYLE (-20)

	csHRedraw    = 0x0002
	csVRedraw    = 0x0001
	csDropShadow = 0x00020000

	swHide            = 0
	swShowNormal      = 1
	swShowMinimized   = 2
	swShowMaximized   = 3
	swShow            = 5
	swMinimize        = 6
	swRestore         = 9
	swShowNoActivate  = 4
	swShowNA          = 8
	swpNoZOrder       = 0x0004
	swpNoActivate     = 0x0010
	swpFrameChanged   = 0x0020
	swpShowWindow     = 0x0040
	swpNoSize         = 0x0001
	swpNoMove         = 0x0002
	hwndTopmostInsert = ^uintptr(0) // HWND_TOPMOST (-1)

	htClient = 1
	htTop    = 12

	smCxFrame       = 32
	smCyFrame       = 33
	smCxPaddedBordr = 92
	smCxSmIcon      = 49

	scMove         = 0xF010
	scKeyMenu      = 0xF100
	scSize         = 0xF000
	scMinimize     = 0xF020
	scMaximize     = 0xF030
	scClose        = 0xF060
	scRestore      = 0xF120
	scBookmarkBase = 0x1000 // custom system-menu items; spaced by 16, below SC_*
	scBookmarkSep  = 0x0FF0

	waInactive = 0

	sizeMinimized = 1
)

// copyDataStruct matches COPYDATASTRUCT. The padding between Size and Ptr is
// inserted by Go so the pointer stays 8-byte aligned on amd64.
type copyDataStruct struct {
	Data uintptr
	Size uint32
	Ptr  uintptr
}

type point struct{ X, Y int32 }

type rect struct{ Left, Top, Right, Bottom int32 }

func (r rect) W() int32 { return r.Right - r.Left }
func (r rect) H() int32 { return r.Bottom - r.Top }

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	_       uint32
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type nccalcsizeParams struct {
	Rgrc  [3]rect
	Lppos uintptr
}

type minMaxInfo struct {
	Reserved     point
	MaxSize      point
	MaxPosition  point
	MinTrackSize point
	MaxTrackSize point
}

type windowPlacement struct {
	Length         uint32
	Flags          uint32
	ShowCmd        uint32
	MinPosition    point
	MaxPosition    point
	NormalPosition rect
}

type monitorInfo struct {
	Size    uint32
	Monitor rect
	Work    rect
	Flags   uint32
}

func u16(s string) *uint16 {
	p, _ := windows.UTF16PtrFromString(s)
	return p
}

func ptr[T any](p *T) uintptr { return uintptr(unsafe.Pointer(p)) }

func clientRect(hwnd uintptr) rect {
	var r rect
	pGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return r
}

func windowRect(hwnd uintptr) rect {
	var r rect
	pGetWindowRect.Call(hwnd, ptr(&r))
	return r
}

func isZoomed(hwnd uintptr) bool { r, _, _ := pIsZoomed.Call(hwnd); return r != 0 }
func isIconic(hwnd uintptr) bool { r, _, _ := pIsIconic.Call(hwnd); return r != 0 }
func isVisible(hwnd uintptr) bool {
	r, _, _ := pIsWindowVisible.Call(hwnd)
	return r != 0
}

func dpiOf(hwnd uintptr) uint32 {
	if pGetDpiForWindow.Find() != nil {
		return 96
	}
	d, _, _ := pGetDpiForWindow.Call(hwnd)
	if d == 0 {
		return 96
	}
	return uint32(d)
}

func systemDPI() uint32 {
	if pGetDpiForSystem.Find() != nil {
		return 96
	}
	d, _, _ := pGetDpiForSystem.Call()
	if d == 0 {
		return 96
	}
	return uint32(d)
}

func metricForDpi(idx int, dpi uint32) int32 {
	if pGetSystemMetricsForDpi.Find() != nil {
		return 8
	}
	r, _, _ := pGetSystemMetricsForDpi.Call(uintptr(idx), uintptr(dpi))
	return int32(r)
}

func messageBox(hwnd uintptr, text, title string, flags uint32) int {
	r, _, _ := pMessageBoxW.Call(hwnd, ptr(u16(text)), ptr(u16(title)), uintptr(flags))
	return int(r)
}

func shellOpen(target string) {
	pShellExecuteW.Call(0, ptr(u16("open")), ptr(u16(target)), 0, 0, swShowNormal)
}

func setDwmAttr(hwnd uintptr, attr uint32, val int32) {
	if pDwmSetWindowAttribute.Find() != nil {
		return
	}
	pDwmSetWindowAttribute.Call(hwnd, uintptr(attr), ptr(&val), 4)
}

func loword(v uintptr) int32 { return int32(int16(v & 0xffff)) }
func hiword(v uintptr) int32 { return int32(int16((v >> 16) & 0xffff)) }
