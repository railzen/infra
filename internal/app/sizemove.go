package app

import (
	"fmt"
	"log"
	"math"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The title bar drags with WebView2 non-client regions: a click on the drag
// area starts the system move loop on this thread (WM_SYSCOMMAND SC_MOVE |
// HTCAPTION), which brings snapping and Snap Assist. A move loop that never
// sees its button-up keeps the mouse captured and swallows every later click
// on the window, while hover still works because Chromium paints it. The
// watchdog below ends such a loop once the physical button is up.

const (
	timerSizeMove  = 1 // watchdog tick while a mouse-driven move/size loop runs
	timerDragCheck = 2 // compares WebView2's drag window with the UI layout
	timerDragPoll  = 3 // the same check, once a second while the window is active

	watchdogTick  = 50 * time.Millisecond
	watchdogGrace = 3 // ticks the button must stay up before the loop is ended
)

type sizeMoveLog struct {
	active  bool
	byMouse bool    // started from a mouse press (not Alt+Space → Move/Size)
	cmd     uintptr // the WM_SYSCOMMAND that starts the coming loop
	since   time.Time
	upTicks int
	ended   bool // the watchdog ended this loop
}

// onSysCommand runs before DefWindowProc handles WM_SYSCOMMAND. It returns
// true when the command was consumed.
func (w *Window) onSysCommand(wp, lp uintptr) bool {
	switch cmd := wp & 0xFFF0; cmd {
	case scMove, scSize:
		// the low bits carry the hit-test code of a mouse press: HTCAPTION
		// for a title bar drag, WMSZ_* for a frame edge; 0 from the keyboard
		w.sizeMove.cmd = wp
		w.sizeMove.byMouse = wp&0xF != 0
	}
	return false
}

func (w *Window) enterSizeMove() {
	sm := &w.sizeMove
	sm.active, sm.upTicks, sm.ended, sm.since = true, 0, false, time.Now()
	log.Printf("size/move loop: enter cmd=0x%X mouse=%v", sm.cmd, sm.byMouse)
	if sm.byMouse {
		pSetTimer.Call(w.hwnd, timerSizeMove, uintptr(watchdogTick/time.Millisecond), 0)
	}
}

func (w *Window) exitSizeMove() {
	sm := &w.sizeMove
	pKillTimer.Call(w.hwnd, timerSizeMove)
	log.Printf("size/move loop: exit after %v ended=%v", time.Since(sm.since).Round(time.Millisecond), sm.ended)
	*sm = sizeMoveLog{}
	w.scheduleDragCheck()
}

func (w *Window) onTimer(id uintptr) {
	switch id {
	case timerSizeMove:
		sm := &w.sizeMove
		if !sm.active || sm.ended {
			pKillTimer.Call(w.hwnd, timerSizeMove)
			return
		}
		if mouseDown() {
			sm.upTicks = 0
			return
		}
		if sm.upTicks++; sm.upTicks < watchdogGrace {
			return
		}
		sm.ended = true
		pKillTimer.Call(w.hwnd, timerSizeMove)
		log.Printf("size/move loop: button released %v ago but the loop is still running; ending it",
			time.Duration(sm.upTicks)*watchdogTick)
		endSizeMoveLoop(w.hwnd)
	case timerDragCheck:
		pKillTimer.Call(w.hwnd, timerDragCheck)
		w.checkDragWindow()
	case timerDragPoll:
		if w.active || len(w.drag.clipped) > 0 {
			w.checkDragWindow()
		}
	}
}

// endSizeMoveLoop hands the move loop the button-up it missed. The loop
// takes the posted message like a real one and keeps the window where it is;
// releasing the capture instead would cancel the move and snap it back.
func endSizeMoveLoop(hwnd uintptr) {
	var pt point
	pGetCursorPos.Call(ptr(&pt))
	lp := uintptr(uint16(pt.X)) | uintptr(uint16(pt.Y))<<16
	pPostMessageW.Call(hwnd, wmLButtonUp, 0, lp)
}

// ---------------------------------------------------------------- drag area

// WebView2 answers HTCAPTION over app-region:drag areas with a transparent
// child window on this thread. The check below compares that window with
// where the UI really laid out the drag area and, while they disagree, clips
// it with a window region to the UI's area. In testing, a widened drag window
// over the caption buttons still let their clicks through (WebView2 checks
// its regions again on the press), so this is a safeguard, and the log line
// records when WebView2's layout and the page disagree.

type dragGuard struct {
	css     [4]float64          // the drag area as the UI reported it: x, y, w, h in CSS px
	clipped map[uintptr][2]rect // drag window -> its client rect and the region applied, client px
	lastLog string
}

// dragArea records where the UI laid out the title bar's drag area; the
// check runs a moment later, after WebView2 has updated its regions.
func (w *Window) dragArea(x, y, wCSS, hCSS float64) {
	w.drag.css = [4]float64{x, y, wCSS, hCSS}
	w.scheduleDragCheck()
}

func (w *Window) scheduleDragCheck() {
	if w.nativeDrag && w.hwnd != 0 {
		pSetTimer.Call(w.hwnd, timerDragCheck, 600, 0)
	}
}

// startDragPoll also checks once a second while the window is active, for
// changes WebView2 makes without a resize on our side.
func (w *Window) startDragPoll() {
	if w.nativeDrag {
		pSetTimer.Call(w.hwnd, timerDragPoll, 1000, 0)
	}
}

// dragWant is the reported drag area in client px at the current DPI.
func (w *Window) dragWant() rect {
	s, c := w.scale(), w.drag.css
	return rect{
		Left: int32(math.Round(c[0] * s)), Top: int32(math.Round(c[1] * s)),
		Right: int32(math.Round((c[0] + c[2]) * s)), Bottom: int32(math.Round((c[1] + c[3]) * s)),
	}
}

func (w *Window) checkDragWindow() {
	if w.closing || !w.nativeDrag || w.drag.css[2] <= 0 || w.sizeMove.active || isIconic(w.hwnd) || !isVisible(w.hwnd) {
		return
	}
	var org point
	pClientToScreen.Call(w.hwnd, ptr(&org))
	want := w.dragWant()
	tol := int32(math.Ceil(2 * w.scale()))
	near := func(a, b rect) bool {
		d := func(x, y int32) bool { return x-y > tol || y-x > tol }
		return !(d(a.Left, b.Left) || d(a.Right, b.Right) || d(a.Top, b.Top) || d(a.Bottom, b.Bottom))
	}
	if w.drag.clipped == nil {
		w.drag.clipped = map[uintptr][2]rect{}
	}
	wins := dragWindows(w.hwnd)
	var got []rect
	var fixed []uintptr
	failed := 0
	seen := map[uintptr]bool{}
	for _, h := range wins {
		seen[h] = true
		r := windowRect(h) // a window region does not change this rectangle
		g := rect{r.Left - org.X, r.Top - org.Y, r.Right - org.X, r.Bottom - org.Y}
		got = append(got, g)
		if near(g, want) {
			w.unclipDrag(h)
			continue
		}
		if w.clipDrag(h, g, intersect(g, want)) {
			fixed = append(fixed, h)
		} else {
			failed++
		}
	}
	for h := range w.drag.clipped {
		if !seen[h] {
			delete(w.drag.clipped, h) // destroyed or recreated by WebView2
		}
	}
	var line string
	switch {
	case len(wins) == 0:
		line = "drag area: no WebView2 drag window found"
	case len(fixed) > 0 || failed > 0:
		line = fmt.Sprintf("drag area mismatch: UI %v, WebView2 drag windows %v (client px); clipped %d to the UI area, %d failed", want, got, len(fixed), failed)
	}
	if line == "" {
		if w.drag.lastLog != "" {
			log.Printf("drag area: WebView2 drag window matches the UI again %v", got)
			w.drag.lastLog = ""
		}
		return
	}
	if line != w.drag.lastLog { // the same state is logged once
		log.Print(line)
		w.drag.lastLog = line
	}
}

// clipDrag limits drag window h (client rect g) to area in, both client px.
// It reports whether the window is clipped that way.
func (w *Window) clipDrag(h uintptr, g, in rect) bool {
	if prev, ok := w.drag.clipped[h]; ok && prev == [2]rect{g, in} {
		return true
	}
	var rgn uintptr
	if in.W() > 0 && in.H() > 0 {
		rgn, _, _ = pCreateRectRgn.Call(uintptr(in.Left-g.Left), uintptr(in.Top-g.Top), uintptr(in.Right-g.Left), uintptr(in.Bottom-g.Top))
	} else {
		rgn, _, _ = pCreateRectRgn.Call(0, 0, 0, 0) // no overlap: nothing of it may catch clicks
	}
	if rgn == 0 {
		return false
	}
	if ok, _, _ := pSetWindowRgn.Call(h, rgn, 1); ok == 0 {
		pDeleteObject.Call(rgn) // on success the system owns the region
		return false
	}
	w.drag.clipped[h] = [2]rect{g, in}
	return true
}

func (w *Window) unclipDrag(h uintptr) {
	if _, ok := w.drag.clipped[h]; ok {
		pSetWindowRgn.Call(h, 0, 1)
		delete(w.drag.clipped, h)
	}
}

func intersect(a, b rect) rect {
	r := rect{max(a.Left, b.Left), max(a.Top, b.Top), min(a.Right, b.Right), min(a.Bottom, b.Bottom)}
	if r.Right < r.Left {
		r.Right = r.Left
	}
	if r.Bottom < r.Top {
		r.Bottom = r.Top
	}
	return r
}

var (
	dragEnumOut []uintptr
	dragEnumCb  = windows.NewCallback(func(h, _ uintptr) uintptr {
		var pid uint32
		tid, _, _ := pGetWindowThreadProcessId.Call(h, ptr(&pid))
		if uint32(tid) != windows.GetCurrentThreadId() || !isVisible(h) {
			return 1
		}
		if ex, _, _ := pGetWindowLongPtrW.Call(h, gwlExStyle); ex&wsExTransparent == 0 {
			return 1
		}
		if className(h) == "Chrome_WidgetWin_0" {
			dragEnumOut = append(dragEnumOut, h)
		}
		return 1
	})
)

// dragWindows lists WebView2's transparent hit-test windows under hwnd: the
// ones on this thread that answer HTCAPTION over app-region:drag areas.
func dragWindows(hwnd uintptr) []uintptr {
	dragEnumOut = dragEnumOut[:0]
	pEnumChildWindows.Call(hwnd, dragEnumCb, 0)
	return append([]uintptr(nil), dragEnumOut...)
}

func className(h uintptr) string {
	var b [128]uint16
	n, _, _ := pGetClassNameW.Call(h, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)))
	return syscall.UTF16ToString(b[:n])
}
