package update

import (
	"context"
	"crypto/sha256"
	"debug/pe"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

const LatestURL = "https://api.github.com/repos/railzen/Infra/releases/latest"
const maxAssetSize = 128 << 20

var ErrUpToDate = errors.New("already running the latest release")

type Release struct {
	Version string
	URL     string
	Size    int64
	SHA256  string
}

func parseVersion(s string) ([3]uint64, bool) {
	var out [3]uint64
	parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		if p == "" || (len(p) > 1 && p[0] == '0') {
			return out, false
		}
		for _, ch := range p {
			if ch < '0' || ch > '9' {
				return out, false
			}
		}
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func newer(candidate, current string) bool {
	a, ok := parseVersion(candidate)
	if !ok {
		return false
	}
	b, ok := parseVersion(current)
	if !ok {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

// AllowRedirect keeps release downloads on GitHub's HTTPS asset infrastructure.
func AllowRedirect(req *http.Request, via []*http.Request) error {
	host := strings.ToLower(req.URL.Hostname())
	if len(via) >= 8 || req.URL.Scheme != "https" || req.URL.User != nil ||
		(req.URL.Port() != "" && req.URL.Port() != "443") ||
		(host != "github.com" && host != "api.github.com" && !strings.HasSuffix(host, ".githubusercontent.com")) {
		return errors.New("invalid update redirect")
	}
	return nil
}

// Check returns ErrUpToDate only when a valid published version is not newer.
// Unusable or missing releases return nil without claiming the app is current.
func Check(ctx context.Context, client *http.Client, current string) (*Release, error) {
	return check(ctx, client, LatestURL, current)
}

func check(ctx context.Context, client *http.Client, endpoint, current string) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Intra/"+current)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release status %d", resp.StatusCode)
	}
	var release struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Size   int64  `json:"size"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		return nil, errors.New("invalid release response")
	}
	if err := json.Unmarshal(data, &release); err != nil {
		return nil, err
	}
	_, validRelease := parseVersion(release.Tag)
	_, validCurrent := parseVersion(current)
	if release.Draft || release.Prerelease || !validRelease || !validCurrent {
		return nil, nil
	}
	if !newer(release.Tag, current) {
		return nil, ErrUpToDate
	}
	for _, asset := range release.Assets {
		if asset.Name != "Intra.exe" || asset.Size <= 0 || asset.Size > maxAssetSize || !strings.HasPrefix(asset.Digest, "sha256:") {
			continue
		}
		digest := strings.TrimPrefix(asset.Digest, "sha256:")
		if raw, err := hex.DecodeString(digest); err != nil || len(raw) != sha256.Size {
			continue
		}
		u, err := url.Parse(asset.URL)
		expectedPath := "/railzen/infra/releases/download/" + release.Tag + "/Intra.exe"
		if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil ||
			!strings.EqualFold(u.Path, expectedPath) || u.RawQuery != "" || u.Fragment != "" {
			continue
		}
		return &Release{Version: strings.TrimPrefix(release.Tag, "v"), URL: asset.URL, Size: asset.Size, SHA256: strings.ToLower(digest)}, nil
	}
	return nil, nil
}

func (r *Release) download(ctx context.Context, client *http.Client, path string) error {
	if r.Size <= 0 || r.Size > maxAssetSize {
		return errors.New("invalid update size")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Intra updater")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download status %d", resp.StatusCode)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, r.Size+1))
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		return err
	}
	if n != r.Size || hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
		return errors.New("update size or checksum mismatch")
	}
	return validateExecutable(path, r.SHA256)
}

func validateExecutable(path, digest string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxAssetSize+1))
	f.Close()
	if err != nil || n > maxAssetSize || hex.EncodeToString(h.Sum(nil)) != digest {
		return errors.New("update checksum mismatch")
	}
	image, err := pe.Open(path)
	if err != nil {
		return err
	}
	defer image.Close()
	if image.Machine != pe.IMAGE_FILE_MACHINE_AMD64 || image.Characteristics&pe.IMAGE_FILE_EXECUTABLE_IMAGE == 0 {
		return errors.New("update is not a Windows x64 executable")
	}
	return nil
}
