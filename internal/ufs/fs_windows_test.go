// SPDX-License-Identifier: MIT

//go:build windows

package ufs_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/pelican/wings/internal/ufs"
)

func newWinFS(t *testing.T) (*ufs.UnixFS, string) {
	t.Helper()
	root := t.TempDir()
	base := filepath.Join(root, "base")
	if err := os.Mkdir(base, 0o755); err != nil {
		t.Fatal(err)
	}
	fs, err := ufs.NewUnixFS(base, false)
	if err != nil {
		t.Fatal(err)
	}
	// The sandbox resolves symlinks in its base path.
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return fs, resolved
}

func TestWindows_RejectsEscapes(t *testing.T) {
	fs, root := newWinFS(t)
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{
		"../secret.txt",
		`..\secret.txt`,
		"a/../../secret.txt",
		filepath.Join(root, "secret.txt"),
		`C:\Windows\win.ini`,
		"file.txt:stream",
	} {
		if _, err := fs.Open(p); !errors.Is(err, ufs.ErrBadPathResolution) {
			t.Errorf("Open(%q): expected ErrBadPathResolution, got %v", p, err)
		}
	}
}

func TestWindows_AbsoluteAndRootedPathsInsideBase(t *testing.T) {
	fs, _ := newWinFS(t)
	if err := os.WriteFile(filepath.Join(fs.BasePath(), "a.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"a.txt", "/a.txt", `\a.txt`, filepath.Join(fs.BasePath(), "a.txt")} {
		f, err := fs.Open(p)
		if err != nil {
			t.Errorf("Open(%q): %v", p, err)
			continue
		}
		b, _ := io.ReadAll(f)
		_ = f.Close()
		if string(b) != "hi" {
			t.Errorf("Open(%q): read %q", p, b)
		}
	}
}

func TestWindows_SymlinkEscape(t *testing.T) {
	fs, root := newWinFS(t)
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(fs.BasePath(), "link")); err != nil {
		t.Skipf("cannot create symlinks: %v", err)
	}

	if _, err := fs.Open("link/f.txt"); err == nil {
		t.Error("expected an error opening a file through a symlink that leaves the sandbox")
	}
	if _, err := fs.Touch("link/new.txt", ufs.O_RDWR, 0o644); err == nil {
		t.Error("expected an error creating a file through a symlink that leaves the sandbox")
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); err == nil {
		t.Error("a file was created outside of the sandbox")
	}
}

func TestWindows_TouchMkdirAllRename(t *testing.T) {
	fs, _ := newWinFS(t)

	f, err := fs.Touch("a/b/c.txt", ufs.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	created, err := fs.MkdirAll("x/y/z", 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 3 || created[0] != "x" || created[2] != "x/y/z" {
		t.Errorf("unexpected created dirs: %v", created)
	}
	if created, err := fs.MkdirAll("x/y/z", 0o755); err != nil || len(created) != 0 {
		t.Errorf("MkdirAll of an existing dir: %v %v", created, err)
	}

	if err := fs.Rename("a/b/c.txt", "moved/deep/c.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Lstat("a/b/c.txt"); !errors.Is(err, ufs.ErrNotExist) {
		t.Errorf("old path still exists: %v", err)
	}
	if err := fs.Rename("moved/deep/c.txt", "a"); !errors.Is(err, ufs.ErrExist) {
		t.Errorf("expected ErrExist renaming over an existing entry, got %v", err)
	}
	if err := fs.Rename(".", "elsewhere"); !errors.Is(err, ufs.ErrBadPathResolution) {
		t.Errorf("expected ErrBadPathResolution renaming the base, got %v", err)
	}
}

func TestWindows_WalkDiratAndSafePath(t *testing.T) {
	fs, _ := newWinFS(t)
	for _, p := range []string{"one.txt", "dir/two.txt", "dir/sub/three.txt"} {
		f, err := fs.Touch(p, ufs.O_RDWR, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}

	dirfd, name, closeFd, err := fs.SafePath("/")
	if err != nil {
		t.Fatal(err)
	}
	defer closeFd()

	var rels []string
	err = fs.WalkDirat(dirfd, name, func(dirfd int, name, rel string, d ufs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			// The dirfd and name must be usable to reach the file.
			info, err := fs.Lstatat(dirfd, name)
			if err != nil {
				return err
			}
			if info.Name() != name {
				t.Errorf("Lstatat(%d, %q) returned %q", dirfd, name, info.Name())
			}
			rels = append(rels, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"dir/sub/three.txt", "dir/two.txt", "one.txt"}
	if len(rels) != len(want) {
		t.Fatalf("walked %v, want %v", rels, want)
	}
	for i := range want {
		if rels[i] != want[i] {
			t.Fatalf("walked %v, want %v", rels, want)
		}
	}

	// A closed dirfd must no longer be usable.
	closeFd()
	if _, err := fs.Lstatat(dirfd, name); err == nil {
		t.Error("expected an error using a released dirfd")
	}
}

func TestWindows_QuotaRemoveAll(t *testing.T) {
	fs, _ := newWinFS(t)
	q := ufs.NewQuota(fs, 1000)

	for _, p := range []string{"d/a.txt", "d/sub/b.txt"} {
		f, err := q.Touch(p, ufs.O_RDWR, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write(make([]byte, 100))
		_ = f.Close()
	}
	q.SetUsage(200)

	if err := q.RemoveAll("d"); err != nil {
		t.Fatal(err)
	}
	if got := q.Usage(); got != 0 {
		t.Errorf("usage after RemoveAll = %d, want 0", got)
	}
	if err := q.RemoveAll("."); !errors.Is(err, ufs.ErrBadPathResolution) {
		t.Errorf("expected ErrBadPathResolution removing the base, got %v", err)
	}
}

func TestWindows_ReadDirSorted(t *testing.T) {
	fs, _ := newWinFS(t)
	for _, n := range []string{"c", "a", "b"} {
		f, err := fs.Touch(n, ufs.O_RDWR, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	entries, err := fs.ReadDir("/")
	if err != nil {
		t.Fatal(err)
	}
	got := ""
	for _, e := range entries {
		got += e.Name()
	}
	if got != "abc" {
		t.Errorf("ReadDir order = %q", got)
	}
}

func TestWindows_ReadDirMapEntriesCanOpen(t *testing.T) {
	fs, _ := newWinFS(t)
	f, err := fs.Touch("dir/a.txt", ufs.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte("hello"))
	_ = f.Close()

	type opener interface {
		Open() (ufs.File, error)
	}
	got, err := ufs.ReadDirMap(fs, "dir", func(e ufs.DirEntry) (string, error) {
		info, err := e.Info()
		if err != nil || info.Name() != "a.txt" {
			t.Errorf("Info: %v %v", info, err)
		}
		o, ok := e.(opener)
		if !ok {
			t.Fatal("entry does not implement Open")
		}
		f, err := o.Open()
		if err != nil {
			return "", err
		}
		defer f.Close()
		b, err := io.ReadAll(f)
		return string(b), err
	})
	if err != nil || len(got) != 1 || got[0] != "hello" {
		t.Errorf("ReadDirMap = %v, %v", got, err)
	}
}
