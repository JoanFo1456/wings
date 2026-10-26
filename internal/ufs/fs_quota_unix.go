// SPDX-License-Identifier: MIT
// SPDX-FileCopyrightText: Copyright (c) 2024 Matthew Penner

//go:build unix

package ufs

func (fs *Quota) removeAll(path string) error {
	return removeAll(fs, path)
}

func (fs *Quota) unlinkat(dirfd int, name string, flags int) error {
	if flags == 0 {
		s, err := fs.Lstatat(dirfd, name)
		if err == nil && s.Mode().IsRegular() {
			fs.Add(-s.Size())
		}
	}
	return fs.UnixFS.unlinkat(dirfd, name, flags)
}
