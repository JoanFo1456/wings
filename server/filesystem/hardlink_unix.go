//go:build unix

package filesystem

import (
	"os"

	"golang.org/x/sys/unix"
)

// hardLinkID returns the inode number of a file that has more than one hard
// link, and false for any other file.
func hardLinkID(info os.FileInfo) (uint64, bool) {
	st, ok := info.Sys().(*unix.Stat_t)
	if !ok || st.Nlink <= 1 {
		return 0, false
	}
	return uint64(st.Ino), true
}
