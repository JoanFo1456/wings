//go:build windows

package cmd

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isReadOnlyFS reports whether err was caused by writing to a read-only filesystem.
func isReadOnlyFS(err error) bool {
	return errors.Is(err, windows.ERROR_WRITE_PROTECT)
}
