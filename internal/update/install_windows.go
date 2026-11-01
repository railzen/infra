package update

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const helperFlag = "--intra-apply-update"
const cleanupFlag = "--intra-update-cleanup"

var errRollback = errors.New("unable to restore previous executable")

type manifest struct {
	PID        uint32
	Target     string
	WorkingDir string
	Args       []string
	SHA256     string
}

type Prepared struct{ dir string }

func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	return errors.Join(copyErr, out.Sync(), out.Close())
}

// Prepare stages and verifies the download beside the current executable.
// The original stays in place until Start succeeds and the process exits.
func Prepare(ctx context.Context, client *http.Client, release *Release) (*Prepared, error) {
	target, err := os.Executable()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(filepath.Dir(target), ".intra-update-")
	if err != nil {
		return nil, err
	}
	p := &Prepared{dir: dir}
	ok := false
	defer func() {
		if !ok {
			p.Cleanup()
		}
	}()
	if err := release.download(ctx, client, filepath.Join(dir, "new.exe")); err != nil {
		return nil, err
	}
	if err := copyFile(target, filepath.Join(dir, "helper.exe")); err != nil {
		return nil, err
	}
	workingDir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	m := manifest{PID: uint32(os.Getpid()), Target: target, WorkingDir: workingDir, Args: os.Args[1:], SHA256: release.SHA256}
	data, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o600); err != nil {
		return nil, err
	}
	ok = true
	return p, nil
}

func (p *Prepared) Cleanup() { cleanupFiles(p.dir) }

func (p *Prepared) Start() error {
	cmd := exec.Command(filepath.Join(p.dir, "helper.exe"), helperFlag)
	cmd.Dir = p.dir
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(p.dir, "ready")); err == nil {
				return nil
			}
		case <-done:
			return errors.New("update helper exited before readiness")
		case <-timeout.C:
			cmd.Process.Kill()
			<-done
			return errors.New("update helper timed out")
		}
	}
}

func readManifest(dir string) (*manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m manifest
	if json.Unmarshal(data, &m) != nil || !filepath.IsAbs(dir) || !filepath.IsAbs(m.Target) ||
		!strings.HasPrefix(filepath.Base(dir), ".intra-update-") ||
		!strings.EqualFold(filepath.Dir(dir), filepath.Dir(m.Target)) ||
		!strings.EqualFold(filepath.Clean(dir), dir) {
		return nil, errors.New("invalid update manifest")
	}
	return &m, nil
}

func moveFile(source, target string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(target); os.IsNotExist(err) {
		return windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH)
	}
	result, _, callErr := windows.NewLazySystemDLL("kernel32.dll").NewProc("ReplaceFileW").Call(
		uintptr(unsafe.Pointer(to)), uintptr(unsafe.Pointer(from)), 0, 0, 0, 0)
	if result == 0 {
		return callErr
	}
	return nil
}

func replaceAndRestart(dir string, m *manifest, launch func(string, []string, string) error) error {
	newPath := filepath.Join(dir, "new.exe")
	if err := validateExecutable(newPath, m.SHA256); err != nil {
		return err
	}
	backup := filepath.Join(dir, "previous.exe")
	if err := copyFile(m.Target, backup); err != nil {
		return err
	}
	if err := moveFile(newPath, m.Target); err != nil {
		// ReplaceFile can report a failed rename after removing the target name.
		if _, statErr := os.Stat(m.Target); os.IsNotExist(statErr) {
			if restoreErr := moveFile(backup, m.Target); restoreErr != nil {
				return errors.Join(errRollback, err, restoreErr)
			}
		}
		return err
	}
	args := append([]string{cleanupFlag, dir}, m.Args...)
	if err := launch(m.Target, args, m.WorkingDir); err != nil {
		if rollbackErr := moveFile(backup, m.Target); rollbackErr != nil {
			return errors.Join(errRollback, err, rollbackErr)
		}
		return err
	}
	return nil
}

func launch(path string, args []string, workingDir string) error {
	cmd := exec.Command(path, args...)
	cmd.Dir = workingDir
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	cmd.Process.Release()
	return nil
}

func applyHelper(dir string) {
	m, err := readManifest(dir)
	if err != nil {
		return
	}
	parent, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, m.PID)
	if err != nil {
		return
	}
	defer windows.CloseHandle(parent)
	buf := make([]uint16, 32768)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(parent, 0, &buf[0], &n) != nil ||
		!strings.EqualFold(windows.UTF16ToString(buf[:n]), m.Target) {
		return
	}
	if err := validateExecutable(filepath.Join(dir, "new.exe"), m.SHA256); err != nil {
		return
	}
	if os.WriteFile(filepath.Join(dir, "ready"), nil, 0o600) != nil {
		return
	}
	if result, err := windows.WaitForSingleObject(parent, 60000); err != nil || result != windows.WAIT_OBJECT_0 {
		return
	}
	if err := replaceAndRestart(dir, m, launch); err != nil {
		if errors.Is(err, errRollback) {
			return // keep the backup for recovery if the filesystem refused rollback
		}
		// A failed replacement leaves the old file intact; failed launch restores it.
		launch(m.Target, append([]string{cleanupFlag, dir}, m.Args...), m.WorkingDir)
	}
}

// Delete only updater-owned files. An unrelated file prevents directory removal.
func cleanupFiles(dir string) {
	for _, name := range []string{"new.exe", "helper.exe", "previous.exe", "manifest.json", "ready"} {
		os.Remove(filepath.Join(dir, name))
	}
	os.Remove(dir)
}

// HandleArgs runs the copied helper before the app initializes, or schedules
// removal of updater files after the relaunched app has started.
func HandleArgs() bool {
	if len(os.Args) < 2 {
		return false
	}
	if os.Args[1] == helperFlag {
		if len(os.Args) == 2 {
			if self, err := os.Executable(); err == nil && filepath.Base(self) == "helper.exe" {
				applyHelper(filepath.Dir(self))
			}
		}
		return true
	}
	if os.Args[1] == cleanupFlag && len(os.Args) >= 3 {
		dir := os.Args[2]
		os.Args = append(os.Args[:1], os.Args[3:]...)
		self, err := os.Executable()
		m, manifestErr := readManifest(dir)
		if err == nil && manifestErr == nil && strings.EqualFold(self, m.Target) {
			go func() {
				for i := 0; i < 100; i++ {
					// Keep the manifest until the running helper can also be removed.
					if err := os.Remove(filepath.Join(dir, "helper.exe")); err == nil || os.IsNotExist(err) {
						cleanupFiles(dir)
						return
					}
					time.Sleep(100 * time.Millisecond)
				}
			}()
		}
	}
	return false
}
