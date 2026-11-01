package update

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	cleanup := len(os.Args) > 1 && os.Args[1] == cleanupFlag
	if HandleArgs() {
		return
	}
	if os.Getenv("INTRA_UPDATE_TEST_PARENT") == "1" {
		dir := os.Getenv("INTRA_UPDATE_TEST_STAGE")
		if cleanup {
			os.WriteFile(os.Getenv("INTRA_UPDATE_TEST_MARKER"), []byte("restarted"), 0o600)
			for i := 0; i < 50; i++ {
				if _, err := os.Stat(dir); os.IsNotExist(err) {
					return
				}
				time.Sleep(100 * time.Millisecond)
			}
			os.Exit(3)
		}
		manifest, err := readManifest(dir)
		if err != nil {
			os.Exit(4)
		}
		manifest.PID = uint32(os.Getpid())
		data, _ := json.Marshal(manifest)
		if os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o600) != nil || (&Prepared{dir: dir}).Start() != nil {
			os.Exit(5)
		}
		return // helper must wait for this process to release app.exe
	}
	os.Exit(m.Run())
}

func stagedFixture(t *testing.T) (string, *manifest, []byte, []byte) {
	t.Helper()
	parent := t.TempDir()
	dir, err := os.MkdirTemp(parent, ".intra-update-")
	if err != nil {
		t.Fatal(err)
	}
	old := executableFixture(t)
	next := append(append([]byte{}, old...), []byte("new version fixture")...)
	target := filepath.Join(parent, "app.exe")
	for path, data := range map[string][]byte{target: old, filepath.Join(dir, "new.exe"): next, filepath.Join(dir, "helper.exe"): old} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := &manifest{Target: target, WorkingDir: parent, SHA256: digest(next)}
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, m, old, next
}

func TestReplacementRollback(t *testing.T) {
	dir, m, old, _ := stagedFixture(t)
	launchErr := errors.New("simulated launch failure")
	if err := replaceAndRestart(dir, m, func(string, []string, string) error { return launchErr }); !errors.Is(err, launchErr) {
		t.Fatalf("expected launch failure: %v", err)
	}
	got, readErr := os.ReadFile(m.Target)
	if !bytes.Equal(got, old) {
		t.Fatalf("failed restart must restore the original executable: read=%v got=%d old=%d", readErr, len(got), len(old))
	}
}

func TestTamperedStagingLeavesOriginal(t *testing.T) {
	dir, m, old, _ := stagedFixture(t)
	os.WriteFile(filepath.Join(dir, "new.exe"), []byte("tampered"), 0o600)
	called := false
	err := replaceAndRestart(dir, m, func(string, []string, string) error { called = true; return nil })
	got, _ := os.ReadFile(m.Target)
	if err == nil || called || !bytes.Equal(got, old) {
		t.Fatal("tampered staging must not replace or execute the target")
	}
}

func TestHelperWaitReplaceRestartAndCleanup(t *testing.T) {
	dir, m, _, next := stagedFixture(t)
	marker := filepath.Join(filepath.Dir(m.Target), "restarted.txt")
	cmd := exec.Command(m.Target)
	cmd.Env = append(os.Environ(), "INTRA_UPDATE_TEST_PARENT=1", "INTRA_UPDATE_TEST_STAGE="+dir, "INTRA_UPDATE_TEST_MARKER="+marker)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("update parent: %v", err)
		}
	case <-time.After(15 * time.Second):
		cmd.Process.Kill()
		t.Fatal("update parent timed out")
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		_, markerErr := os.Stat(marker)
		_, stageErr := os.Stat(dir)
		if markerErr == nil && os.IsNotExist(stageErr) {
			got, _ := os.ReadFile(m.Target)
			if !bytes.Equal(got, next) {
				t.Fatal("restarted image does not match the downloaded version")
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("replacement, restart or cleanup did not complete")
}

func TestCleanupKeepsUnrelatedFiles(t *testing.T) {
	dir, _, _, _ := stagedFixture(t)
	keep := filepath.Join(dir, "user-file.txt")
	os.WriteFile(keep, []byte("keep"), 0o600)
	cleanupFiles(dir)
	if data, err := os.ReadFile(keep); err != nil || string(data) != "keep" {
		t.Fatal("cleanup must preserve unrelated files")
	}
}
