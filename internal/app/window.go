package app

import (
	"encoding/json"
	"log"
	"math"
	"strings"
	"unsafe"

	"github.com/wailsapp/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"

	"intra/internal/config"
	"intra/ui"
)

// Window is one top-level Intra window: a title bar/pages webview, a site
// webview and its own dropdown popup.
type Window struct {
	a       *App
	hwnd    uintptr
	ui      *edge.Chromium
	site    *edge.Chromium
	siteWV  webview
	uiReady bool
	menu    *popup

	page, pageArg   string
	cur             *siteState
	active          bool
	closing         bool
	focusPending    bool
	upToDate        bool
	updateMenuEpoch uint64
	grab            pointerGrab
}

// pointerGrab is the pointer gesture currently moving or resizing the window
// without entering the Win32 modal move/size loop.
type pointerGrab struct {
	key    string
	x0, y0 int32
	orig   rect
}

// minimum window size in DIPs; the title bar needs about 420 wide
const minWindowW, minWindowH = 480, 480

var windowsByHwnd = map[uintptr]*Window{}

func (w *Window) scale() float64 { return float64(dpiOf(w.hwnd)) / 96 }

func (w *Window) barPx() int32 { return int32(math.Round(barHeightDIP * w.scale())) }

func hInstance() uintptr {
	h, _, _ := pGetModuleHandleW.Call(0)
	return h
}

func appIcon(size int) uintptr {
	// resource "APP" is the icon group embedded by tools/genicon
	h, _, _ := pLoadImageW.Call(hInstance(), ptr(u16("APP")), 1 /*IMAGE_ICON*/, uintptr(size), uintptr(size), 0)
	return h
}

func (a *App) registerWindowClass() error {
	r, g, b := a.bgColor()
	brush, _, _ := pCreateSolidBrush.Call(uintptr(r) | uintptr(g)<<8 | uintptr(b)<<16)
	cursor, _, _ := pLoadCursorW.Call(0, 32512 /*IDC_ARROW*/)
	wc := wndClassEx{
		Style:      csHRedraw | csVRedraw,
		WndProc:    windows.NewCallback(mainWndProc),
		Instance:   hInstance(),
		Icon:       appIcon(32),
		IconSm:     appIcon(16),
		Cursor:     cursor,
		Background: brush,
		ClassName:  u16(a.className()),
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if atom, _, err := pRegisterClassExW.Call(ptr(&wc)); atom == 0 {
		return err
	}
	return nil
}

// newWindow creates and shows a window for the bookmark `id` ("" for the
// home page). It uses that link's remembered placement; without one, a window
// opened from another is cascaded from it and the first window uses the home
// page placement.
func (a *App) newWindow(from *Window, id string) (*Window, error) {
	w := &Window{a: a, active: true}
	const cwUseDefault = 0x80000000
	h, _, err := pCreateWindowExW.Call(0, ptr(u16(a.className())), ptr(u16("Intra")),
		wsOverlappedWindow|wsClipChildren,
		cwUseDefault, cwUseDefault, cwUseDefault, cwUseDefault, 0, 0, hInstance(), 0)
	if h == 0 {
		return nil, err
	}
	w.hwnd = h
	windowsByHwnd[h] = w
	setWindowAppID(h)
	a.windows = append(a.windows, w)
	var saved *config.WindowState
	if b := a.cfg.Bookmark(id); b != nil {
		saved = b.Window
	}
	switch {
	case w.applyPlacement(saved):
	case from != nil:
		w.cascadeFrom(from)
	default:
		w.restorePlacement()
	}
	w.embed()
	w.menu = newPopup(w)
	a.syncLinkShortcuts()
	return w, nil
}

func (w *Window) embed() {
	a := w.a
	// --- UI: title bar + pages ---
	w.ui = a.newChromium()
	w.ui.MessageCallback = func(m string, _ *edge.ICoreWebView2, _ *edge.ICoreWebView2WebMessageReceivedEventArgs) {
		w.onMessage(false, m)
	}
	w.ui.Embed(w.hwnd)
	r, g, b := a.bgColor()
	w.ui.SetBackgroundColour(r, g, b, 255)
	lockDownUI(w.ui)
	boot := `window.__INTRA_MODE="main"`
	if enableTitlebarDrag(w.ui) {
		// must be set before the document is created; the setting applies on navigation
		boot += `;window.__INTRA_NATIVE_DRAG=1`
	}
	w.ui.Init(boot)
	w.layout()
	w.ui.NavigateToString(ui.HTML)

	// --- site ---
	w.site = a.newChromium()
	w.site.Embed(w.hwnd)
	w.site.SetBackgroundColour(r, g, b, 255)
	if s, err := w.site.GetSettings(); err == nil {
		// Sites must not reach the bridge: go-webview2 terminates the process
		// on non-string web messages, and the site has no business talking to Go.
		s.PutIsWebMessageEnabled(false)
		s.PutAreHostObjectsAllowed(false)
		s.PutIsStatusBarEnabled(true)
	}
	w.site.Hide()
	w.siteWV = webviewOf(w.site)
	w.hookSite()
	w.setRasterScale()
	w.layout()
}

func (w *Window) hookSite() {
	wv := w.siteWV
	if wv == 0 {
		return
	}
	vt := wv.vt()
	wv.on(vt.AddDocumentTitleChanged, func(_, _ uintptr) {
		if w.cur != nil {
			w.cur.Title = wv.Title()
			w.updateTitle()
			w.push()
		}
	})
	wv.on(vt.AddHistoryChanged, func(_, _ uintptr) {
		if w.cur != nil {
			w.cur.CanBack, w.cur.CanFwd = wv.CanGoBack(), wv.CanGoForward()
			w.push()
		}
	})
	wv.on(vt.AddSourceChanged, func(_, _ uintptr) {
		if w.cur != nil {
			w.cur.URL = wv.Source()
			w.push() // the address field in the links dropdown shows it
		}
	})
	wv.on(vt.AddNavigationStarting, func(_, _ uintptr) {
		if w.cur != nil && !w.cur.Loading {
			w.cur.Loading = true
			w.push()
		}
	})
	w.site.NavigationCompletedCallback = func(_ *edge.ICoreWebView2, _ *edge.ICoreWebView2NavigationCompletedEventArgs) {
		if w.cur != nil {
			w.cur.Loading = false
			w.cur.CanBack, w.cur.CanFwd = wv.CanGoBack(), wv.CanGoForward()
			w.push()
		}
	}
	// keep window.open / target=_blank inside the app window
	wv.on(vt.AddNewWindowRequested, func(_, args uintptr) {
		uri := newWindowURI(args)
		newWindowHandled(args)
		low := strings.ToLower(uri)
		switch {
		case strings.HasPrefix(low, "http://"), strings.HasPrefix(low, "https://"):
			w.site.Navigate(uri)
		case uri != "" && !strings.HasPrefix(low, "javascript:") && !strings.HasPrefix(low, "file:"):
			shellOpen(uri)
		}
	})
}

// ---------------------------------------------------------------- placement

// Window size and position are remembered per link: a window showing a link
// saves its placement onto that bookmark, a window on the home page saves to
// Config.Window.

// applyPlacement moves the window to a saved placement and shows it. It
// returns false when there is nothing usable to restore.
func (w *Window) applyPlacement(ws *config.WindowState) bool {
	if !w.a.cfg.RememberWindow || ws == nil || ws.W <= 200 || ws.H <= 150 || !onScreen(ws) {
		return false
	}
	wp := windowPlacement{
		ShowCmd:        swShowNormal,
		NormalPosition: rect{int32(ws.X), int32(ws.Y), int32(ws.X + ws.W), int32(ws.Y + ws.H)},
	}
	if ws.Maximized {
		wp.ShowCmd = swShowMaximized
	}
	wp.Length = uint32(unsafe.Sizeof(wp))
	if ok, _, err := pSetWindowPlacement.Call(w.hwnd, uintptr(unsafe.Pointer(&wp))); ok == 0 {
		log.Printf("restore window placement: %v", err)
		return false
	}
	return true
}

// restorePlacement shows the window where the home page window was last
// closed, or centered on the primary monitor's work area.
func (w *Window) restorePlacement() {
	if w.applyPlacement(w.a.cfg.Window) {
		return
	}
	s := w.scale()
	ww, hh := int32(1280*s), int32(820*s)
	mon, _, _ := pMonitorFromWindow.Call(w.hwnd, 1 /*MONITOR_DEFAULTTOPRIMARY*/)
	mi := monitorInfo{}
	mi.Size = uint32(unsafe.Sizeof(mi))
	pGetMonitorInfoW.Call(mon, ptr(&mi))
	wk := mi.Work
	ww, hh = min(ww, wk.W()-int32(40*s)), min(hh, wk.H()-int32(40*s))
	x, y := wk.Left+(wk.W()-ww)/2, wk.Top+(wk.H()-hh)/2
	pSetWindowPos.Call(w.hwnd, 0, uintptr(x), uintptr(y), uintptr(ww), uintptr(hh), swpNoZOrder|swpFrameChanged)
	pShowWindow.Call(w.hwnd, swShowNormal)
}

// cascadeFrom places the window at the source window's normal position,
// offset down and to the right, keeping it inside the work area.
func (w *Window) cascadeFrom(from *Window) {
	wp := windowPlacement{}
	wp.Length = uint32(unsafe.Sizeof(wp))
	pGetWindowPlacement.Call(from.hwnd, ptr(&wp))
	n := wp.NormalPosition
	s := from.scale()
	off := int32(32 * s)
	mon, _, _ := pMonitorFromWindow.Call(from.hwnd, 2 /*MONITOR_DEFAULTTONEAREST*/)
	mi := monitorInfo{}
	mi.Size = uint32(unsafe.Sizeof(mi))
	pGetMonitorInfoW.Call(mon, ptr(&mi))
	x, y := n.Left+off, n.Top+off
	if x+n.W() > mi.Work.Right || y+n.H() > mi.Work.Bottom {
		x, y = mi.Work.Left+off, mi.Work.Top+off
	}
	out := windowPlacement{ShowCmd: swShowNormal, NormalPosition: rect{x, y, x + n.W(), y + n.H()}}
	if isZoomed(from.hwnd) {
		out.ShowCmd = swShowMaximized
	}
	out.Length = uint32(unsafe.Sizeof(out))
	pSetWindowPlacement.Call(w.hwnd, ptr(&out))
}

// onScreen reports whether the saved window's title area is on any monitor.
func onScreen(ws *config.WindowState) bool {
	pt := point{int32(ws.X + ws.W/2), int32(ws.Y + 10)}
	m, _, _ := pMonitorFromPoint.Call(uintptr(uint32(pt.X))|uintptr(uint32(pt.Y))<<32, 0 /*MONITOR_DEFAULTTONULL*/)
	return m != 0
}

// rememberPlacement saves the window's placement onto the link it shows, or
// as the home page placement.
func (w *Window) rememberPlacement() {
	cfg := w.a.cfg
	if !cfg.RememberWindow {
		return
	}
	wp := windowPlacement{}
	wp.Length = uint32(unsafe.Sizeof(wp))
	if r, _, _ := pGetWindowPlacement.Call(w.hwnd, ptr(&wp)); r == 0 {
		return
	}
	n := wp.NormalPosition
	if n.W() <= 0 || n.H() <= 0 {
		return // never overwrite a good placement with an empty one
	}
	ws := &config.WindowState{X: int(n.Left), Y: int(n.Top), W: int(n.W()), H: int(n.H()),
		Maximized: wp.ShowCmd == swShowMaximized || (wp.ShowCmd == swShowMinimized && wp.Flags&2 != 0)}
	if w.cur != nil {
		if b := cfg.Bookmark(w.cur.ID); b != nil {
			b.Window = ws
			return
		}
	}
	cfg.Window = ws
}

func (w *Window) activate() {
	if !isVisible(w.hwnd) {
		pShowWindow.Call(w.hwnd, swShow) // hidden in the tray
	}
	if isIconic(w.hwnd) {
		pShowWindow.Call(w.hwnd, swRestore)
	}
	pSetForegroundWindow.Call(w.hwnd)
}

// hideToTray hides the window; its site stays loaded until the tray or a link
// shows it again.
func (w *Window) hideToTray() {
	if w.menu != nil {
		w.menu.hide()
	}
	w.rememberPlacement()
	w.a.save()
	pShowWindow.Call(w.hwnd, swHide)
	w.layout()
}

// ---------------------------------------------------------------- layout

// layout positions the two webviews: the UI view owns the title bar strip,
// and the whole client area while a page (home, links manager…) is shown.
func (w *Window) layout() {
	if w.closing || w.ui == nil || w.ui.GetController() == nil {
		return
	}
	// Keep the last valid bounds while minimized or hidden in the tray, but
	// explicitly hide both controllers so WebView2's visibility follows the
	// native window.
	if isIconic(w.hwnd) || !isVisible(w.hwnd) {
		w.ui.Hide()
		if w.site != nil && w.site.GetController() != nil {
			w.site.Hide()
		}
		return
	}
	cr := clientRect(w.hwnd)
	if cr.W() <= 0 || cr.H() <= 0 {
		return
	}
	bar := w.barPx()
	full := w.page != "" || w.cur == nil
	if full {
		w.ui.SetSize(edge.Rect{Left: 0, Top: 0, Right: cr.Right, Bottom: cr.Bottom})
	} else {
		w.ui.SetSize(edge.Rect{Left: 0, Top: 0, Right: cr.Right, Bottom: bar})
	}
	w.ui.Show()
	if w.site != nil && w.site.GetController() != nil {
		w.site.SetSize(edge.Rect{Left: 0, Top: bar, Right: cr.Right, Bottom: max(cr.Bottom, bar)})
		if full {
			w.site.Hide()
		} else {
			w.site.Show()
		}
	}
}

func (w *Window) setRasterScale() {
	s := w.scale()
	for _, c := range []*edge.Chromium{w.ui, w.site} {
		putRasterScale(c, s)
	}
	if w.menu != nil {
		putRasterScale(w.menu.wv, s)
	}
}

// putRasterScale sets ICoreWebView2Controller3::put_RasterizationScale
// (vtable slot 29). go-webview2's wrapper passes the double incorrectly; Go's
// Windows syscall path mirrors the first arguments into XMM registers, so
// passing the IEEE-754 bits through a raw call delivers the right value.
func putRasterScale(c *edge.Chromium, s float64) {
	if c == nil || c.GetController() == nil {
		return
	}
	c3 := c.GetController().GetICoreWebView2Controller3()
	if c3 == nil {
		return
	}
	comCall(uintptr(unsafe.Pointer(c3)), 29, uintptr(math.Float64bits(s)))
}

// closeController calls ICoreWebView2Controller::Close (vtable slot 24) so
// the browser side releases a window's webview while other windows live on.
func closeController(c *edge.Chromium) {
	if c == nil || c.GetController() == nil {
		return
	}
	c.ShuttingDown()
	comCall(uintptr(unsafe.Pointer(c.GetController())), 24)
}

func (w *Window) notifyMoved() {
	for _, c := range []*edge.Chromium{w.ui, w.site} {
		if c != nil {
			c.NotifyParentWindowPositionChanged()
		}
	}
}

// ---------------------------------------------------------------- page state

// startBookmark is the link the first window opens, or "" for the home page.
func (a *App) startBookmark() string {
	dp := a.cfg.DefaultPage
	if dp == "last" {
		dp = a.cfg.LastBookmark
	}
	if dp != "" && dp != "home" && a.cfg.Bookmark(dp) != nil {
		return dp
	}
	return ""
}

func (w *Window) showPage(page, arg string) {
	if page == "" && w.cur == nil {
		page = "home"
	}
	w.page, w.pageArg = page, arg
	w.layout()
	w.push()
	if page == "" {
		w.focusSite()
	} else if w.ui != nil && !isIconic(w.hwnd) {
		w.ui.Focus()
	}
}

// Activation can arrive before restore finishes showing the controllers.
// Restore focus after that transition, and only while this window is foreground.
func (w *Window) requestFocus() {
	if w.closing || w.focusPending {
		return
	}
	w.focusPending = true
	w.a.post(func() {
		w.focusPending = false
		foreground, _, _ := pGetForegroundWindow.Call()
		if w.closing || isIconic(w.hwnd) || foreground != w.hwnd || (w.menu != nil && w.menu.open) {
			return
		}
		w.layout()
		if w.page == "" && w.cur != nil {
			w.focusSite()
		} else if w.ui != nil && w.ui.GetController() != nil {
			w.ui.Focus()
		}
	})
}

func (w *Window) focusSite() {
	if w.site != nil && w.cur != nil && w.page == "" && !isIconic(w.hwnd) {
		w.site.Focus()
	}
}

// openBookmark loads a bookmark in this window.
func (w *Window) openBookmark(id string) {
	b := w.a.cfg.Bookmark(id)
	if b == nil {
		return
	}
	w.cur = &siteState{ID: b.ID, Name: b.Name, URL: b.URL, Loading: true}
	w.a.applyRecords()
	w.page, w.pageArg = "", ""
	w.layout()
	w.site.Navigate(normalizeURL(b.URL))
	w.a.cfg.LastBookmark = id
	w.updateTitle()
	w.push()
	w.focusSite()
}

func (w *Window) closeSite() {
	w.cur = nil
	if w.site != nil {
		w.site.Navigate("about:blank")
	}
	w.updateTitle()
}

func (w *Window) updateTitle() {
	t := "Intra"
	if w.cur != nil {
		name := w.cur.Name
		if w.cur.Title != "" {
			name = w.cur.Title
		}
		t = name + " - Intra"
	}
	pSetWindowTextW.Call(w.hwnd, ptr(u16(t)))
}

func (w *Window) state() *stateMsg {
	s := w.a.baseState()
	s.Current, s.Page, s.PageArg, s.Active = w.cur, w.page, w.pageArg, w.active
	s.Update.UpToDate = w.upToDate
	if w.menu != nil {
		s.MenuOpen, s.MenuKind, s.MenuArg = w.menu.open, w.menu.kind, w.menu.arg
		if w.menu.open && w.menu.kind == "certificate" && w.cur != nil && w.a.proxy != nil {
			s.Certificate = w.a.proxy.Certificate(normalizeURL(w.cur.URL))
		}
	}
	s.Maximized = w.hwnd != 0 && isZoomed(w.hwnd)
	return s
}

// push sends this window's full state to its front-ends.
func (w *Window) push() {
	if w.ui == nil || w.closing {
		return
	}
	j, err := json.Marshal(w.state())
	if err != nil {
		return
	}
	script := "window.intra&&intra.recv(" + string(j) + ")"
	if w.uiReady {
		w.ui.Eval(script)
	}
	if w.menu != nil && w.menu.ready {
		w.menu.wv.Eval(script)
	}
}

func (w *Window) toast(text, kind string) {
	if !w.uiReady || w.closing {
		return
	}
	j, _ := json.Marshal(map[string]string{"t": "toast", "text": text, "kind": kind})
	script := "window.intra&&intra.recv(" + string(j) + ")"
	// while a dropdown is open it is what the user is looking at; the main UI
	// view is only the 36px bar when a site is shown
	if w.menu != nil && w.menu.open && w.menu.ready {
		w.menu.wv.Eval(script)
		return
	}
	if w.page == "" && w.cur != nil {
		return // it would be clipped to the bar
	}
	w.ui.Eval(script)
}

// ---------------------------------------------------------------- window procedure

func mainWndProc(hwnd, m, wp, lp uintptr) uintptr {
	w := windowsByHwnd[hwnd]
	if w != nil && w.closing && m != wmDestroy {
		// the webview controllers are being torn down; any call into them now
		// fails, and go-webview2 terminates the process on such failures
		r, _, _ := pDefWindowProcW.Call(hwnd, m, wp, lp)
		return r
	}
	switch m {
	case wmNCCalcSize:
		if wp == 0 {
			break
		}
		// Keep the standard left/right/bottom resize frame but give the
		// caption area to the client, like Electron's titleBarStyle "hidden".
		p := (*nccalcsizeParams)(unsafe.Pointer(lp))
		top := p.Rgrc[0].Top
		r, _, _ := pDefWindowProcW.Call(hwnd, m, wp, lp)
		if r != 0 {
			return r
		}
		p.Rgrc[0].Top = top
		if isZoomed(hwnd) {
			dpi := dpiOf(hwnd)
			p.Rgrc[0].Top += metricForDpi(smCyFrame, dpi) + metricForDpi(smCxPaddedBordr, dpi)
		}
		return 0
	case wmNCHitTest:
		r, _, _ := pDefWindowProcW.Call(hwnd, m, wp, lp)
		if r == htClient && !isZoomed(hwnd) {
			var pt point
			pt.X, pt.Y = loword(lp), hiword(lp)
			wr := windowRect(hwnd)
			if pt.Y < wr.Top+metricForDpi(smCyFrame, dpiOf(hwnd)) {
				return htTop
			}
		}
		return r
	case wmSize:
		if w != nil {
			w.layout()
			if w.menu != nil {
				w.menu.hide()
			}
			w.push()
			// catches every way of minimizing: the bar button, the taskbar, Win+Down…
			if wp == sizeMinimized && w.a.minimizesToTray() {
				w.a.post(func() {
					if !w.closing && isIconic(w.hwnd) && isVisible(w.hwnd) {
						w.hideToTray()
					}
				})
			}
		}
	case wmSetFocus:
		if w != nil {
			w.requestFocus()
		}
	case wmShowWindow:
		if w != nil && wp != 0 {
			// sent before the window becomes visible; show the webviews after
			w.a.post(w.layout)
		}
	case wmMove, wmMoving:
		if w != nil {
			w.notifyMoved()
			if w.menu != nil && m == wmMoving {
				w.menu.hide()
			}
		}
	case wmActivate:
		if w != nil {
			active := wp&0xffff != waInactive
			if !active && w.menu != nil && lp == w.menu.hwnd {
				active = true // an open dropdown keeps the window looking active
			}
			if active != w.active {
				w.active = active
				w.push()
			}
			if wp&0xffff != waInactive {
				w.requestFocus()
			}
		}
	case wmCopyData:
		if w != nil && lp != 0 {
			cd := (*copyDataStruct)(unsafe.Pointer(lp))
			if cd.Data == 2 {
				w.a.post(w.a.showFromTray)
				return 1
			}
			if cd.Data == 1 && cd.Ptr != 0 && cd.Size >= 2 {
				id := windows.UTF16PtrToString((*uint16)(unsafe.Pointer(cd.Ptr)))
				w.a.post(func() { w.a.openFromExternal(id) })
				return 1
			}
		}
	case wmSysCommand:
		if w != nil {
			if id, ok := w.a.bookmarkForCommand(wp); ok {
				w.a.post(func() { w.a.openFromExternal(id) })
				return 0
			}
		}
	case wmGetMinMaxInfo:
		mi := (*minMaxInfo)(unsafe.Pointer(lp))
		s := float64(dpiOf(hwnd)) / 96
		mi.MinTrackSize = point{int32(minWindowW * s), int32(minWindowH * s)}
		return 0
	case wmDpiChanged:
		r := (*rect)(unsafe.Pointer(lp))
		pSetWindowPos.Call(hwnd, 0, uintptr(r.Left), uintptr(r.Top), uintptr(r.W()), uintptr(r.H()), swpNoZOrder|swpNoActivate)
		if w != nil {
			w.setRasterScale()
			w.layout()
		}
		return 0
	case wmClose:
		if w != nil {
			w.a.closeWindow(w)
		}
		pDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		delete(windowsByHwnd, hwnd)
		if theApp != nil && len(theApp.windows) == 0 {
			pPostQuitMessage.Call(0)
		}
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, m, wp, lp)
	return r
}

// ---------------------------------------------------------------- window actions

// trackPointer moves or resizes the window to follow the pointer. It uses
// SetWindowPos instead of WM_NCLBUTTONDOWN: that message starts a modal loop
// on this thread while the WebView still holds the mouse, so the loop never
// sees the button go up and every later title-bar click is lost.
func (w *Window) trackPointer(kind, edge string, x0, y0, x, y float64) {
	if w.closing || isIconic(w.hwnd) {
		return
	}
	if kind == "resize" && isZoomed(w.hwnd) {
		return
	}
	sx0, sy0 := int32(math.Round(x0)), int32(math.Round(y0))
	key := kind
	if kind == "resize" {
		key += ":" + edge
	}
	if w.grab.key != key || w.grab.x0 != sx0 || w.grab.y0 != sy0 {
		if w.menu != nil {
			w.menu.hide()
		}
		if kind == "drag" && isZoomed(w.hwnd) {
			pShowWindow.Call(w.hwnd, swRestore)
		}
		w.grab = pointerGrab{key: key, x0: sx0, y0: sy0, orig: windowRect(w.hwnd)}
	}
	s := w.scale()
	dx := int32(math.Round((x - x0) * s))
	dy := int32(math.Round((y - y0) * s))
	if dx == 0 && dy == 0 {
		return
	}
	r := w.grab.orig
	switch {
	case kind == "drag":
		r.Left += dx
		r.Right += dx
		r.Top += dy
		r.Bottom += dy
	case edge == "tl":
		r.Left += dx
		r.Top += dy
	case edge == "tr":
		r.Right += dx
		r.Top += dy
	default:
		r.Top += dy
	}
	if kind == "resize" {
		minW := int32(math.Round(minWindowW * s))
		minH := int32(math.Round(minWindowH * s))
		if r.W() < minW {
			if edge == "tl" {
				r.Left = r.Right - minW
			} else if edge == "tr" {
				r.Right = r.Left + minW
			}
		}
		if r.H() < minH {
			r.Top = r.Bottom - minH
		}
	}
	pSetWindowPos.Call(w.hwnd, 0, uintptr(r.Left), uintptr(r.Top), uintptr(r.W()), uintptr(r.H()), swpNoZOrder|swpNoActivate)
}

func (w *Window) toggleMaximize() {
	if isZoomed(w.hwnd) {
		pShowWindow.Call(w.hwnd, swRestore)
	} else {
		pShowWindow.Call(w.hwnd, swShowMaximized)
	}
}

func (w *Window) showSystemMenu() {
	menu, _, _ := pGetSystemMenu.Call(w.hwnd, 0)
	if menu == 0 {
		return
	}
	const mfByCommand, mfEnabled, mfGrayed = 0, 0, 1
	zoomed := isZoomed(w.hwnd)
	set := func(cmd uintptr, on bool) {
		f := uintptr(mfByCommand | mfGrayed)
		if on {
			f = mfByCommand | mfEnabled
		}
		pEnableMenuItem.Call(menu, cmd, f)
	}
	set(scRestore, zoomed)
	set(scMove, !zoomed)
	set(scSize, !zoomed)
	set(scMinimize, true)
	set(scMaximize, !zoomed)
	set(scClose, true)
	pSetMenuDefaultItem.Call(menu, scClose, 0)
	var pt point
	pGetCursorPos.Call(ptr(&pt))
	const tpmReturnCmd, tpmRightButton = 0x0100, 0x0002
	cmd, _, _ := pTrackPopupMenu.Call(menu, tpmReturnCmd|tpmRightButton, uintptr(pt.X), uintptr(pt.Y), 0, w.hwnd, 0)
	if cmd != 0 {
		pPostMessageW.Call(w.hwnd, wmSysCommand, cmd, 0)
	}
}
