//go:build linux

package vdisk

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestIntegrationLifecycle exercises the real create/mount/enforce/destroy
// cycle against the host's loop devices.
//
// It is gated behind VDISK_INTEGRATION because it needs privileges most CI
// environments do not have, and because it creates and mounts a real
// filesystem rather than simulating one.
func TestIntegrationLifecycle(t *testing.T) {
	if os.Getenv("VDISK_INTEGRATION") == "" {
		t.Skip("set VDISK_INTEGRATION=1 to run against real loop devices")
	}

	m := New()
	if err := m.Supported(); err != nil {
		t.Skipf("host cannot host virtual disks: %v", err)
	}

	root := t.TempDir()
	opts := Options{
		Image:      filepath.Join(root, "test.img"),
		MountPoint: filepath.Join(root, "mnt"),
		Size:       64 << 20,
		UID:        os.Getuid(),
		GID:        os.Getgid(),
	}

	ctx := context.Background()

	t.Cleanup(func() {
		if err := m.Destroy(ctx, opts); err != nil {
			t.Errorf("cleanup: Destroy: %v", err)
		}
	})

	if err := m.Ensure(ctx, opts); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	mounted, err := m.Mounted(opts.MountPoint)
	if err != nil {
		t.Fatalf("Mounted: %v", err)
	}
	if !mounted {
		t.Fatal("disk reports as not mounted straight after Ensure")
	}

	// A second Ensure must be a no-op rather than a second mount.
	if err := m.Ensure(ctx, opts); err != nil {
		t.Fatalf("Ensure (second call): %v", err)
	}

	usage, err := m.Usage(opts.MountPoint)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	t.Logf("fresh 64MiB disk: used=%d total=%d free=%d inodes=%d/%d",
		usage.Used, usage.Total, usage.Free, usage.InodesUsed, usage.InodesTotal)

	if usage.Total <= 0 {
		t.Errorf("Total = %d, want a positive capacity", usage.Total)
	}
	// The allocation has to actually be available, which is the whole point of
	// sizing the image above the limit.
	if usage.Total < opts.Size {
		t.Errorf("usable capacity %d is smaller than the %d allocated", usage.Total, opts.Size)
	}

	// The kernel must refuse a write past the end of the disk. This is the
	// behaviour the whole feature exists for: userland accounting could never
	// stop a container writing directly into its data directory.
	big := filepath.Join(opts.MountPoint, "toobig")
	if err := writeN(big, 128<<20); err == nil {
		t.Fatal("writing 128MiB into a 64MiB disk succeeded; the limit is not enforced")
	} else if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("writing past the limit failed with %v, want ENOSPC", err)
	}
	_ = os.Remove(big)

	// A write that fits must still succeed.
	small := filepath.Join(opts.MountPoint, "fits")
	if err := writeN(small, 8<<20); err != nil {
		t.Fatalf("writing 8MiB into a 64MiB disk failed: %v", err)
	}

	after, err := m.Usage(opts.MountPoint)
	if err != nil {
		t.Fatalf("Usage after write: %v", err)
	}
	if after.Used < 8<<20 {
		t.Errorf("Used = %d after writing 8MiB, want at least %d", after.Used, 8<<20)
	}
	t.Logf("after writing 8MiB: used=%d free=%d", after.Used, after.Free)
}

// TestIntegrationGrow checks that a disk can be enlarged while it is mounted,
// which is what keeps a running server from being interrupted by a plan change.
func TestIntegrationGrow(t *testing.T) {
	if os.Getenv("VDISK_INTEGRATION") == "" {
		t.Skip("set VDISK_INTEGRATION=1 to run against real loop devices")
	}

	m := New()
	if err := m.Supported(); err != nil {
		t.Skipf("host cannot host virtual disks: %v", err)
	}

	root := t.TempDir()
	opts := Options{
		Image:      filepath.Join(root, "grow.img"),
		MountPoint: filepath.Join(root, "mnt"),
		Size:       64 << 20,
		UID:        os.Getuid(),
		GID:        os.Getgid(),
	}
	ctx := context.Background()

	if err := m.Ensure(ctx, opts); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	t.Cleanup(func() {
		if err := m.Destroy(ctx, opts); err != nil {
			t.Errorf("cleanup: Destroy: %v", err)
		}
	})

	before, err := m.Usage(opts.MountPoint)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}

	grown := opts
	grown.Size = 192 << 20
	if err := m.Resize(ctx, grown); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	// The disk must still be mounted: growing is an online operation.
	mounted, err := m.Mounted(opts.MountPoint)
	if err != nil {
		t.Fatalf("Mounted: %v", err)
	}
	if !mounted {
		t.Fatal("disk was unmounted by a grow")
	}

	after, err := m.Usage(opts.MountPoint)
	if err != nil {
		t.Fatalf("Usage after grow: %v", err)
	}
	if after.Total <= before.Total {
		t.Fatalf("capacity did not grow: %d -> %d", before.Total, after.Total)
	}
	if after.Total < grown.Size {
		t.Errorf("usable capacity %d is smaller than the %d allocated", after.Total, grown.Size)
	}
	t.Logf("grew 64MiB -> 192MiB: total %d -> %d", before.Total, after.Total)
}

// writeN writes n bytes to path, returning the underlying syscall error so the
// caller can tell a quota rejection from any other failure.
func writeN(path string, n int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	buf := make([]byte, 1<<20)
	for written := int64(0); written < n; written += int64(len(buf)) {
		if _, err := f.Write(buf); err != nil {
			return err
		}
	}
	// Buffered writes can be accepted and only fail when they reach the disk.
	return f.Sync()
}

// TestIntegrationShrink checks that a disk can be reduced, which is what makes
// an "unlimited" server convertible to a real limit later on.
func TestIntegrationShrink(t *testing.T) {
	if os.Getenv("VDISK_INTEGRATION") == "" {
		t.Skip("set VDISK_INTEGRATION=1 to run against real loop devices")
	}

	m := New()
	if err := m.Supported(); err != nil {
		t.Skipf("host cannot host virtual disks: %v", err)
	}

	root := t.TempDir()
	opts := Options{
		Image:      filepath.Join(root, "shrink.img"),
		MountPoint: filepath.Join(root, "mnt"),
		Size:       512 << 20,
		UID:        os.Getuid(),
		GID:        os.Getgid(),
	}
	ctx := context.Background()

	if err := m.Ensure(ctx, opts); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	t.Cleanup(func() {
		if err := m.Destroy(ctx, opts); err != nil {
			t.Errorf("cleanup: Destroy: %v", err)
		}
	})

	// Put something on the disk so the shrink has real data to preserve.
	payload := filepath.Join(opts.MountPoint, "keepme")
	if err := writeN(payload, 16<<20); err != nil {
		t.Fatalf("writing payload: %v", err)
	}

	before, err := m.Usage(opts.MountPoint)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}

	shrunk := opts
	shrunk.Size = 128 << 20
	if err := m.Resize(ctx, shrunk); err != nil {
		t.Fatalf("Resize (shrink): %v", err)
	}

	// Shrinking unmounts the disk, so bring it back before inspecting it.
	if err := m.Ensure(ctx, shrunk); err != nil {
		t.Fatalf("Ensure after shrink: %v", err)
	}

	after, err := m.Usage(opts.MountPoint)
	if err != nil {
		t.Fatalf("Usage after shrink: %v", err)
	}
	if after.Total >= before.Total {
		t.Fatalf("capacity did not shrink: %d -> %d", before.Total, after.Total)
	}
	if after.Total < shrunk.Size {
		t.Errorf("usable capacity %d is smaller than the %d allocated", after.Total, shrunk.Size)
	}

	// The data has to survive, otherwise a plan change would silently destroy
	// a customer's server.
	st, err := os.Stat(payload)
	if err != nil {
		t.Fatalf("payload missing after shrink: %v", err)
	}
	if st.Size() != 16<<20 {
		t.Errorf("payload is %d bytes after shrink, want %d", st.Size(), 16<<20)
	}
	t.Logf("shrank 512MiB -> 128MiB: total %d -> %d, payload intact", before.Total, after.Total)
}

// TestIntegrationShrinkRefusedWhenDataWouldNotFit checks that a shrink below
// the data already stored is rejected rather than destroying it.
func TestIntegrationShrinkRefusedWhenDataWouldNotFit(t *testing.T) {
	if os.Getenv("VDISK_INTEGRATION") == "" {
		t.Skip("set VDISK_INTEGRATION=1 to run against real loop devices")
	}

	m := New()
	if err := m.Supported(); err != nil {
		t.Skipf("host cannot host virtual disks: %v", err)
	}

	root := t.TempDir()
	opts := Options{
		Image:      filepath.Join(root, "toosmall.img"),
		MountPoint: filepath.Join(root, "mnt"),
		Size:       256 << 20,
		UID:        os.Getuid(),
		GID:        os.Getgid(),
	}
	ctx := context.Background()

	if err := m.Ensure(ctx, opts); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	t.Cleanup(func() {
		if err := m.Destroy(ctx, opts); err != nil {
			t.Errorf("cleanup: Destroy: %v", err)
		}
	})

	if err := writeN(filepath.Join(opts.MountPoint, "bulk"), 100<<20); err != nil {
		t.Fatalf("writing payload: %v", err)
	}

	tooSmall := opts
	tooSmall.Size = 48 << 20
	err := m.Resize(ctx, tooSmall)
	if err == nil {
		t.Fatal("shrinking below the stored data succeeded; data would have been lost")
	}
	if !errors.Is(err, ErrWouldNotFit) {
		t.Fatalf("Resize failed with %v, want ErrWouldNotFit", err)
	}
	t.Logf("correctly refused: %v", err)
}

// TestLostFoundWithRecoveredFilesIsKept checks that a lost+found holding files
// recovered by e2fsck is never deleted for the sake of a tidy listing.
func TestLostFoundWithRecoveredFilesIsKept(t *testing.T) {
	// No loop device needed: removeLostFound only inspects the directory.
	mount := t.TempDir()
	lf := filepath.Join(mount, "lost+found")
	if err := os.Mkdir(lf, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(lf, "#12345"), []byte("recovered"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	removeLostFound(mount)

	if _, err := os.Stat(lf); err != nil {
		t.Fatalf("lost+found holding recovered files was removed: %v", err)
	}
}

// TestEmptyLostFoundIsRemoved is the counterpart: an empty one goes.
func TestEmptyLostFoundIsRemoved(t *testing.T) {
	mount := t.TempDir()
	lf := filepath.Join(mount, "lost+found")
	if err := os.Mkdir(lf, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	removeLostFound(mount)

	if _, err := os.Stat(lf); !os.IsNotExist(err) {
		t.Fatalf("empty lost+found was not removed (stat err: %v)", err)
	}
}

// TestIntegrationShrinkFarBelowExt4Minimum reproduces moving an "unlimited"
// server onto a small plan.
//
// ext4 will not shrink a filesystem below a floor set by the metadata it was
// created with, and that floor scales with the original size — a 64 GiB disk
// will not resize down to 500 MiB however empty it is. The disk has to end up
// at the size that was asked for regardless, with no hidden remainder.
func TestIntegrationShrinkFarBelowExt4Minimum(t *testing.T) {
	if os.Getenv("VDISK_INTEGRATION") == "" {
		t.Skip("set VDISK_INTEGRATION=1 to run against real loop devices")
	}

	m := New()
	if err := m.Supported(); err != nil {
		t.Skipf("host cannot host virtual disks: %v", err)
	}

	root := t.TempDir()
	opts := Options{
		Image:      filepath.Join(root, "huge.img"),
		MountPoint: filepath.Join(root, "mnt"),
		Size:       64 << 30, // 64 GiB, sparse so it costs almost nothing
		UID:        os.Getuid(),
		GID:        os.Getgid(),
	}
	ctx := context.Background()

	if err := m.Ensure(ctx, opts); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	t.Cleanup(func() {
		if err := m.Destroy(ctx, opts); err != nil {
			t.Errorf("cleanup: Destroy: %v", err)
		}
	})

	payload := filepath.Join(opts.MountPoint, "world.dat")
	if err := writeN(payload, 12<<20); err != nil {
		t.Fatalf("writing payload: %v", err)
	}

	before, err := m.Usage(opts.MountPoint)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}

	const want = 500 << 20
	small := opts
	small.Size = want
	if err := m.Resize(ctx, small); err != nil {
		t.Fatalf("Resize to 500MiB: %v", err)
	}
	if err := m.Ensure(ctx, small); err != nil {
		t.Fatalf("Ensure after shrink: %v", err)
	}

	after, err := m.Usage(opts.MountPoint)
	if err != nil {
		t.Fatalf("Usage after shrink: %v", err)
	}

	// The user asked for 500 MiB and must get 500 MiB of usable space.
	if after.Total < want {
		t.Errorf("usable capacity %d is below the %d requested", after.Total, want)
	}
	// And it must not be secretly far larger, which would mean the shrink had
	// quietly not happened and the limit was not being enforced.
	if after.Total > want*2 {
		t.Errorf("usable capacity %d is far above the %d requested; the disk did not actually shrink", after.Total, want)
	}

	st, err := os.Stat(payload)
	if err != nil {
		t.Fatalf("payload missing after shrink: %v", err)
	}
	if st.Size() != 12<<20 {
		t.Errorf("payload is %d bytes, want %d", st.Size(), 12<<20)
	}

	image, err := os.Stat(opts.Image)
	if err != nil {
		t.Fatalf("Stat image: %v", err)
	}
	t.Logf("64GiB -> 500MiB: usable %d -> %d, image now %d bytes, payload intact",
		before.Total, after.Total, image.Size())
}
