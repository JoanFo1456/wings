//go:build unix

package cmd

import (
	"errors"

	"golang.org/x/sys/unix"
)

// isReadOnlyFS reports whether err was caused by writing to a read-only filesystem.
func isReadOnlyFS(err error) bool {
	return errors.Is(err, unix.EROFS)
}
