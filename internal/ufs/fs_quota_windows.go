// SPDX-License-Identifier: MIT

//go:build windows

package ufs

import (
	"errors"
	"path"
)

// removeAll removes path and everything below it, subtracting the size of every
// regular file that was actually removed from the quota usage.
func (fs *Quota) removeAll(p string) error {
	type sized struct {
		path string
		size int64
	}
	var files []sized
	_ = fs.UnixFS.WalkDir(p, func(name string, d DirEntry, err error) error {
		if err != nil || d == nil || !d.Type().IsRegular() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			files = append(files, sized{path: path.Clean(name), size: info.Size()})
		}
		return nil
	})

	err := fs.UnixFS.removeAll(p)
	for _, f := range files {
		if _, serr := fs.UnixFS.Lstat(f.path); errors.Is(serr, ErrNotExist) {
			fs.Add(-f.size)
		}
	}
	return err
}
