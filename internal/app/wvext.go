package app

// Bindings for ICoreWebView2 members that go-webview2 does not wrap
// (title, history, source, navigation start, new window requests).
// The vtable layout follows WebView2.h for ICoreWebView2.

import (
	"syscall"
	"unsafe"

	"github.com/wailsapp/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
)

type wv2Vtbl struct {
	QueryInterface, AddRef, Release                                        uintptr
	GetSettings, GetSource, Navigate, NavigateToString                     uintptr
	AddNavigationStarting, RemoveNavigationStarting                        uintptr
	AddContentLoading, RemoveContentLoading                                uintptr
	AddSourceChanged, RemoveSourceChanged                                  uintptr
	AddHistoryChanged, RemoveHistoryChanged                                uintptr
	AddNavigationCompleted, RemoveNavigationCompleted                      uintptr
	AddFrameNavigationStarting, RemoveFrameNavigationStarting              uintptr
	AddFrameNavigationCompleted, RemoveFrameNavigationCompleted            uintptr
	AddScriptDialogOpening, RemoveScriptDialogOpening                      uintptr
	AddPermissionRequested, RemovePermissionRequested                      uintptr
	AddProcessFailed, RemoveProcessFailed                                  uintptr
	AddScriptToExecuteOnDocumentCreated, RemoveScriptToExecuteOnDocCreated uintptr
	ExecuteScript, CapturePreview, Reload                                  uintptr
	PostWebMessageAsJSON, PostWebMessageAsString                           uintptr
	AddWebMessageReceived, RemoveWebMessageReceived                        uintptr
	CallDevToolsProtocolMethod, GetBrowserProcessID                        uintptr
	GetCanGoBack, GetCanGoForward, GoBack, GoForward                       uintptr
	GetDevToolsProtocolEventReceiver, Stop                                 uintptr
	AddNewWindowRequested, RemoveNewWindowRequested                        uintptr
	AddDocumentTitleChanged, RemoveDocumentTitleChanged                    uintptr
	GetDocumentTitle                                                       uintptr
}

// webview is a raw ICoreWebView2 pointer.
type webview uintptr

func webviewOf(c *edge.Chromium) webview {
	wv, err := c.GetController().GetCoreWebView2()
	if err != nil {
		return 0
	}
	return webview(unsafe.Pointer(wv))
}

func (w webview) vt() *wv2Vtbl { return *(**wv2Vtbl)(unsafe.Pointer(w)) }

//go:uintptrescapes
func (w webview) call(fn uintptr, args ...uintptr) uintptr {
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(w)}, args...)...)
	return r
}

func (w webview) str(getter uintptr) string {
	var p *uint16
	if w.call(getter, uintptr(unsafe.Pointer(&p))) != 0 || p == nil {
		return ""
	}
	s := windows.UTF16PtrToString(p)
	windows.CoTaskMemFree(unsafe.Pointer(p))
	return s
}

func (w webview) boolean(getter uintptr) bool {
	var b int32
	w.call(getter, uintptr(unsafe.Pointer(&b)))
	return b != 0
}

func (w webview) Title() string      { return w.str(w.vt().GetDocumentTitle) }
func (w webview) Source() string     { return w.str(w.vt().GetSource) }
func (w webview) CanGoBack() bool    { return w.boolean(w.vt().GetCanGoBack) }
func (w webview) CanGoForward() bool { return w.boolean(w.vt().GetCanGoForward) }
func (w webview) GoBack()            { w.call(w.vt().GoBack) }
func (w webview) GoForward()         { w.call(w.vt().GoForward) }
func (w webview) Reload()            { w.call(w.vt().Reload) }
func (w webview) Stop()              { w.call(w.vt().Stop) }

// --- generic event handler COM object -------------------------------------

type handlerVtbl struct{ QueryInterface, AddRef, Release, Invoke uintptr }

type handler struct {
	vtbl *handlerVtbl
	fn   func(sender, args uintptr)
}

var (
	theHandlerVtbl = &handlerVtbl{
		QueryInterface: windows.NewCallback(func(this *handler, iid, out *uintptr) uintptr {
			*out = uintptr(unsafe.Pointer(this))
			return 0
		}),
		AddRef:  windows.NewCallback(func(this *handler) uintptr { return 1 }),
		Release: windows.NewCallback(func(this *handler) uintptr { return 1 }),
		Invoke: windows.NewCallback(func(this *handler, sender, args uintptr) uintptr {
			this.fn(sender, args)
			return 0
		}),
	}
	liveHandlers []*handler // keep handlers reachable for the life of the process
)

func (w webview) on(add uintptr, fn func(sender, args uintptr)) {
	h := &handler{vtbl: theHandlerVtbl, fn: fn}
	liveHandlers = append(liveHandlers, h)
	var token int64
	w.call(add, uintptr(unsafe.Pointer(h)), uintptr(unsafe.Pointer(&token)))
}

// comCall invokes vtable slot idx on an arbitrary COM object pointer.
//
//go:uintptrescapes
func comCall(obj uintptr, idx int, args ...uintptr) uintptr {
	vt := *(*[32]uintptr)(unsafe.Pointer(*(*uintptr)(unsafe.Pointer(obj))))
	r, _, _ := syscall.SyscallN(vt[idx], append([]uintptr{obj}, args...)...)
	return r
}

// enableTitlebarDrag turns on ICoreWebView2Settings9 non-client region
// support. CSS app-region:drag then lets the window move with the system move
// loop, so it snaps to screen edges and shows Snap Assist.
//
// WebView2 implements this with a transparent child window on this thread
// that answers HTCAPTION over the drag regions. The move loop it starts runs
// here; the watchdog in sizemove.go ends one that misses the button-up.
func enableTitlebarDrag(c *edge.Chromium) bool {
	s, err := c.GetSettings()
	if err != nil || s == nil {
		return false
	}
	defer s.Release()
	// IID_ICoreWebView2Settings9
	iid := windows.GUID{
		Data1: 0x0528A73B, Data2: 0xE92D, Data3: 0x49F4,
		Data4: [8]byte{0x92, 0x7A, 0xE5, 0x47, 0xDD, 0xDA, 0xA3, 0x7D},
	}
	var s9 uintptr
	obj := uintptr(unsafe.Pointer(s))
	if comCall(obj, 0, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&s9))) != 0 || s9 == 0 {
		return false
	}
	defer comCall(s9, 2) // Release
	// put_IsNonClientRegionSupportEnabled is vtable slot 38, past comCall's table.
	vt := *(*[39]uintptr)(unsafe.Pointer(*(*uintptr)(unsafe.Pointer(s9))))
	hr, _, _ := syscall.SyscallN(vt[38], s9, 1)
	return hr == 0
}

// ICoreWebView2NewWindowRequestedEventArgs: get_Uri = 3, put_Handled = 6.
func newWindowURI(args uintptr) string {
	var p *uint16
	if comCall(args, 3, uintptr(unsafe.Pointer(&p))) != 0 || p == nil {
		return ""
	}
	s := windows.UTF16PtrToString(p)
	windows.CoTaskMemFree(unsafe.Pointer(p))
	return s
}

func newWindowHandled(args uintptr) { comCall(args, 6, 1) }
