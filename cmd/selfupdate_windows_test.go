//go:build windows

package cmd

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A running executable can't be overwritten on Windows, the update has to work
// even though the old Wings process is still running.
func TestReplaceExecutableWhileRunning(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "wings.exe")

	ping := filepath.Join(os.Getenv("SystemRoot"), "System32", "PING.EXE")
	src, err := os.Open(ping)
	if err != nil {
		t.Skipf("PING.EXE not available: %v", err)
	}
	dst, err := os.Create(current)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	src.Close()
	dst.Close()

	proc := exec.Command(current, "-t", "127.0.0.1")
	if err := proc.Start(); err != nil {
		t.Fatalf("failed to start the stand-in executable: %v", err)
	}
	t.Cleanup(func() { _ = proc.Process.Kill(); _, _ = proc.Process.Wait() })

	// Sanity check: overwriting a running executable really is refused.
	if err := os.WriteFile(current, []byte("x"), 0o755); err == nil {
		t.Fatal("expected overwriting a running executable to fail")
	}

	newBin := filepath.Join(t.TempDir(), "wings_windows_amd64.exe")
	if err := os.WriteFile(newBin, []byte("new version"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceExecutable(newBin, current); err != nil {
		t.Fatalf("replaceExecutable: %v", err)
	}

	got, err := os.ReadFile(current)
	if err != nil || string(got) != "new version" {
		t.Fatalf("executable content = %q, %v", got, err)
	}
	if _, err := os.Stat(current + ".new"); !os.IsNotExist(err) {
		t.Error("the temporary .new file was left behind")
	}
	olds, _ := filepath.Glob(current + ".*.old")
	if len(olds) != 1 {
		t.Fatalf("expected the old executable to be kept aside, found %v", olds)
	}

	// The old file is still in use, so cleanup must not fail, and removes it
	// once the process has exited.
	removeOldExecutables(current)
	_ = proc.Process.Kill()
	_, _ = proc.Process.Wait()
	removeOldExecutables(current)
	if olds, _ := filepath.Glob(current + ".*.old"); len(olds) != 0 {
		t.Errorf("old executables were not cleaned up: %v", olds)
	}
}

func TestReplaceExecutableRollsBackOnFailure(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "wings.exe")
	if err := os.WriteFile(current, []byte("old version"), 0o755); err != nil {
		t.Fatal(err)
	}

	// The new binary does not exist, nothing may change.
	err := replaceExecutable(filepath.Join(dir, "missing.exe"), current)
	if err == nil {
		t.Fatal("expected an error")
	}
	got, _ := os.ReadFile(current)
	if string(got) != "old version" {
		t.Errorf("the executable was modified: %q", got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("leftover files: %s", strings.Join(names, ", "))
	}
}
