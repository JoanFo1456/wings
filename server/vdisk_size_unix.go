//go:build unix

package server

import (
	"emperror.dev/errors"
	"golang.org/x/sys/unix"
)

// filesystemSize returns the total capacity, in bytes, of the filesystem
// holding path.
//
// This is the whole volume rather than what is currently free: an unlimited
// server's disk is sized against the storage the node actually has, and the
// image is sparse, so capacity it never writes to costs nothing.
func filesystemSize(path string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, errors.WrapIff(err, "server: failed to measure the filesystem at %s", path)
	}
	return int64(st.Blocks) * int64(st.Bsize), nil
}
