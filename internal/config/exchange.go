package config

import (
	"encoding/json"
	"errors"
	"time"
)

// ExportFormat is the JSON document written by "导出配置" and read by
// "导入配置". Window placement is machine specific and is left out.
type ExportFormat struct {
	App        string `json:"app"`
	Version    int    `json:"version"`
	ExportedAt string `json:"exportedAt"`
	Settings   struct {
		DefaultPage          string `json:"defaultPage"`
		RememberWindow       bool   `json:"rememberWindow"`
		MinimizeToTray       bool   `json:"minimizeToTray"`
		OpenLinksInNewWindow bool   `json:"openLinksInNewWindow"`
	} `json:"settings"`
	Bookmarks []Bookmark   `json:"bookmarks"`
	CAs       []ExportedCA `json:"cas"`
}

type ExportedCA struct {
	Name string `json:"name"`
	PEM  string `json:"pem"`
}

const exportVersion = 1

// Export renders the shareable part of the configuration as indented JSON.
func (c *Config) Export() ([]byte, error) {
	e := ExportFormat{App: "Intra", Version: exportVersion, ExportedAt: time.Now().Format(time.RFC3339)}
	e.Settings.DefaultPage = c.DefaultPage
	e.Settings.RememberWindow = c.RememberWindow
	e.Settings.MinimizeToTray = c.MinimizeToTray
	e.Settings.OpenLinksInNewWindow = c.OpenLinksInNewWindow
	e.Bookmarks = append([]Bookmark{}, c.Bookmarks...)
	for i := range e.Bookmarks {
		e.Bookmarks[i].Window = nil
		if e.Bookmarks[i].Records == nil {
			e.Bookmarks[i].Records = []DNSRecord{}
		}
	}
	e.CAs = []ExportedCA{}
	for _, ca := range c.CAs {
		e.CAs = append(e.CAs, ExportedCA{Name: ca.Name, PEM: ca.PEM})
	}
	return json.MarshalIndent(e, "", "  ")
}

// ParseExport reads a file produced by Export.
func ParseExport(data []byte) (*ExportFormat, error) {
	data = trimBOM(data)
	var e ExportFormat
	e.Settings.OpenLinksInNewWindow = true
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, errors.New("不是有效的 JSON 文件")
	}
	if e.App != "Intra" {
		return nil, errors.New("不是 Intra 导出的配置文件")
	}
	if e.Version > exportVersion {
		return nil, errors.New("配置文件来自更新版本的 Intra")
	}
	return &e, nil
}

func trimBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}
