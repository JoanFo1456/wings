//go:build windows

package filesystem

import (
	"syscall"
	"time"
)

// CTime returns the time that the file/folder was created.
func (s *Stat) CTime() time.Time {
	if d, ok := s.Sys().(*syscall.Win32FileAttributeData); ok {
		return time.Unix(0, d.CreationTime.Nanoseconds())
	}
	return time.Time{}
}
