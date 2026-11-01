// Package config stores Intra's settings as JSON in
// %APPDATA%\IntraWebApps\config.json.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AppDirName is the per-user folder name under %APPDATA% and %LOCALAPPDATA%.
const AppDirName = "IntraWebApps"

type DNSRecord struct {
	Host  string `json:"host"`
	Type  string `json:"type"` // "A" or "CNAME"
	Value string `json:"value"`
}

type Bookmark struct {
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	URL     string      `json:"url"`
	Records []DNSRecord `json:"records"`
	// Window is where this link's window was last closed (machine specific, not exported).
	Window *WindowState `json:"window,omitempty"`
}

type CA struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	PEM         string `json:"pem"`
	Fingerprint string `json:"fingerprint"`
	NotAfter    string `json:"notAfter"`
	AddedAt     string `json:"addedAt"`
}

type WindowState struct {
	X, Y, W, H int
	Maximized  bool
}

type Config struct {
	Bookmarks []Bookmark `json:"bookmarks"`
	// DefaultPage: "home" (favorites overview), "last" (last opened bookmark) or a bookmark ID.
	DefaultPage          string       `json:"defaultPage"`
	RememberWindow       bool         `json:"rememberWindow"`
	MinimizeToTray       bool         `json:"minimizeToTray"` // a minimized window hides in the tray
	OpenLinksInNewWindow bool         `json:"openLinksInNewWindow"`
	Window               *WindowState `json:"window,omitempty"`
	LastBookmark         string       `json:"lastBookmark,omitempty"`
	CAs                  []CA         `json:"cas"`
}

func Default() *Config {
	return &Config{DefaultPage: "home", RememberWindow: true, OpenLinksInNewWindow: true}
}

func (c *Config) Clone() *Config {
	b, _ := json.Marshal(c)
	out := &Config{}
	json.Unmarshal(b, out)
	return out
}

func (c *Config) Bookmark(id string) *Bookmark {
	for i := range c.Bookmarks {
		if c.Bookmarks[i].ID == id {
			return &c.Bookmarks[i]
		}
	}
	return nil
}

// DefaultPath is %APPDATA%\IntraWebApps\config.json.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, AppDirName, "config.json"), nil
}

// Store reads and writes one JSON configuration file.
type Store struct {
	mu   sync.Mutex
	path string
}

func NewStore(path string) *Store { return &Store{path: path} }

func (s *Store) Path() string { return s.path }

// Load reads the configuration. A missing file yields an error matching
// os.ErrNotExist; an unreadable one is moved aside (see Quarantine).
func (s *Store) Load() (*Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		return Default(), err
	}
	c := Default()
	if err := json.Unmarshal(trimBOM(data), c); err != nil {
		return Default(), &CorruptError{Path: s.path, Err: err}
	}
	return c, nil
}

// CorruptError reports a configuration file that is not valid JSON.
type CorruptError struct {
	Path string
	Err  error
}

func (e *CorruptError) Error() string {
	return fmt.Sprintf("配置文件 %s 已损坏: %v", e.Path, e.Err)
}
func (e *CorruptError) Unwrap() error { return e.Err }

// Quarantine renames an unreadable configuration file so a fresh one can be
// written without destroying it, and returns the backup path.
func (s *Store) Quarantine() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	backup := s.path + "." + time.Now().Format("20060102-150405") + ".bad"
	return backup, os.Rename(s.path, backup)
}

// Save writes the configuration atomically: a temporary file in the same
// folder replaces config.json, so a crash never leaves a half-written file.
func (s *Store) Save(c *Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("无法创建 %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "config-*.tmp")
	if err != nil {
		return fmt.Errorf("无法写入 %s: %w", dir, err)
	}
	_, werr := tmp.Write(raw)
	serr := tmp.Sync()
	cerr := tmp.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("无法写入配置: %w", err)
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("无法写入配置: %w", err)
	}
	return nil
}
