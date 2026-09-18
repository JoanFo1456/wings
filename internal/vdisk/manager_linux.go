//go:build linux

package vdisk

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"emperror.dev/errors"
	"github.com/apex/log"
	"golang.org/x/sys/unix"
)

// ErrUnsupported is returned by Supported when this host cannot back servers
// with virtual disks.
var ErrUnsupported = errors.Sentinel("vdisk: virtual disks are not supported on this host")

// ErrWouldNotFit is returned when a disk is asked to shrink below the space
// its contents already occupy.
var ErrWouldNotFit = errors.Sentinel("vdisk: existing data does not fit within the requested size")

// requiredTools are the e2fsprogs binaries the manager shells out to. There is
// no usable Go implementation of ext4 formatting or resizing, so their absence
// is a hard prerequisite rather than something to discover mid-operation.
var requiredTools = []string{"mkfs.ext4", "resize2fs", "e2fsck"}

type manager struct {
	// mu serialises operations that mutate the set of loop devices. The kernel
	// hands out a free device without reserving it, so two concurrent attaches
	// can be given the same one; attachLoop retries on that, but serialising
	// here keeps server boot from degenerating into a retry storm.
	mu sync.Mutex

	supportOnce sync.Once
	supportErr  error
}

// New returns a Manager backed by loop-mounted ext4 images.
func New() Manager {
	return &manager{}
}

func (m *manager) Supported() error {
	m.supportOnce.Do(func() {
		m.supportErr = m.checkSupport()
	})
	return m.supportErr
}

func (m *manager) checkSupport() error {
	for _, tool := range requiredTools {
		if _, err := exec.LookPath(tool); err != nil {
			return errors.WrapIff(ErrUnsupported, "%s is not installed; install e2fsprogs", tool)
		}
	}

	if err := ensureLoopControl(); err != nil {
		return errors.WrapIf(ErrUnsupported, err.Error())
	}

	// Allocating and immediately releasing a device proves the container has
	// both the device node and the privilege to drive it. Checking for
	// CAP_SYS_ADMIN instead would pass in an unprivileged LXC container that
	// holds the capability only within its own user namespace, where the loop
	// ioctls still fail.
	dev, err := freeLoopDevice()
	if err != nil {
		return errors.WrapIf(ErrUnsupported, err.Error())
	}
	f, err := os.OpenFile(dev, os.O_RDWR, 0)
	if err != nil {
		return errors.WrapIff(ErrUnsupported, "cannot open %s: %v", dev, err)
	}
	f.Close()
	return nil
}

// ensureLoopControl makes /dev/loop-control usable, creating the node when the
// kernel is not managing /dev.
//
// Containers frequently ship a /dev that was populated by the runtime rather
// than by devtmpfs, so the node is missing even though the loop driver is
// loaded on the host and the container is allowed to use it. Creating it is
// what lets this work inside LXC.
func ensureLoopControl() error {
	if _, err := os.Stat(loopControl); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return errors.WrapIff(err, "cannot stat %s", loopControl)
	}

	// 10:237 is the fixed misc-device number for loop-control.
	if err := unix.Mknod(loopControl, unix.S_IFCHR|0o600, int(unix.Mkdev(10, 237))); err != nil {
		if errors.Is(err, syscall.EEXIST) {
			return nil
		}
		return errors.WrapIff(err, "%s is missing and could not be created", loopControl)
	}
	return nil
}

func (m *manager) Ensure(ctx context.Context, opts Options) error {
	if err := m.Supported(); err != nil {
		return err
	}
	if err := validate(&opts); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if err := os.MkdirAll(opts.MountPoint, 0o755); err != nil {
		return errors.WrapIf(err, "vdisk: failed to create mount point")
	}

	mounted, err := m.mountedLocked(opts.MountPoint)
	if err != nil {
		return err
	}
	if mounted {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(opts.Image), 0o700); err != nil {
		return errors.WrapIf(err, "vdisk: failed to create image directory")
	}

	created, err := createImage(ctx, opts)
	if err != nil {
		return err
	}

	if err := m.mountLocked(opts); err != nil {
		if created {
			// A newly created image that cannot be mounted is useless and
			// would otherwise linger as an orphan consuming its full
			// allocation on the next sparse write.
			_ = os.Remove(opts.Image)
		}
		return err
	}

	removeLostFound(opts.MountPoint)

	if err := os.Chown(opts.MountPoint, opts.UID, opts.GID); err != nil {
		return errors.WrapIf(err, "vdisk: failed to set ownership of the mounted disk")
	}
	return nil
}

// removeLostFound deletes the lost+found directory mke2fs creates at the root
// of every ext4 filesystem, so it does not show up in the server's file
// manager as a directory the user did not create and cannot explain.
//
// It is only removed while empty. e2fsck puts recovered orphaned files there
// after a crash, and those are the one thing on the disk the operator most
// needs to see; deleting them to tidy up the listing would be indefensible.
// e2fsck recreates the directory by itself whenever it actually needs it, so
// removing an empty one costs nothing.
func removeLostFound(mountPoint string) {
	path := filepath.Join(mountPoint, "lost+found")

	entries, err := os.ReadDir(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.WithField("path", path).WithError(err).
				Debug("vdisk: could not inspect lost+found")
		}
		return
	}
	if len(entries) > 0 {
		log.WithFields(log.Fields{"path": path, "entries": len(entries)}).
			Warn("vdisk: lost+found holds recovered files and has been left in place")
		return
	}

	if err := os.Remove(path); err != nil {
		log.WithField("path", path).WithError(err).
			Debug("vdisk: could not remove empty lost+found")
	}
}

// createImage creates and formats the backing file when it does not already
// exist, reporting whether it did so.
func createImage(ctx context.Context, opts Options) (bool, error) {
	if st, err := os.Stat(opts.Image); err == nil {
		if st.Size() == 0 {
			// A zero-length image is the fingerprint of a crash between
			// creating the file and formatting it. Nothing can be lost by
			// starting over, and leaving it would fail to mount forever.
			if err := os.Remove(opts.Image); err != nil {
				return false, errors.WrapIf(err, "vdisk: failed to discard a truncated image")
			}
		} else {
			return false, nil
		}
	} else if !os.IsNotExist(err) {
		return false, errors.WrapIf(err, "vdisk: failed to stat image")
	}

	imageSize := SizeWithOverhead(opts.Size, opts.OverheadPercent)

	// Create the image at a temporary name and rename it into place only once
	// it carries a filesystem, so an interrupted format never leaves behind a
	// file that looks ready to mount.
	tmp := opts.Image + ".new"
	_ = os.Remove(tmp)

	f, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, errors.WrapIf(err, "vdisk: failed to create image")
	}
	// The file is sparse: it reports its full size but occupies only the
	// blocks actually written, which is what allows a node to allocate more
	// disk to servers than it physically has.
	if err := f.Truncate(imageSize); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return false, errors.WrapIf(err, "vdisk: failed to size image")
	}
	f.Close()

	if err := formatImage(ctx, tmp, opts); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}

	if err := os.Rename(tmp, opts.Image); err != nil {
		_ = os.Remove(tmp)
		return false, errors.WrapIf(err, "vdisk: failed to publish image")
	}

	log.WithFields(log.Fields{
		"image":      opts.Image,
		"size_bytes": imageSize,
	}).Debug("vdisk: created virtual disk image")
	return true, nil
}

// LargeImageThreshold is the size above which an image is given a sparser
// inode table. See formatImage.
const LargeImageThreshold = 64 << 30

// LargeImageBytesPerInode is the inode ratio used above LargeImageThreshold.
//
// mke2fs defaults to one inode per 32 KiB on large filesystems, which on a
// 370 GiB disk reserves roughly 2.9 GiB for the inode table alone — and the
// kernel zeroes that in the background after mount, so it becomes real blocks
// on the host rather than staying sparse. One inode per 256 KiB still allows
// well over a million files, which no game server approaches, and cuts the
// table to a few hundred megabytes.
const LargeImageBytesPerInode = 256 << 10

func formatImage(ctx context.Context, image string, opts Options) error {
	// -m 0 hands the reserved-blocks percentage back to the server: those
	// blocks exist to keep root able to recover a full system filesystem,
	// which has no meaning for a disk holding one server's files.
	//
	// -E nodiscard skips trimming a file that has never been written, which
	// otherwise dominates the time spent creating a large image.
	args := []string{"-q", "-m", "0", "-E", "nodiscard"}

	if SizeWithOverhead(opts.Size, opts.OverheadPercent) >= LargeImageThreshold {
		args = append(args, "-i", strconv.Itoa(LargeImageBytesPerInode))
	}

	args = append(args, "-F", image)
	return run(ctx, opts.Timeout, "mkfs.ext4", args...)
}

func (m *manager) mountLocked(opts Options) error {
	dev, err := findLoopByBackingFile(opts.Image)
	if err != nil {
		return err
	}
	attached := false
	if dev == "" {
		dev, err = attachLoop(opts.Image, false)
		if err != nil {
			return err
		}
		attached = true
	}

	mountOpts := opts.MountOptions
	if mountOpts == "" {
		mountOpts = DefaultMountOptions
	}
	flags, data := splitMountOptions(mountOpts)

	if err := unix.Mount(dev, opts.MountPoint, "ext4", flags, data); err != nil {
		if attached {
			_ = detachLoop(dev)
		}
		return errors.WrapIff(err, "vdisk: failed to mount %s at %s", dev, opts.MountPoint)
	}
	return nil
}

// splitMountOptions separates the options mount(2) takes as flags from those
// it takes as a filesystem-specific data string.
func splitMountOptions(opts string) (uintptr, string) {
	var flags uintptr
	var data []string
	for _, opt := range strings.Split(opts, ",") {
		switch strings.TrimSpace(opt) {
		case "":
		case "noatime":
			flags |= unix.MS_NOATIME
		case "nodiratime":
			flags |= unix.MS_NODIRATIME
		case "relatime":
			flags |= unix.MS_RELATIME
		case "nosuid":
			flags |= unix.MS_NOSUID
		case "nodev":
			flags |= unix.MS_NODEV
		case "noexec":
			flags |= unix.MS_NOEXEC
		case "sync":
			flags |= unix.MS_SYNCHRONOUS
		case "ro":
			flags |= unix.MS_RDONLY
		case "rw":
		default:
			data = append(data, opt)
		}
	}
	return flags, strings.Join(data, ",")
}

func (m *manager) Mounted(mountPoint string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mountedLocked(mountPoint)
}

func (m *manager) mountedLocked(mountPoint string) (bool, error) {
	src, err := mountSource(mountPoint)
	if err != nil {
		return false, err
	}
	return strings.HasPrefix(src, "/dev/loop"), nil
}

func (m *manager) Unmount(ctx context.Context, mountPoint string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.unmountLocked(mountPoint)
}

func (m *manager) unmountLocked(mountPoint string) error {
	// Resolve the device before unmounting: once the filesystem is gone the
	// mount table no longer says which loop device was backing it, and the
	// device would stay bound to its image forever.
	dev, err := mountSource(mountPoint)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(dev, "/dev/loop") {
		return nil
	}

	if err := unix.Unmount(mountPoint, 0); err != nil {
		if !errors.Is(err, syscall.EBUSY) {
			return errors.WrapIff(err, "vdisk: failed to unmount %s", mountPoint)
		}
		// Something still holds the filesystem — most often a container that
		// has not finished dying. Detach it from the tree and let the kernel
		// finish the teardown when the last reference goes away.
		log.WithField("mount_point", mountPoint).
			Warn("vdisk: disk is busy, falling back to a lazy unmount")
		if err := unix.Unmount(mountPoint, unix.MNT_DETACH); err != nil {
			return errors.WrapIff(err, "vdisk: failed to lazily unmount %s", mountPoint)
		}
		// The loop device cannot be released while the detached mount is still
		// referenced. Ask the kernel to drop it as soon as it can instead of
		// failing here.
		return autoclearLoop(dev)
	}

	return detachLoop(dev)
}

func (m *manager) Usage(mountPoint string) (Metrics, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(mountPoint, &st); err != nil {
		return Metrics{}, errors.WrapIff(err, "vdisk: failed to stat %s", mountPoint)
	}
	bs := int64(st.Bsize)
	return Metrics{
		// Blocks-Bfree rather than Blocks-Bavail: the difference between the
		// two is space reserved for root, which is not in use by anyone.
		Used:        int64(st.Blocks-st.Bfree) * bs,
		Total:       int64(st.Blocks) * bs,
		Free:        int64(st.Bavail) * bs,
		InodesUsed:  int64(st.Files - st.Ffree),
		InodesTotal: int64(st.Files),
	}, nil
}

func (m *manager) Resize(ctx context.Context, opts Options) error {
	if err := m.Supported(); err != nil {
		return err
	}
	if err := validate(&opts); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	st, err := os.Stat(opts.Image)
	if err != nil {
		if os.IsNotExist(err) {
			// Nothing to resize yet; the next Ensure creates it at the new
			// size.
			return nil
		}
		return errors.WrapIf(err, "vdisk: failed to stat image")
	}

	target := SizeWithOverhead(opts.Size, opts.OverheadPercent)
	switch {
	case target == st.Size():
		return nil
	case target > st.Size():
		return m.growLocked(ctx, opts, target)
	default:
		return m.shrinkLocked(ctx, opts, target)
	}
}

// growLocked expands a disk in place. The filesystem can absorb the new space
// while it is mounted, so a running server is never interrupted.
func (m *manager) growLocked(ctx context.Context, opts Options, target int64) error {
	f, err := os.OpenFile(opts.Image, os.O_RDWR, 0)
	if err != nil {
		return errors.WrapIf(err, "vdisk: failed to open image for growth")
	}
	if err := f.Truncate(target); err != nil {
		f.Close()
		return errors.WrapIf(err, "vdisk: failed to grow image")
	}
	f.Close()

	dev, err := findLoopByBackingFile(opts.Image)
	if err != nil {
		return err
	}
	if dev == "" {
		// Not attached, so there is no live filesystem to extend. The image is
		// already the right size and the next mount will pick it up.
		return nil
	}

	// Without this the loop device keeps advertising the old size and
	// resize2fs has nothing to grow into.
	if err := refreshLoopCapacity(dev); err != nil {
		return err
	}

	if err := run(ctx, opts.Timeout, "resize2fs", dev); err != nil {
		return errors.WrapIf(err, "vdisk: failed to grow filesystem")
	}

	log.WithFields(log.Fields{"image": opts.Image, "size_bytes": target}).
		Debug("vdisk: grew virtual disk")
	return nil
}

// shrinkLocked reduces a disk, which ext4 can only do offline. The filesystem
// is unmounted for the duration, so this must not be called while the server
// is running.
func (m *manager) shrinkLocked(ctx context.Context, opts Options, target int64) error {
	mounted, err := m.mountedLocked(opts.MountPoint)
	if err != nil {
		return err
	}

	if mounted {
		// Refuse before touching anything if the data would not fit. Letting
		// resize2fs discover this after the unmount would take the server
		// offline to accomplish nothing.
		usage, err := m.Usage(opts.MountPoint)
		if err != nil {
			return err
		}
		if usage.Used > opts.Size {
			return errors.WrapIff(ErrWouldNotFit, "%d bytes stored, %d requested", usage.Used, opts.Size)
		}
		if err := m.unmountLocked(opts.MountPoint); err != nil {
			return err
		}
	}

	dev, err := findLoopByBackingFile(opts.Image)
	if err != nil {
		return err
	}
	if dev == "" {
		if dev, err = attachLoop(opts.Image, false); err != nil {
			return err
		}
	}

	shrunk := false
	detached := false
	defer func() {
		if detached {
			return
		}
		if derr := detachLoop(dev); derr != nil {
			log.WithField("device", dev).WithError(derr).
				Warn("vdisk: failed to detach loop device after resize")
		}
		if !shrunk {
			return
		}
		// Only truncate the file once resize2fs has confirmed the filesystem
		// fits in the smaller space. Cutting the image first would destroy
		// whatever lived in the blocks beyond the new end.
		if terr := os.Truncate(opts.Image, target); terr != nil {
			log.WithField("image", opts.Image).WithError(terr).
				Error("vdisk: filesystem was shrunk but the image could not be truncated")
		}
	}()

	// resize2fs refuses to shrink a filesystem that has not been checked.
	// e2fsck reports 1 when it fixed something, which is a success for us.
	if err := run(ctx, opts.Timeout, "e2fsck", "-fp", dev); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() > 1 {
			return errors.WrapIf(err, "vdisk: filesystem check failed before shrinking")
		}
	}

	// resize2fs takes a size, not a byte count; kibibytes divide evenly into
	// every ext4 block size.
	size := strconv.FormatInt(target/1024, 10) + "K"
	if resizeErr := run(ctx, opts.Timeout, "resize2fs", dev, size); resizeErr != nil {
		// ext4 cannot shrink below a floor set by its own metadata, and that
		// floor scales with the size the filesystem was created at: a 370 GiB
		// disk will not go below roughly 2 GiB however empty it is. Moving an
		// "unlimited" server onto a small plan hits this every time.
		//
		// Rebuilding sidesteps it — a filesystem created at the target size has
		// metadata sized for the target size — at the cost of copying the data.
		// The data has already been checked to fit, so the copy is bounded by
		// the new allocation rather than the old one.
		log.WithFields(log.Fields{"image": opts.Image, "size_bytes": target}).
			WithError(resizeErr).
			Info("vdisk: filesystem cannot be shrunk in place, rebuilding it at the new size")

		if derr := detachLoop(dev); derr != nil {
			log.WithField("device", dev).WithError(derr).
				Warn("vdisk: failed to detach loop device before rebuild")
		}
		detached = true

		if err := m.rebuildLocked(ctx, opts, target); err != nil {
			return errors.WrapIf(err, "vdisk: failed to rebuild filesystem at the new size")
		}
		return nil
	}
	shrunk = true

	log.WithFields(log.Fields{"image": opts.Image, "size_bytes": target}).
		Debug("vdisk: shrank virtual disk")
	return nil
}

// rebuildLocked recreates a disk at target by formatting a fresh image and
// copying the old contents across, for shrinks resize2fs will not perform.
//
// The new image is only swapped in once every file has landed on it, so an
// interruption at any point leaves the original image untouched and the server
// recoverable.
func (m *manager) rebuildLocked(ctx context.Context, opts Options, target int64) error {
	dir := filepath.Dir(opts.Image)
	base := filepath.Base(opts.Image)
	oldMount := filepath.Join(dir, "."+base+".from")
	newMount := filepath.Join(dir, "."+base+".to")
	newImage := opts.Image + ".rebuild"

	for _, p := range []string{oldMount, newMount} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			return errors.WrapIf(err, "vdisk: failed to create a staging mount point")
		}
	}
	defer func() {
		_ = os.Remove(oldMount)
		_ = os.Remove(newMount)
	}()

	// Mount the existing disk read-only: nothing should be able to change it
	// while it is being copied, and it must survive untouched if this fails.
	if err := m.mountLocked(Options{Image: opts.Image, MountPoint: oldMount, MountOptions: "ro,noatime"}); err != nil {
		return errors.WrapIf(err, "vdisk: failed to mount the existing disk for rebuild")
	}
	defer func() {
		if err := m.unmountLocked(oldMount); err != nil {
			log.WithField("mount_point", oldMount).WithError(err).
				Warn("vdisk: failed to unmount the source disk after rebuild")
		}
	}()

	_ = os.Remove(newImage)
	fresh := opts
	fresh.Image = newImage
	if _, err := createImage(ctx, fresh); err != nil {
		return err
	}

	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(newImage)
		}
	}()

	if err := m.mountLocked(Options{Image: newImage, MountPoint: newMount, MountOptions: opts.MountOptions}); err != nil {
		return errors.WrapIf(err, "vdisk: failed to mount the replacement disk")
	}
	removeLostFound(newMount)

	if err := copyTree(ctx, oldMount, newMount); err != nil {
		_ = m.unmountLocked(newMount)
		return errors.WrapIf(err, "vdisk: failed to copy files onto the replacement disk")
	}

	if err := m.unmountLocked(newMount); err != nil {
		return errors.WrapIf(err, "vdisk: failed to unmount the replacement disk")
	}

	// The source has to be released before its image is replaced, or the
	// running mount would be left pointing at a file that no longer exists.
	if err := m.unmountLocked(oldMount); err != nil {
		return errors.WrapIf(err, "vdisk: failed to unmount the source disk")
	}

	if err := os.Rename(newImage, opts.Image); err != nil {
		return errors.WrapIf(err, "vdisk: failed to swap in the rebuilt image")
	}
	committed = true

	log.WithFields(log.Fields{"image": opts.Image, "size_bytes": target}).
		Debug("vdisk: rebuilt virtual disk at a smaller size")
	return nil
}

func (m *manager) Destroy(ctx context.Context, opts Options) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.unmountLocked(opts.MountPoint); err != nil {
		// Deleting the image out from under a live mount would strand the
		// loop device and leave the space unreclaimed until reboot.
		return err
	}
	if err := os.Remove(opts.Image); err != nil && !os.IsNotExist(err) {
		return errors.WrapIf(err, "vdisk: failed to remove image")
	}
	// The mount point is an empty directory once the disk is gone.
	if err := os.Remove(opts.MountPoint); err != nil && !os.IsNotExist(err) {
		log.WithField("mount_point", opts.MountPoint).WithError(err).
			Debug("vdisk: mount point could not be removed")
	}
	return nil
}

// autoclearLoop marks a device to be released as soon as its last user goes
// away, which is the only option when a lazy unmount is still in flight.
func autoclearLoop(dev string) error {
	f, err := os.OpenFile(dev, os.O_RDWR, 0)
	if err != nil {
		return errors.WrapIff(err, "vdisk: failed to open loop device %s", dev)
	}
	defer f.Close()

	info, err := unix.IoctlLoopGetStatus64(int(f.Fd()))
	if err != nil {
		if errors.Is(err, syscall.ENXIO) {
			return nil
		}
		return errors.WrapIff(err, "vdisk: failed to read status of %s", dev)
	}
	info.Flags |= unix.LO_FLAGS_AUTOCLEAR
	if err := unix.IoctlLoopSetStatus64(int(f.Fd()), info); err != nil {
		return errors.WrapIff(err, "vdisk: failed to mark %s for release", dev)
	}
	return nil
}

func validate(opts *Options) error {
	if opts.Image == "" || opts.MountPoint == "" {
		return errors.New("vdisk: image and mount point are both required")
	}
	if opts.Size < MinimumSize {
		return errors.Errorf("vdisk: a disk must be at least %d bytes, got %d", MinimumSize, opts.Size)
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.OverheadPercent <= 0 {
		opts.OverheadPercent = DefaultOverheadPercent
	}
	return nil
}

// run executes an external filesystem tool, folding its output into the
// returned error so a failure is diagnosable from the Wings log alone.
func run(ctx context.Context, timeout time.Duration, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return err
		}
		return errors.WithMessagef(err, "%s: %s", name, msg)
	}
	return nil
}
