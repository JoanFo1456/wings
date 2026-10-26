// SPDX-License-Identifier: MIT

//go:build windows

package ufs

import "syscall"

// The flag values below are the ones used by Go's own syscall package on
// Windows. Flags that have no Windows equivalent (O_DIRECTORY, O_NOFOLLOW) are
// given private values and are stripped before a call is made to the OS; they
// are interpreted by this package instead.
const (
	// O_RDONLY opens the file read-only.
	O_RDONLY = syscall.O_RDONLY
	// O_WRONLY opens the file write-only.
	O_WRONLY = syscall.O_WRONLY
	// O_RDWR opens the file read-write.
	O_RDWR = syscall.O_RDWR
	// O_APPEND appends data to the file when writing.
	O_APPEND = syscall.O_APPEND
	// O_CREATE creates a new file if it doesn't exist.
	O_CREATE = syscall.O_CREAT
	// O_EXCL is used with O_CREATE, file must not exist.
	O_EXCL = syscall.O_EXCL
	// O_SYNC open for synchronous I/O.
	O_SYNC = syscall.O_SYNC
	// O_TRUNC truncates regular writable file when opened.
	O_TRUNC = syscall.O_TRUNC
	// O_DIRECTORY opens a directory only. If the entry is not a directory an
	// error will be returned.
	O_DIRECTORY = 0x100000
	// O_NOFOLLOW opens the exact path given without following symlinks.
	O_NOFOLLOW = 0x200000
	O_CLOEXEC  = syscall.O_CLOEXEC
)

const (
	AT_SYMLINK_NOFOLLOW = 0x100
	AT_REMOVEDIR        = 0x200
)

// osOnlyFlags removes the flags that are interpreted by this package rather
// than by the operating system.
func osOnlyFlags(flag int) int {
	return flag &^ (O_DIRECTORY | O_NOFOLLOW)
}
