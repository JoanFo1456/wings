//go:build windows

package cmd

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const restartHint = "Please restart Wings, for example with: Stop-ScheduledTask -TaskName Wings; Start-ScheduledTask -TaskName Wings"

// replaceExecutable replaces the running executable with the one at binaryPath.
//
// Windows does not allow a running executable to be overwritten or deleted, but
// it can be renamed. So the new binary is copied next to the current one (a
// rename can't cross drives), the running one is moved out of the way, and the
// new one takes its place. The old file is removed the next time Wings starts.
func replaceExecutable(binaryPath, currentExecutable string) error {
	newPath := currentExecutable + ".new"
	oldPath := fmt.Sprintf("%s.%d.old", currentExecutable, time.Now().UnixNano())

	if err := copyExecutable(binaryPath, newPath); err != nil {
		_ = os.Remove(newPath)
		return permissionHint(fmt.Errorf("failed to copy new binary: %w", err))
	}
	if err := os.Rename(currentExecutable, oldPath); err != nil {
		_ = os.Remove(newPath)
		return permissionHint(fmt.Errorf("failed to move the current executable aside: %w", err))
	}
	if err := os.Rename(newPath, currentExecutable); err != nil {
		// Put the working executable back, the update did not happen.
		_ = os.Rename(oldPath, currentExecutable)
		_ = os.Remove(newPath)
		return permissionHint(fmt.Errorf("failed to replace executable: %w", err))
	}
	return nil
}

func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// permissionHint explains how to fix the most common reason an update fails,
// the executable being in a folder such as C:\Program Files.
func permissionHint(err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("%w (run the update from a PowerShell window opened as Administrator)", err)
	}
	return err
}

// cleanupOldExecutables removes the executables that previous updates moved out
// of the way. They can't be deleted while the old process is still running, so
// this is done on the next start. Failures are ignored.
func cleanupOldExecutables() {
	if exe, err := os.Executable(); err == nil {
		removeOldExecutables(exe)
	}
}

func removeOldExecutables(exe string) {
	matches, err := filepath.Glob(exe + ".*.old")
	if err != nil {
		return
	}
	for _, m := range matches {
		_ = os.Remove(m)
	}
}
