package app

import (
	"math"
	"time"
	"unsafe"

	"github.com/wailsapp/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"

	"intra/ui"
)

// dropdown widths in DIPs, by kind
var popupWidths = map[string]float64{"links": 320, "certificate": 420, "settings": 420, "about": 340, "bookmarks": 520, "edit": 480}

// panels that float centered below the title bar instead of hanging from a menu button
var centeredPanels = map[string]bool{"bookmarks": true, "edit": true}

// popup hosts the title bar dropdowns (links, settings, about) and the
// floating link manager and editor. It is a separate owned top-level window
// so it can overlap the site webview, which a DOM element in the bar could not.
type popup struct {
	w         *Window
	hwnd      uintptr
	wv        *edge.Chromium
	ready     bool
	open      bool
	kind      string // which dropdown the content currently shows
	arg       string // the link the editor panel shows, "" for a new one
	pending   bool   // open requested, waiting for the content to report its height
	switching bool   // hidden on purpose to change panels; not a dismissal
	lastKind  string // kind of the most recently hidden dropdown
	hiddenAt  time.Time
	heightCSS float64
	anchor    point // client coordinates (physical px) of the menu button's bottom-left
}

var (
	popupsByHwnd   = map[uintptr]*popup{}
	popupClassDone bool
)

func newPopup(w *Window) *popup {
	a := w.a
	p := &popup{w: w, kind: "links", heightCSS: 120}
	r, g, b := a.bgColor()
	brush, _, _ := pCreateSolidBrush.Call(uintptr(r) | uintptr(g)<<8 | uintptr(b)<<16)
	cls := a.className() + "-popup"
	wc := wndClassEx{
		Style:      csDropShadow,
		WndProc:    windows.NewCallback(popupWndProc),
		Instance:   hInstance(),
		Background: brush,
		ClassName:  u16(cls),
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if !popupClassDone {
		pRegisterClassExW.Call(ptr(&wc))
		popupClassDone = true
	}
	h, _, _ := pCreateWindowExW.Call(wsExToolWindow, ptr(u16(cls)), ptr(u16("Intra")),
		wsPopup|wsClipChildren, 0, 0, uintptr(popupWidths["links"]*w.scale()), 200, w.hwnd, 0, hInstance(), 0)
	if h == 0 {
		return p
	}
	p.hwnd = h
	popupsByHwnd[h] = p
	setDwmAttr(h, 33 /*DWMWA_WINDOW_CORNER_PREFERENCE*/, 2 /*DWMWCP_ROUND*/)

	p.wv = a.newChromium()
	p.wv.MessageCallback = func(m string, _ *edge.ICoreWebView2, _ *edge.ICoreWebView2WebMessageReceivedEventArgs) {
		w.onMessage(true, m)
	}
	p.wv.Embed(h)
	p.wv.SetBackgroundColour(r, g, b, 255)
	lockDownUI(p.wv)
	putRasterScale(p.wv, w.scale())
	p.wv.Init(`window.__INTRA_MODE="menu"`)
	p.wv.Resize()
	p.wv.NavigateToString(ui.HTML)
	return p
}

func (p *popup) setRasterScale(s float64) { putRasterScale(p.wv, s) }

func (p *popup) toggle(kind string, xCSS, yCSS float64) {
	if p.hwnd == 0 || !p.ready {
		return
	}
	if _, ok := popupWidths[kind]; !ok || centeredPanels[kind] {
		return
	}
	if p.open && kind == p.kind {
		p.hide()
		return
	}
	// the mousedown that deactivated (and so closed) this dropdown also lands
	// here; only a different button should open something
	if !p.open && kind == p.lastKind && time.Since(p.hiddenAt) < 300*time.Millisecond {
		return
	}
	s := p.w.scale()
	p.anchor = point{int32(math.Round(xCSS * s)), int32(math.Round((yCSS + 4) * s))}
	p.open = true
	if kind != p.kind {
		p.relayout(kind)
		return
	}
	p.show()
	p.w.push()
}

// openPanel shows the link manager or editor, also when switching to it from
// another panel of this popup.
func (p *popup) openPanel(kind, arg string) {
	if p.hwnd == 0 || !p.ready || (!centeredPanels[kind] && kind != "certificate" && kind != "links") {
		return
	}
	p.arg = arg
	if p.open && kind == p.kind {
		p.w.push()
		return
	}
	p.open = true
	// always re-measured: the content is rendered afresh every time it opens
	p.relayout(kind)
}

// relayout lets the page render a new panel and report its height before the
// window shows it, so it never flashes at the previous panel's size.
func (p *popup) relayout(kind string) {
	s := p.w.scale()
	p.kind = kind
	p.pending = true
	p.switching = true
	pShowWindow.Call(p.hwnd, swHide)
	p.switching = false
	// lay the content out at the new width before it measures its height
	w := int32(popupWidths[kind] * s)
	pSetWindowPos.Call(p.hwnd, 0, 0, 0, uintptr(w), uintptr(int32(p.heightCSS*s)), swpNoMove|swpNoZOrder|swpNoActivate)
	p.wv.Resize()
	p.w.push()
}

func (p *popup) show() {
	p.place()
	pShowWindow.Call(p.hwnd, swShow)
	pSetForegroundWindow.Call(p.hwnd)
	p.wv.Resize()
	p.wv.Show()
	p.wv.Focus()
}

func (p *popup) place() {
	s := p.w.scale()
	w := int32(popupWidths[p.kind] * s)
	h := int32(math.Ceil(p.heightCSS * s))
	pt := p.anchor
	if centeredPanels[p.kind] {
		pt = point{(clientRect(p.w.hwnd).W() - w) / 2, p.w.barPx() + int32(8*s)}
	}
	pClientToScreen.Call(p.w.hwnd, ptr(&pt))
	mon, _, _ := pMonitorFromWindow.Call(p.w.hwnd, 2 /*MONITOR_DEFAULTTONEAREST*/)
	mi := monitorInfo{}
	mi.Size = uint32(unsafe.Sizeof(mi))
	pGetMonitorInfoW.Call(mon, ptr(&mi))
	h = min(h, mi.Work.Bottom-pt.Y-int32(8*s))
	x := max(min(pt.X, mi.Work.Right-w-int32(4*s)), mi.Work.Left+int32(4*s))
	pSetWindowPos.Call(p.hwnd, 0, uintptr(x), uintptr(pt.Y), uintptr(w), uintptr(max(h, int32(40*s))), swpNoActivate)
}

// sized receives the content height the page measured for a dropdown kind.
func (p *popup) sized(kind string, hCSS float64) {
	if kind != p.kind || hCSS <= 0 {
		return
	}
	p.heightCSS = hCSS
	if !p.open {
		return
	}
	if p.pending {
		p.pending = false
		p.show()
		return
	}
	p.place()
}

func (p *popup) hide() {
	if p == nil || !p.open {
		return
	}
	p.open = false
	p.pending = false
	p.lastKind = p.kind
	p.hiddenAt = time.Now()
	pShowWindow.Call(p.hwnd, swHide)
	p.wv.Hide()
	p.w.push()
}

// ownedBy reports whether h is owner or one of the windows owned (directly or
// indirectly) by owner, e.g. the file picker opened from the settings dropdown.
func ownedBy(h, owner uintptr) bool {
	for i := 0; h != 0 && i < 16; i++ {
		if h == owner {
			return true
		}
		h, _, _ = pGetWindow.Call(h, 4 /*GW_OWNER*/)
	}
	return false
}

func popupWndProc(hwnd, m, wp, lp uintptr) uintptr {
	if m == wmDestroy {
		delete(popupsByHwnd, hwnd)
	}
	p := popupsByHwnd[hwnd]
	if p == nil || p.w.closing {
		r, _, _ := pDefWindowProcW.Call(hwnd, m, wp, lp)
		return r
	}
	switch m {
	case wmActivate:
		if wp&0xffff == waInactive && !ownedBy(lp, hwnd) && !p.switching {
			p.hide()
		}
	case wmSize:
		if p.wv != nil {
			p.wv.Resize()
		}
	case wmClose:
		p.hide()
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, m, wp, lp)
	return r
}
