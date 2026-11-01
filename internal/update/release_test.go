package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"intra/internal/netx"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func responseClient(status int, data []byte) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(data)), Header: http.Header{}, Request: req}, nil
	})}
}

func executableFixture(t *testing.T) []byte {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestReleaseSelection(t *testing.T) {
	base := map[string]any{"tag_name": "v1.10.0", "draft": false, "prerelease": false,
		"assets": []map[string]any{{"name": "Intra.exe", "size": 100, "digest": "sha256:" + strings.Repeat("a", 64),
			"browser_download_url": "https://github.com/railzen/infra/releases/download/v1.10.0/Intra.exe"}}}
	encode := func() []byte { data, _ := json.Marshal(base); return data }
	for _, current := range []string{"1.9.9", "v1.9.9", "1.10.0", "2.0.0", "dev", "1.9.9-rc1"} {
		t.Run(current, func(t *testing.T) {
			r, err := Check(context.Background(), responseClient(200, encode()), current)
			want := current == "1.9.9" || current == "v1.9.9"
			latest := current == "1.10.0" || current == "2.0.0"
			if (err != nil && !errors.Is(err, ErrUpToDate)) || errors.Is(err, ErrUpToDate) != latest || (r != nil) != want {
				t.Fatalf("release=%+v error=%v want update=%t", r, err, want)
			}
		})
	}
	for _, field := range []string{"draft", "prerelease"} {
		base[field] = true
		if r, err := Check(context.Background(), responseClient(200, encode()), "1.0.0"); err != nil || r != nil {
			t.Fatalf("%s release must be ignored", field)
		}
		base[field] = false
	}
	asset := base["assets"].([]map[string]any)[0]
	for _, bad := range []struct {
		field string
		value any
	}{
		{"name", "other.exe"}, {"digest", ""}, {"digest", "sha256:bad"}, {"size", maxAssetSize + 1},
		{"browser_download_url", "http://github.com/railzen/infra/releases/download/v1.10.0/Intra.exe"},
		{"browser_download_url", "https://github.com/other/project/releases/download/v1.10.0/Intra.exe"},
		{"browser_download_url", "https://github.com/railzen/infra/releases/download/v1.9.0/Intra.exe"},
	} {
		previous := asset[bad.field]
		asset[bad.field] = bad.value
		if r, err := Check(context.Background(), responseClient(200, encode()), "1.0.0"); err != nil || r != nil {
			t.Fatalf("invalid asset accepted: %s=%v", bad.field, bad.value)
		}
		asset[bad.field] = previous
	}
	for _, status := range []int{404, 403, 500} {
		if r, err := Check(context.Background(), responseClient(status, nil), "1.0.0"); r != nil || err == nil {
			t.Fatalf("unexpected response for status %d", status)
		}
	}
	if r, err := Check(context.Background(), responseClient(200, []byte("invalid JSON")), "1.0.0"); r != nil || err == nil {
		t.Fatal("invalid JSON must not produce an update")
	}
}

func TestDownloadIntegrity(t *testing.T) {
	valid := executableFixture(t)
	for _, tc := range []struct {
		name string
		body []byte
		size int64
		hash string
		ok   bool
	}{
		{"valid", valid, int64(len(valid)), digest(valid), true},
		{"truncated", valid[:100], int64(len(valid)), digest(valid), false},
		{"too large", append(append([]byte{}, valid...), 1), int64(len(valid)), digest(valid), false},
		{"wrong hash", valid, int64(len(valid)), strings.Repeat("a", 64), false},
		{"not PE", []byte("not an executable"), 17, digest([]byte("not an executable")), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Release{URL: "https://github.com/railzen/infra/releases/download/v1.0.1/Intra.exe", Size: tc.size, SHA256: tc.hash}
			err := r.download(context.Background(), responseClient(200, tc.body), filepath.Join(t.TempDir(), "new.exe"))
			if (err == nil) != tc.ok {
				t.Fatalf("download error=%v want success=%t", err, tc.ok)
			}
		})
	}
}

func TestRedirectPolicy(t *testing.T) {
	for _, tc := range []struct {
		url string
		ok  bool
	}{
		{"https://release-assets.githubusercontent.com/github-production-release-asset/test", true},
		{"https://github.com/railzen/infra/releases/download/v1.0.1/Intra.exe", true},
		{"http://release-assets.githubusercontent.com/test", false},
		{"https://githubusercontent.com.evil.invalid/test", false},
		{"https://evil.invalid/test", false},
		{"https://user:pass@github.com/test", false},
		{"https://github.com:444/test", false},
	} {
		u, _ := url.Parse(tc.url)
		if err := AllowRedirect(&http.Request{URL: u}, nil); (err == nil) != tc.ok {
			t.Fatalf("redirect %s error=%v", tc.url, err)
		}
	}
}

func TestLiveReleaseCheck(t *testing.T) {
	if os.Getenv("INTRA_UPDATE_NET_TEST") == "" {
		t.Skip("set INTRA_UPDATE_NET_TEST=1 for the GitHub metadata check")
	}
	client := netx.TrustedHTTPClient(15 * time.Second)
	client.CheckRedirect = AllowRedirect
	defer client.CloseIdleConnections()
	r, err := Check(context.Background(), client, "0.0.0")
	if err != nil || r == nil {
		t.Fatalf("GitHub release: %+v %v", r, err)
	}
	if same, err := Check(context.Background(), client, r.Version); !errors.Is(err, ErrUpToDate) || same != nil {
		t.Fatalf("same version must not update: %+v %v", same, err)
	}
	t.Logf("validated GitHub release v%s, %d bytes, sha256:%s", r.Version, r.Size, r.SHA256)
}
