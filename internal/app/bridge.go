package app

import (
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"intra/internal/config"
	"intra/internal/netx"
)

type inMsg struct {
	T                    string `json:"t"`
	X, Y, W, H           float64
	X0, Y0               float64
	Edge                 string           `json:"edge"`
	Where                string           `json:"where"`
	Kind                 string           `json:"kind"`
	Page                 string           `json:"page"`
	Arg                  string           `json:"arg"`
	Back                 string           `json:"back"`
	ID                   string           `json:"id"`
	Action               string           `json:"action"`
	Index                int              `json:"index"`
	Bookmark             *config.Bookmark `json:"bookmark"`
	Open                 bool             `json:"open"`
	DefaultPage          string           `json:"defaultPage"`
	RememberWindow       bool             `json:"rememberWindow"`
	MinimizeToTray       bool             `json:"minimizeToTray"`
	OpenLinksInNewWindow bool             `json:"openLinksInNewWindow"`
	Name                 string           `json:"name"`
	Data                 string           `json:"data"`
	URL                  string           `json:"url"`
}

// onMessage handles a message from this window's UI view or dropdown.
func (w *Window) onMessage(fromMenu bool, raw string) {
	if w.closing {
		return
	}
	var m inMsg
	if json.Unmarshal([]byte(raw), &m) != nil {
		return
	}
	a := w.a
	switch m.T {
	case "ready":
		if fromMenu {
			w.menu.ready = true
		} else {
			w.uiReady = true
		}
		w.push()

	// --- window chrome ---
	case "drag", "resize":
		// SetWindowPos / ShowWindow pump messages; leave the webview callback first.
		kind, edge := m.T, m.Edge
		x0, y0, x, y := m.X0, m.Y0, m.X, m.Y
		a.post(func() {
			if !w.closing {
				w.trackPointer(kind, edge, x0, y0, x, y)
			}
		})
	case "gestureEnd":
		a.post(func() { w.grab.key = "" })
	case "dragArea":
		w.dragArea(m.X, m.Y, m.W, m.H)
	case "min", "max":
		log.Printf("caption: %s", m.T)
		// ShowWindow pumps window messages; leave the WebView2 callback first.
		a.post(func() {
			if w.closing {
				return
			}
			if m.T == "min" {
				pShowWindow.Call(w.hwnd, swMinimize)
			} else {
				w.toggleMaximize()
			}
		})
	case "close":
		log.Printf("caption: close")
		pPostMessageW.Call(w.hwnd, wmClose, 0, 0)
	case "sysmenu":
		// TrackPopupMenu runs a modal loop; inside this webview callback it
		// would hold back every other WebView2 event until the menu closes
		a.post(func() {
			if !w.closing {
				w.showSystemMenu()
			}
		})

	// --- dropdown ---
	case "menu":
		w.menu.toggle(m.Kind, m.X, m.Y)
	case "menuSize":
		w.menu.sized(m.Kind, m.H)
	case "menuClose":
		w.menu.hide()
	case "panel":
		w.menu.openPanel(m.Kind, m.Arg)

	// --- navigation ---
	case "show":
		w.menu.hide()
		w.showPage(m.Page, m.Arg)
	case "open":
		w.menu.hide()
		// opening may move and resize this window; do it after the webview
		// callback that delivered the message has returned, not inside it
		id, where := m.ID, m.Where
		a.post(func() {
			if w.closing {
				return
			}
			switch where {
			case "current":
				a.openHere(w, id)
			case "new":
				a.openNew(w, id)
			default:
				a.open(w, id)
			}
		})
	case "deleteLink":
		// from the dropdown: it stays open, so the main window keeps its page
		if a.removeBookmark(m.ID) {
			w.toast("已删除", "ok")
		}
		a.pushAll()
	case "navigate":
		// typed into the address field of the links dropdown
		if w.cur != nil {
			if u, ok := addressURL(m.URL); ok {
				w.menu.hide()
				w.showPage("", "")
				w.site.Navigate(u)
			} else {
				w.toast("只支持 http 和 https 地址", "err") // the dropdown stays open to show it
			}
		} else {
			w.menu.hide()
		}
	case "nav":
		if w.cur == nil || w.siteWV == 0 {
			return
		}
		switch m.Action {
		case "back":
			w.siteWV.GoBack()
		case "forward":
			w.siteWV.GoForward()
		case "reload":
			w.siteWV.Reload()
		case "stop":
			w.siteWV.Stop()
		}

	// --- data ---
	case "saveBookmark":
		a.saveBookmark(w, m.Bookmark, m.Open, m.Back)
	case "deleteBookmark":
		a.deleteBookmark(w, m.ID, m.Back)
	case "moveBookmark":
		a.moveBookmark(m.ID, m.Index)
	case "settings":
		dp := m.DefaultPage
		if dp != "home" && dp != "last" && a.cfg.Bookmark(dp) == nil {
			dp = "home"
		}
		a.cfg.DefaultPage = dp
		a.cfg.RememberWindow = m.RememberWindow
		a.cfg.MinimizeToTray = m.MinimizeToTray
		a.cfg.OpenLinksInNewWindow = m.OpenLinksInNewWindow
		if !m.RememberWindow {
			a.cfg.Window = nil
			for i := range a.cfg.Bookmarks {
				a.cfg.Bookmarks[i].Window = nil
			}
		}
		a.save()
		a.pushAll()
	case "addCA":
		a.addCA(w, m.Name, m.Data)
	case "deleteCA":
		a.deleteCA(w, m.ID)
	case "exportConfig":
		// the file dialog is modal; run it outside the webview callback
		a.post(func() { a.exportConfig(w) })
	case "importConfig":
		a.post(func() { a.importConfig(w) })
	case "checkUpdate":
		a.checkForUpdates(w)
	case "installUpdate":
		a.installUpdate()
	}
}

// dialogOwner keeps the dropdown open while a dialog started from it is shown.
func (w *Window) dialogOwner() uintptr {
	if w.menu != nil && w.menu.open {
		return w.menu.hwnd
	}
	return w.hwnd
}

func (a *App) exportConfig(w *Window) {
	if w.closing {
		return
	}
	data, err := a.cfg.Export()
	if err != nil {
		w.toast("导出失败："+err.Error(), "err")
		return
	}
	path := fileDialog(w.dialogOwner(), true, "导出 Intra 配置", "Intra-config-"+time.Now().Format("20060102")+".json")
	if path == "" || w.closing {
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		w.toast("导出失败："+err.Error(), "err")
		return
	}
	w.toast("已导出到 "+filepath.Base(path), "ok")
}

// importConfig merges an exported file into the current configuration:
// links with a known ID are updated, others added; CAs are de-duplicated by
// fingerprint; settings are taken from the file. Invalid entries are skipped.
func (a *App) importConfig(w *Window) {
	if w.closing {
		return
	}
	path := fileDialog(w.dialogOwner(), false, "导入 Intra 配置", "")
	if path == "" || w.closing {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		w.toast("无法读取文件："+err.Error(), "err")
		return
	}
	e, err := config.ParseExport(data)
	if err != nil {
		w.toast(err.Error(), "err")
		return
	}
	added, updated, cas, skipped := a.mergeImport(e)
	a.syncLinkShortcuts()
	for _, x := range a.windows {
		if x.cur != nil {
			if b := a.cfg.Bookmark(x.cur.ID); b != nil {
				x.cur.Name = b.Name
				x.updateTitle()
			}
		}
	}
	a.pushAll()
	msg := fmt.Sprintf("已导入：新增 %d 个链接，更新 %d 个，CA %d 个", added, updated, cas)
	if skipped > 0 {
		msg += fmt.Sprintf("，跳过 %d 项无效内容", skipped)
	}
	w.toast(msg, "ok")
}

// mergeImport applies an exported configuration and saves the result.
func (a *App) mergeImport(e *config.ExportFormat) (added, updated, cas, skipped int) {
	for _, b := range e.Bookmarks {
		b.Records = append([]config.DNSRecord{}, b.Records...)
		b.Window = nil // placement is machine specific
		if err := validateBookmark(&b); err != nil {
			skipped++
			continue
		}
		if existing := a.cfg.Bookmark(b.ID); b.ID != "" && existing != nil {
			b.Window = existing.Window
			*existing = b
			updated++
			continue
		}
		if b.ID == "" {
			b.ID = newID()
		}
		a.cfg.Bookmarks = append(a.cfg.Bookmarks, b)
		added++
	}
	for _, ca := range e.CAs {
		certs, err := netx.ParseCertificates([]byte(ca.PEM))
		if err != nil {
			skipped++
			continue
		}
		for _, c := range certs {
			if a.addCert(c) {
				cas++
			}
		}
	}
	a.cfg.RememberWindow = e.Settings.RememberWindow
	a.cfg.MinimizeToTray = e.Settings.MinimizeToTray
	a.cfg.OpenLinksInNewWindow = e.Settings.OpenLinksInNewWindow
	if dp := e.Settings.DefaultPage; dp == "home" || dp == "last" || a.cfg.Bookmark(dp) != nil {
		a.cfg.DefaultPage = dp
	}
	a.applyTrust()
	a.applyRecords()
	a.save()
	return
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// validateBookmark mirrors the checks in the UI; the UI is not trusted to have run them.
func validateBookmark(b *config.Bookmark) error {
	b.Name = strings.TrimSpace(b.Name)
	b.URL = strings.TrimSpace(b.URL)
	if b.Name == "" {
		return fmt.Errorf("请输入名称")
	}
	u, err := url.Parse(normalizeURL(b.URL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("网址格式不正确")
	}
	recs := b.Records[:0]
	for _, r := range b.Records {
		r.Host = strings.ToLower(strings.TrimSpace(r.Host))
		r.Value = strings.TrimSpace(r.Value)
		r.Type = strings.ToUpper(strings.TrimSpace(r.Type))
		if r.Host == "" && r.Value == "" {
			continue
		}
		switch r.Type {
		case "A":
			if net.ParseIP(r.Value) == nil {
				return fmt.Errorf("%s 的 A 记录值不是有效的 IP 地址", r.Host)
			}
		case "CNAME":
			if r.Value == "" || strings.ContainsAny(r.Value, " /:*") {
				return fmt.Errorf("%s 的 CNAME 值不是有效的主机名", r.Host)
			}
		default:
			return fmt.Errorf("不支持的记录类型 %q", r.Type)
		}
		if r.Host == "" || strings.ContainsAny(r.Host, " /:") {
			return fmt.Errorf("主机名 %q 格式不正确", r.Host)
		}
		recs = append(recs, r)
	}
	b.Records = recs
	return nil
}

// saveBookmark is the save button of the floating link editor. Afterwards the
// editor returns to the panel it came from (back), or closes.
func (a *App) saveBookmark(w *Window, b *config.Bookmark, open bool, back string) {
	if b == nil {
		return
	}
	if err := validateBookmark(b); err != nil {
		w.toast(err.Error(), "err")
		return
	}
	if existing := a.cfg.Bookmark(b.ID); b.ID != "" && existing != nil {
		b.Window = existing.Window // placement is not part of the edit form
		*existing = *b
	} else {
		b.ID = newID()
		b.Window = nil
		a.cfg.Bookmarks = append(a.cfg.Bookmarks, *b)
	}
	for _, x := range a.windows {
		if x.cur != nil && x.cur.ID == b.ID {
			x.cur.Name = b.Name
			x.updateTitle()
		}
	}
	a.applyRecords()
	a.save()
	if open {
		// a window already showing a site gets a new one; opening may move
		// and resize windows, so leave the webview callback first
		w.menu.hide()
		id := b.ID
		a.post(func() {
			if !w.closing {
				a.open(w, id)
			}
		})
	} else {
		w.leaveEditor(back)
	}
	w.toast("已保存", "ok")
	a.pushAll()
	a.syncLinkShortcuts()
}

// deleteBookmark is the delete button of the link editor.
func (a *App) deleteBookmark(w *Window, id, back string) {
	ok := a.removeBookmark(id)
	w.leaveEditor(back)
	if ok {
		w.toast("已删除", "ok")
	}
	a.pushAll()
}

// leaveEditor returns from the link editor to the link manager it was opened
// from, or closes it.
func (w *Window) leaveEditor(back string) {
	if back == "bookmarks" {
		w.menu.openPanel("bookmarks", "")
	} else {
		w.menu.hide()
	}
}

// removeBookmark deletes a link and sends windows showing it back to the home page.
func (a *App) removeBookmark(id string) bool {
	if a.cfg.Bookmark(id) == nil {
		return false
	}
	out := a.cfg.Bookmarks[:0]
	for _, b := range a.cfg.Bookmarks {
		if b.ID != id {
			out = append(out, b)
		}
	}
	a.cfg.Bookmarks = out
	if a.cfg.DefaultPage == id {
		a.cfg.DefaultPage = "home"
	}
	if a.cfg.LastBookmark == id {
		a.cfg.LastBookmark = ""
	}
	for _, x := range a.windows {
		if x.cur != nil && x.cur.ID == id {
			x.closeSite()
			x.showPage("home", "")
		}
	}
	a.applyRecords()
	a.save()
	a.syncLinkShortcuts()
	return true
}

// moveBookmark puts a link at position to, where it was dropped in the link manager.
func (a *App) moveBookmark(id string, to int) {
	bs := a.cfg.Bookmarks
	from := slices.IndexFunc(bs, func(b config.Bookmark) bool { return b.ID == id })
	if from < 0 || to < 0 || to >= len(bs) || to == from {
		return
	}
	b := bs[from]
	if from < to {
		copy(bs[from:to], bs[from+1:to+1])
	} else {
		copy(bs[to+1:from+1], bs[to:from])
	}
	bs[to] = b
	a.save()
	a.pushAll()
	a.syncLinkShortcuts()
}

func (a *App) addCA(w *Window, name, data string) {
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		w.toast("无法读取文件", "err")
		return
	}
	certs, err := netx.ParseCertificates(raw)
	if err != nil {
		w.toast(name+"："+err.Error(), "err")
		return
	}
	added, dup := 0, 0
	for _, c := range certs {
		if a.addCert(c) {
			added++
		} else {
			dup++
		}
	}
	if added == 0 {
		w.toast("证书已存在", "err")
		return
	}
	a.applyTrust()
	a.save()
	a.pushAll()
	msg := fmt.Sprintf("已添加 %d 个 CA 证书", added)
	if dup > 0 {
		msg += fmt.Sprintf("（%d 个已存在）", dup)
	}
	w.toast(msg, "ok")
}

// addCert adds a trusted certificate unless one with the same fingerprint exists.
func (a *App) addCert(c *x509.Certificate) bool {
	fp := netx.Fingerprint(c)
	for _, ca := range a.cfg.CAs {
		if ca.Fingerprint == fp {
			return false
		}
	}
	a.cfg.CAs = append(a.cfg.CAs, config.CA{
		ID:          strings.ReplaceAll(fp, ":", "")[:16],
		Name:        netx.DisplayName(c),
		PEM:         netx.CertToPEM(c),
		Fingerprint: fp,
		NotAfter:    c.NotAfter.Local().Format("2006-01-02"),
		AddedAt:     time.Now().Format("2006-01-02"),
	})
	return true
}

func (a *App) deleteCA(w *Window, id string) {
	out := a.cfg.CAs[:0]
	for _, c := range a.cfg.CAs {
		if c.ID != id {
			out = append(out, c)
		}
	}
	a.cfg.CAs = out
	a.applyTrust()
	a.save()
	a.pushAll()
	w.toast("已删除证书", "ok")
}

// addressURL turns what was typed into the address field into an http(s)
// URL; a bare host such as "oa.corp/path" gets https://.
func addressURL(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	if i := strings.Index(s, "://"); i >= 0 {
		if sch := strings.ToLower(s[:i]); sch != "http" && sch != "https" {
			return "", false
		}
	}
	u, err := url.Parse(normalizeURL(s))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	return u.String(), true
}
