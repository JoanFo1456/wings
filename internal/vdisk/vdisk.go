// Package vdisk backs each server with its own ext4 image file mounted over a
// loop device, so that the kernel — not Wings — enforces the disk limit.
//
// Wings historically tracked disk usage in userland: every write that went
// through the Go code was counted, and a periodic walk of the server directory
// corrected the total. Anything the container itself wrote bypassed that
// accounting entirely, because /home/container is a plain bind mount of a host
// directory. A server could therefore fill the host far past its allocation
// and only be stopped at the next boot, when the usage check finally ran.
//
// Giving a server a filesystem of its own closes that hole: a write past the
// limit fails with ENOSPC inside the container, at the moment it happens,
// whether it came from an upload, an unpacked archive, or the game server
// writing its own world save. Usage becomes a statfs call rather than a walk
// over every file the server owns.
package vdisk

import (
	"context"
	"time"
)

// Metrics describes the space accounting of a mounted virtual disk, as
// reported by the kernel.
type Metrics struct {
	// Used is the number of bytes consumed by files on the disk.
	Used int64
	// Total is the usable capacity of the disk, which is the image size less
	// filesystem metadata.
	Total int64
	// Free is the number of bytes still writable.
	Free int64
	// InodesUsed and InodesTotal track inode exhaustion, which can strand a
	// server with apparently free space but no ability to create a file.
	InodesUsed  int64
	InodesTotal int64
}

// Options configures how images are created and mounted.
type Options struct {
	// Image is the path of the backing file, e.g. /var/lib/pelican/disks/<uuid>.img.
	Image string

	// MountPoint is where the filesystem is mounted, which is also the path
	// bind mounted into the container.
	MountPoint string

	// Size is the usable capacity the server has been allocated, in bytes.
	// The image created on disk is larger than this by OverheadPercent to
	// leave room for filesystem metadata, so that the server actually gets
	// the space it was sold.
	Size int64

	// OverheadPercent is how much larger than Size the image is made.
	OverheadPercent int

	// MountOptions are passed to mount(2) as the data argument.
	MountOptions string

	// UID and GID own the root of the mounted filesystem. A freshly formatted
	// ext4 filesystem is owned by root, which would leave the container unable
	// to write to its own directory.
	UID int
	GID int

	// Timeout bounds each external mkfs/resize2fs/e2fsck invocation.
	Timeout time.Duration
}

// Manager is the interface the rest of Wings uses to work with virtual disks.
// It is satisfied only on Linux; other platforms get an implementation whose
// methods report that virtual disks are unsupported.
type Manager interface {
	// Supported reports whether this host can host virtual disks at all,
	// returning the reason it cannot when it cannot.
	Supported() error

	// Ensure brings the disk described by opts into existence and mounts it,
	// creating and formatting the image if it is not there yet. It is safe to
	// call on a disk that is already mounted.
	Ensure(ctx context.Context, opts Options) error

	// Mounted reports whether a filesystem is currently mounted at mountPoint.
	Mounted(mountPoint string) (bool, error)

	// Unmount detaches the filesystem and releases its loop device.
	Unmount(ctx context.Context, mountPoint string) error

	// Resize changes the usable capacity of an existing disk. Growing happens
	// online; shrinking requires the disk to be unmounted and is refused when
	// the data already stored would not fit.
	Resize(ctx context.Context, opts Options) error

	// Usage reports space accounting for a mounted disk.
	Usage(mountPoint string) (Metrics, error)

	// Destroy unmounts the disk and deletes its image.
	Destroy(ctx context.Context, opts Options) error
}

const (
	// DefaultOverheadPercent is the headroom added to a server's allocation
	// when sizing its image. ext4 spends roughly 1.5-2% of a volume on the
	// inode table and journal, and running a filesystem completely full also
	// fragments it badly, so 5% buys back the allocation with a little slack.
	DefaultOverheadPercent = 5

	// DefaultMountOptions keeps the image sparse and denies the container the
	// ability to introduce device nodes or setuid binaries on a filesystem it
	// fully controls.
	//
	// discard matters more than it looks: without it a deleted file's blocks
	// stay allocated in the backing file, so an image only ever grows towards
	// its full size and the overcommit that makes virtual disks affordable
	// stops working.
	DefaultMountOptions = "noatime,nosuid,nodev,discard"

	// DefaultTimeout bounds mkfs and resize operations. Formatting a large
	// sparse image is fast, but e2fsck on a full multi-terabyte disk is not.
	DefaultTimeout = 30 * time.Minute

	// MinimumSize is the smallest allocation that can be given a disk of its
	// own. ext4 needs room for its metadata before it can store anything, and
	// a limit below this would produce a filesystem that is full when empty.
	MinimumSize = 32 << 20
)

// FixedOverhead is added to every image on top of OverheadPercent.
//
// ext4's cost is not purely proportional: the journal, superblock copies and
// group descriptors are largely fixed, so a percentage alone under-delivers
// badly on small disks. Measured on real images, total overhead runs at about
// 8 MiB plus ~2.5% of capacity — a 64 MiB disk loses 13% of its image to
// metadata where a 10 GiB disk loses only 2.6%.
//
// Over-sizing is close to free because the images are sparse: unused capacity
// occupies no blocks on the host until something writes to it. Under-sizing is
// not free, because it silently hands a server less disk than it was sold.
const FixedOverhead = 16 << 20

// SizeWithOverhead returns the on-disk image size for a given allocation.
//
// The result is deliberately generous. A server sold 10 GiB ends up able to
// store slightly more than 10 GiB rather than slightly less, because the exact
// metadata cost cannot be predicted before the filesystem is created.
func SizeWithOverhead(size int64, overheadPercent int) int64 {
	if overheadPercent <= 0 {
		overheadPercent = DefaultOverheadPercent
	}
	// Guard the multiplication rather than the result: a limit large enough to
	// overflow is nonsense, and silently wrapping to a tiny image would be far
	// worse than refusing to grow it.
	if size > (1<<62)/int64(100+overheadPercent) {
		return size
	}
	scaled := size * int64(100+overheadPercent) / 100
	if scaled > (1<<62)-FixedOverhead {
		return scaled
	}
	return scaled + FixedOverhead
}
