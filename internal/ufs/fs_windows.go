// SPDX-License-Identifier: MIT

//go:build windows

package ufs

import (
	"errors"
	iofs "io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// UnixFS is a sandboxed filesystem. Despite the name (kept so that the rest of
// Wings can use the same type on every platform) the Windows implementation is
// built on top of [os.Root], which guarantees that no operation can escape the
// base path, including through symlinks and junctions.
//
// Unix file descriptors for directories ("dirfd") are emulated with a handle
// table: a dirfd is an opaque integer which maps to an open [os.Root].
type UnixFS struct {
	// basePath is the base path for file operations to take place in.
	basePath string

	mu      sync.Mutex
	next    int
	handles map[int]*os.Root
}

// NewUnixFS creates a new sandboxed filesystem. BasePath is used as the sandbox
// path. The useOpenat2 argument exists for API compatibility and is ignored.
func NewUnixFS(basePath string, _ bool) (*UnixFS, error) {
	basePath = filepath.Clean(basePath)
	if resolved, err := filepath.EvalSymlinks(basePath); err == nil {
		basePath = resolved
	}
	return &UnixFS{
		basePath: basePath,
		handles:  make(map[int]*os.Root),
	}, nil
}

// BasePath returns the base path of the sandbox, file operations pointing
// outside this path are prohibited.
func (fs *UnixFS) BasePath() string {
	return fs.basePath
}

// Close releases any resources held by the filesystem.
func (fs *UnixFS) Close() error {
	return nil
}

// register stores r in the handle table and returns its handle.
func (fs *UnixFS) register(r *os.Root) int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.next++
	fs.handles[fs.next] = r
	return fs.next
}

// release removes a handle from the table without closing the root.
func (fs *UnixFS) release(dirfd int) {
	fs.mu.Lock()
	delete(fs.handles, dirfd)
	fs.mu.Unlock()
}

// root returns the [os.Root] behind a dirfd.
func (fs *UnixFS) root(dirfd int) (*os.Root, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	r, ok := fs.handles[dirfd]
	if !ok {
		return nil, &PathError{Op: "dirfd", Path: "", Err: ErrClosed}
	}
	return r, nil
}

// wrapErr maps the errors returned by [os.Root] onto the errors of this package.
func wrapErr(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "path escapes from parent") {
		var pErr *PathError
		if errors.As(err, &pErr) {
			return &PathError{Op: pErr.Op, Path: pErr.Path, Err: ErrBadPathResolution}
		}
		return &PathError{Op: "safePath", Path: "", Err: ErrBadPathResolution}
	}
	return err
}

// wrapDirErr is like wrapErr for errors that occurred while traversing a
// directory in the middle of a path. A symlink that leaves the sandbox is
// reported as "not a directory", which is what the Unix implementation reports
// since it refuses to traverse symlinks at all.
func wrapDirErr(err error) error {
	if err = wrapErr(err); errors.Is(err, ErrBadPathResolution) {
		var pErr *PathError
		if errors.As(err, &pErr) {
			return &PathError{Op: pErr.Op, Path: pErr.Path, Err: ErrNotDirectory}
		}
	}
	return err
}

// unsafePath converts path, which may be relative to the base path or an
// absolute path inside of it, to a slash separated path relative to the base
// path. The fully resolved path (following symlinks) is not checked, the
// returned path must only ever be used through an [os.Root].
func (fs *UnixFS) unsafePath(p string) (string, error) {
	bad := &PathError{Op: "safePath", Path: p, Err: ErrBadPathResolution}

	p = filepath.FromSlash(p)
	var rel string
	if filepath.VolumeName(p) != "" {
		r, err := filepath.Rel(fs.basePath, filepath.Clean(p))
		if err != nil {
			return "", bad
		}
		rel = r
	} else {
		// A rooted path without a volume (e.g. "\foo") is relative to the base.
		r, err := filepath.Rel(fs.basePath, filepath.Join(fs.basePath, p))
		if err != nil {
			return "", bad
		}
		rel = r
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", bad
	}
	// Alternate data streams (file:stream) and device paths are never allowed.
	if strings.ContainsRune(rel, ':') {
		return "", bad
	}
	return filepath.ToSlash(rel), nil
}

// SafePath resolves path and opens the directory containing it, returning a
// dirfd for the directory, the name of the entry within it and a function that
// must be called to release the dirfd.
func (fs *UnixFS) SafePath(path string) (int, string, func(), error) {
	return fs.safePath(path)
}

func (fs *UnixFS) safePath(p string) (dirfd int, file string, closeFd func(), err error) {
	closeFd = func() {}

	var name string
	name, err = fs.unsafePath(p)
	if err != nil {
		return
	}

	var base *os.Root
	base, err = os.OpenRoot(fs.basePath)
	if err != nil {
		err = wrapErr(err)
		return
	}

	dir, file := path.Split(name)
	if dir == "" {
		dirfd = fs.register(base)
		closeFd = func() {
			fs.release(dirfd)
			_ = base.Close()
		}
		return dirfd, file, closeFd, nil
	}

	sub, err := base.OpenRoot(strings.TrimSuffix(dir, "/"))
	if err != nil {
		_ = base.Close()
		return 0, file, func() {}, wrapDirErr(err)
	}
	dirfd = fs.register(sub)
	closeFd = func() {
		fs.release(dirfd)
		_ = sub.Close()
		_ = base.Close()
	}
	return dirfd, file, closeFd, nil
}

// withEntry resolves p and calls fn with the directory root containing it and
// the entry name.
func (fs *UnixFS) withEntry(p string, fn func(r *os.Root, name string) error) error {
	dirfd, name, closeFd, err := fs.safePath(p)
	defer closeFd()
	if err != nil {
		return err
	}
	r, err := fs.root(dirfd)
	if err != nil {
		return err
	}
	return wrapErr(fn(r, name))
}

// Chmod changes the mode of the named file to mode.
func (fs *UnixFS) Chmod(name string, mode FileMode) error {
	return fs.withEntry(name, func(r *os.Root, n string) error { return r.Chmod(n, mode) })
}

// Chmodat is like Chmod but relative to dirfd.
func (fs *UnixFS) Chmodat(dirfd int, name string, mode FileMode) error {
	r, err := fs.root(dirfd)
	if err != nil {
		return err
	}
	return wrapErr(r.Chmod(name, mode))
}

// Chown is a no-op on Windows, file ownership is not meaningful there. Docker
// Desktop takes care of ownership inside of its Linux VM.
func (fs *UnixFS) Chown(name string, uid, gid int) error { return nil }

// Lchown is a no-op on Windows.
func (fs *UnixFS) Lchown(name string, uid, gid int) error { return nil }

// Chownat is a no-op on Windows.
func (fs *UnixFS) Chownat(dirfd int, name string, uid, gid int) error { return nil }

// Lchownat is a no-op on Windows.
func (fs *UnixFS) Lchownat(dirfd int, name string, uid, gid int) error { return nil }

// Chtimes changes the access and modification times of the named file.
func (fs *UnixFS) Chtimes(name string, atime, mtime time.Time) error {
	return fs.withEntry(name, func(r *os.Root, n string) error { return r.Chtimes(n, atime, mtime) })
}

// Create creates or truncates the named file.
func (fs *UnixFS) Create(name string) (File, error) {
	return fs.OpenFile(name, O_RDWR|O_CREATE|O_TRUNC, 0o666)
}

// Mkdir creates a new directory with the specified name.
func (fs *UnixFS) Mkdir(name string, mode FileMode) error {
	return fs.withEntry(name, func(r *os.Root, n string) error { return r.Mkdir(n, mode) })
}

// Mkdirat is like Mkdir but relative to dirfd.
func (fs *UnixFS) Mkdirat(dirfd int, name string, mode FileMode) error {
	r, err := fs.root(dirfd)
	if err != nil {
		return err
	}
	return wrapErr(r.Mkdir(name, mode))
}

// MkdirAll creates a directory named path, along with any necessary parents,
// and returns the directories it created ordered from shallowest to deepest.
func (fs *UnixFS) MkdirAll(name string, mode FileMode) ([]string, error) {
	name, err := fs.unsafePath(name)
	if err != nil {
		return nil, err
	}
	if name == "." {
		return nil, nil
	}

	base, err := os.OpenRoot(fs.basePath)
	if err != nil {
		return nil, wrapDirErr(err)
	}
	defer base.Close()

	var created []string
	var cur string
	for _, part := range strings.Split(name, "/") {
		if part == "" {
			continue
		}
		cur = path.Join(cur, part)
		info, err := base.Stat(cur)
		if err == nil {
			if !info.IsDir() {
				return created, &PathError{Op: "mkdir", Path: cur, Err: ErrNotDirectory}
			}
			continue
		}
		if !errors.Is(err, ErrNotExist) {
			return created, wrapDirErr(err)
		}
		if err := base.Mkdir(cur, mode); err != nil {
			// Someone else may have created it in the meantime.
			if info, err1 := base.Stat(cur); err1 == nil && info.IsDir() {
				continue
			}
			return created, wrapDirErr(err)
		}
		created = append(created, cur)
	}
	return created, nil
}

// Open opens the named file for reading.
func (fs *UnixFS) Open(name string) (File, error) {
	return fs.OpenFile(name, O_RDONLY, 0)
}

// OpenFile is the generalized open call.
func (fs *UnixFS) OpenFile(name string, flag int, mode FileMode) (File, error) {
	dirfd, name, closeFd, err := fs.safePath(name)
	defer closeFd()
	if err != nil {
		return nil, err
	}
	return fs.OpenFileat(dirfd, name, flag, mode)
}

// OpenFileat is like OpenFile but relative to dirfd.
func (fs *UnixFS) OpenFileat(dirfd int, name string, flag int, mode FileMode) (File, error) {
	r, err := fs.root(dirfd)
	if err != nil {
		return nil, err
	}
	f, err := r.OpenFile(name, osOnlyFlags(flag), mode)
	if err != nil {
		return nil, wrapErr(err)
	}
	if flag&O_DIRECTORY != 0 {
		info, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		if !info.IsDir() {
			_ = f.Close()
			return nil, &PathError{Op: "openat", Path: name, Err: ErrNotDirectory}
		}
	}
	return f, nil
}

// readDirRoot reads and sorts the entries of the directory name within r.
func readDirRoot(r *os.Root, name string) ([]DirEntry, error) {
	f, err := r.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, err
}

// ReadDir reads the named directory, returning all its entries sorted by name.
func (fs *UnixFS) ReadDir(p string) ([]DirEntry, error) {
	dirfd, name, closeFd, err := fs.safePath(p)
	defer closeFd()
	if err != nil {
		return nil, err
	}
	r, err := fs.root(dirfd)
	if err != nil {
		return nil, err
	}
	return readDirRoot(r, name)
}

// dirent is a directory entry that can be opened and stat-ed relative to the
// directory it was read from. It is only valid while that directory is open,
// which is for the duration of the callback given to [ReadDirMap].
type dirent struct {
	DirEntry
	root *os.Root
	path string
}

// Info returns the information of the entry without following symlinks.
func (de dirent) Info() (FileInfo, error) {
	info, err := de.root.Lstat(de.path)
	return info, wrapErr(err)
}

// Open opens the entry for reading.
func (de dirent) Open() (File, error) {
	f, err := de.root.OpenFile(de.path, os.O_RDONLY, 0)
	if err != nil {
		return nil, wrapErr(err)
	}
	return f, nil
}

// ReadDirMap reads the named directory and maps every entry using fn. The
// entries passed to fn also implement `Open() (File, error)`.
func ReadDirMap[T any](fs *UnixFS, p string, fn func(DirEntry) (T, error)) ([]T, error) {
	dirfd, name, closeFd, err := fs.safePath(p)
	defer closeFd()
	if err != nil {
		return nil, err
	}
	r, err := fs.root(dirfd)
	if err != nil {
		return nil, err
	}
	entries, err := readDirRoot(r, name)
	if err != nil {
		return nil, err
	}
	out := make([]T, len(entries))
	for i, e := range entries {
		v, err := fn(dirent{DirEntry: e, root: r, path: path.Join(name, e.Name())})
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// RemoveStat is a combination of Stat and Remove, see the Unix implementation.
func (fs *UnixFS) RemoveStat(name string) (FileInfo, error) {
	dirfd, name, closeFd, err := fs.safePath(name)
	defer closeFd()
	if err != nil {
		return nil, err
	}
	r, err := fs.root(dirfd)
	if err != nil {
		return nil, err
	}
	s, err := r.Lstat(name)
	if err != nil {
		return nil, wrapErr(err)
	}
	if err := r.Remove(name); err != nil {
		return s, wrapErr(err)
	}
	return s, nil
}

// Remove removes the named file or (empty) directory.
func (fs *UnixFS) Remove(name string) error {
	_, err := fs.RemoveStat(name)
	return err
}

// RemoveAll removes path and any children it contains.
func (fs *UnixFS) RemoveAll(name string) error {
	name, err := fs.unsafePath(name)
	if err != nil {
		return err
	}
	if name == "." {
		return &PathError{Op: "removeall", Path: name, Err: ErrBadPathResolution}
	}
	return fs.removeAll(name)
}

func (fs *UnixFS) removeAll(name string) error {
	base, err := os.OpenRoot(fs.basePath)
	if err != nil {
		return wrapErr(err)
	}
	defer base.Close()
	return wrapErr(base.RemoveAll(name))
}

// RemoveContents recursively removes the contents of name, but not name itself.
func (fs *UnixFS) RemoveContents(name string) error {
	name, err := fs.unsafePath(name)
	if err != nil {
		return err
	}
	return fs.removeContents(name)
}

func (fs *UnixFS) removeContents(name string) error {
	base, err := os.OpenRoot(fs.basePath)
	if err != nil {
		return wrapErr(err)
	}
	defer base.Close()
	entries, err := readDirRoot(base, name)
	if err != nil {
		if errors.Is(err, ErrNotExist) {
			return nil
		}
		return err
	}
	var first error
	for _, e := range entries {
		if err := base.RemoveAll(path.Join(name, e.Name())); err != nil && first == nil {
			first = wrapErr(err)
		}
	}
	return first
}

// unlinkat removes name relative to dirfd.
func (fs *UnixFS) unlinkat(dirfd int, name string, flags int) error {
	r, err := fs.root(dirfd)
	if err != nil {
		return err
	}
	return wrapErr(r.Remove(name))
}

// Rename renames (moves) oldpath to newpath. It never replaces an existing
// entry and creates any missing parent directories of newpath.
func (fs *UnixFS) Rename(oldpath, newpath string) error {
	if oldpath == newpath {
		return nil
	}
	oldname, err := fs.unsafePath(oldpath)
	if err != nil {
		return err
	}
	newname, err := fs.unsafePath(newpath)
	if err != nil {
		return err
	}
	if oldname == "." {
		return &PathError{Op: "rename", Path: oldname, Err: ErrBadPathResolution}
	}
	if newname == "." {
		return &PathError{Op: "rename", Path: newname, Err: ErrBadPathResolution}
	}

	base, err := os.OpenRoot(fs.basePath)
	if err != nil {
		return wrapErr(err)
	}
	defer base.Close()

	if _, err := base.Lstat(oldname); err != nil {
		return wrapErr(err)
	}
	if dir := path.Dir(newname); dir != "." {
		if _, err := fs.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	_, err = base.Lstat(newname)
	switch {
	case err == nil:
		return &PathError{Op: "rename", Path: newname, Err: ErrExist}
	case !errors.Is(err, ErrNotExist):
		return wrapErr(err)
	}
	if err := base.Rename(oldname, newname); err != nil {
		return &LinkError{Op: "rename", Old: oldpath, New: newpath, Err: wrapErr(err)}
	}
	return nil
}

// Stat returns a FileInfo describing the named file, following symlinks.
func (fs *UnixFS) Stat(name string) (FileInfo, error) {
	var info FileInfo
	err := fs.withEntry(name, func(r *os.Root, n string) (err error) {
		info, err = r.Stat(n)
		return
	})
	return info, err
}

// Statat is like Stat but relative to dirfd.
func (fs *UnixFS) Statat(dirfd int, name string) (FileInfo, error) {
	r, err := fs.root(dirfd)
	if err != nil {
		return nil, err
	}
	info, err := r.Stat(name)
	return info, wrapErr(err)
}

// Lstat returns a FileInfo describing the named file without following
// symlinks.
func (fs *UnixFS) Lstat(name string) (FileInfo, error) {
	var info FileInfo
	err := fs.withEntry(name, func(r *os.Root, n string) (err error) {
		info, err = r.Lstat(n)
		return
	})
	return info, err
}

// Lstatat is like Lstat but relative to dirfd.
func (fs *UnixFS) Lstatat(dirfd int, name string) (FileInfo, error) {
	r, err := fs.root(dirfd)
	if err != nil {
		return nil, err
	}
	info, err := r.Lstat(name)
	return info, wrapErr(err)
}

// Symlink creates newpath as a symbolic link to oldpath. The target is not
// restricted, but the link can never be followed out of the sandbox.
func (fs *UnixFS) Symlink(oldpath, newpath string) error {
	return fs.withEntry(newpath, func(r *os.Root, n string) error {
		if err := r.Symlink(oldpath, n); err != nil {
			return &LinkError{Op: "symlink", Old: oldpath, New: n, Err: err}
		}
		return nil
	})
}

// Touch opens a file for reading and/or writing, creating it and any missing
// parent directories if needed.
func (fs *UnixFS) Touch(p string, flag int, mode FileMode) (File, error) {
	if flag&O_CREATE == 0 {
		flag |= O_CREATE
	}
	dirfd, name, closeFd, err, _ := fs.TouchPath(p)
	defer closeFd()
	if err != nil {
		return nil, err
	}
	return fs.OpenFileat(dirfd, name, flag, mode)
}

// TouchPath is like SafePath except that it will create any missing
// directories in the path. The returned boolean reports whether the parent
// directories already existed.
func (fs *UnixFS) TouchPath(p string) (int, string, func(), error, bool) {
	dirfd, name, closeFd, err := fs.safePath(p)
	switch {
	case err == nil:
		return dirfd, name, closeFd, nil, true
	case !errors.Is(err, ErrNotExist):
		return dirfd, name, closeFd, err, false
	}

	var pathErr *PathError
	if !errors.As(err, &pathErr) {
		return dirfd, name, closeFd, err, false
	}
	if _, err := fs.MkdirAll(pathErr.Path, 0o755); err != nil {
		return dirfd, name, closeFd, err, false
	}
	closeFd()

	dirfd, name, closeFd, err = fs.safePath(p)
	return dirfd, name, closeFd, err, false
}

// WalkDir walks the file tree rooted at root, calling fn for each file or
// directory in the tree, including root.
func (fs *UnixFS) WalkDir(root string, fn WalkDirFunc) error {
	return WalkDir(fs, root, fn)
}

// WalkDiratFunc is the type of the function called by WalkDirat.
type WalkDiratFunc func(dirfd int, name, relative string, d DirEntry, err error) error

// WalkDirat walks the file tree rooted at name, relative to dirfd.
func (fs *UnixFS) WalkDirat(dirfd int, name string, fn WalkDiratFunc) error {
	r, err := fs.root(dirfd)
	if err != nil {
		return err
	}
	info, err := r.Lstat(name)
	if err != nil {
		err = fn(dirfd, name, ".", nil, wrapErr(err))
	} else {
		err = fs.walkDir(r, dirfd, name, ".", iofs.FileInfoToDirEntry(info), fn)
	}
	if err == SkipDir || err == SkipAll {
		return nil
	}
	return err
}

func (fs *UnixFS) walkDir(parent *os.Root, parentfd int, name, relative string, d DirEntry, walkDirFn WalkDiratFunc) error {
	if err := walkDirFn(parentfd, name, relative, d, nil); err != nil || !d.IsDir() {
		if err == SkipDir && d.IsDir() {
			err = nil
		}
		return err
	}

	sub, err := parent.OpenRoot(name)
	if err != nil {
		return wrapErr(err)
	}
	defer sub.Close()
	dirfd := fs.register(sub)
	defer fs.release(dirfd)

	dirs, err := readDirRoot(sub, ".")
	if err != nil {
		// Second call, to report ReadDir error.
		err = walkDirFn(dirfd, name, relative, d, err)
		if err != nil {
			if err == SkipDir && d.IsDir() {
				err = nil
			}
			return err
		}
	}

	for _, d1 := range dirs {
		n := d1.Name()
		rel := n
		if relative != "." {
			rel = path.Join(relative, n)
		}
		if err := fs.walkDir(sub, dirfd, n, rel, d1, walkDirFn); err != nil {
			if err == SkipDir {
				break
			}
			return err
		}
	}
	return nil
}
