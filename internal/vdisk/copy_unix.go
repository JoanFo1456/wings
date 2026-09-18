//go:build unix

package vdisk

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"emperror.dev/errors"
)

// CopyTree recursively copies src into dst, preserving permissions, ownership
// and symlinks.
//
// Ownership has to survive: a server's container runs as a fixed uid, and a
// tree that arrived owned by root would leave it unable to read its own files.
func CopyTree(ctx context.Context, src, dst string) error {
	return copyTree(ctx, src, dst)
}

func copyTree(ctx context.Context, src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)

		switch {
		case info.IsDir():
			if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
				return err
			}
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			// Reproduced verbatim, including links that point outside the
			// tree: resolving them would change what the server sees.
			if err := os.Symlink(link, target); err != nil && !os.IsExist(err) {
				return err
			}
			return copyOwnership(target, info, true)
		case !info.Mode().IsRegular():
			// Sockets and fifos are recreated by whatever owns them; copying
			// one would either block or fail.
			return nil
		default:
			if err := copyFile(p, target, info); err != nil {
				return err
			}
		}
		return copyOwnership(target, info, false)
	})
}

func copyFile(src, dst string, info os.FileInfo) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return errors.WrapIff(err, "vdisk: failed to copy %s", src)
	}
	return out.Close()
}

// copyOwnership reproduces the uid and gid of a file on its copy.
func copyOwnership(path string, info os.FileInfo, symlink bool) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if symlink {
		// Chown would follow the link and retarget whatever it points at.
		return os.Lchown(path, int(st.Uid), int(st.Gid))
	}
	return os.Chown(path, int(st.Uid), int(st.Gid))
}
