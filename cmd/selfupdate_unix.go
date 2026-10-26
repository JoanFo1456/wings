//go:build !windows

package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const restartHint = "Please restart the wings service (e.g., systemctl restart wings)"

// replaceExecutable replaces the running executable with the one at binaryPath.
func replaceExecutable(binaryPath, currentExecutable string) error {
	var err error
	// Try rename first (faster if on same filesystem)
	err = os.Rename(binaryPath, currentExecutable)
	if err != nil {
		// If rename fails (likely due to cross-filesystem), use copy instead
		fmt.Println("Direct replacement failed, using copy method...")
		
		// Open source file
		src, err := os.Open(binaryPath)
		if err != nil {
			return fmt.Errorf("failed to open source file: %v", err)
		}
		defer src.Close()
		
		// Create a temporary file in the same directory as the executable
		execDir := filepath.Dir(currentExecutable)
		tempExec := filepath.Join(execDir, fmt.Sprintf(".%s.new", filepath.Base(currentExecutable)))
		
		// Create the new executable file
		dst, err := os.OpenFile(tempExec, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
		if err != nil {
			return fmt.Errorf("failed to create new executable: %v", err)
		}
		
		// Copy the content
		_, err = io.Copy(dst, src)
		dst.Close()
		if err != nil {
			os.Remove(tempExec) // Clean up on failure
			return fmt.Errorf("failed to copy new binary: %v", err)
		}
		
		// Replace the old executable with the new one
		err = os.Rename(tempExec, currentExecutable)
		if err != nil {
			os.Remove(tempExec) // Clean up on failure
			return fmt.Errorf("failed to replace executable: %v", err)
		}
	}

	return nil
}

// cleanupOldExecutables is only needed on Windows, where a running executable
// can't be deleted during an update.
func cleanupOldExecutables() {}
