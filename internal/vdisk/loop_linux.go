//go:build linux

package vdisk

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"emperror.dev/errors"
	"golang.org/x/sys/unix"
)

// loopControl is the kernel's allocator for loop devices. Opening it and
// issuing LOOP_CTL_GET_FREE both finds a free device and creates its device
// node if the kernel is managing /dev (devtmpfs).
const loopControl = "/dev/loop-control"

// attachLoop binds image to a free loop device and returns the device path.
//
// The device is *not* marked LO_FLAGS_AUTOCLEAR: we want the binding to
// survive the closing of our file descriptors so that the mount holds the
// device on its own. Detaching is done explicitly in detachLoop, which runs
// after the filesystem has been unmounted.
func attachLoop(image string, readOnly bool) (string, error) {
	flag := os.O_RDWR
	if readOnly {
		flag = os.O_RDONLY
	}
	backing, err := os.OpenFile(image, flag, 0)
	if err != nil {
		return "", errors.WrapIf(err, "vdisk: failed to open backing image")
	}
	defer backing.Close()

	// The kernel can hand out a device that another process claims before we
	// get to LOOP_CONFIGURE. That race is expected and reported as EBUSY, so
	// retry rather than failing the server boot.
	var lastErr error
	for attempt := 0; attempt < loopAttachRetries; attempt++ {
		dev, err := freeLoopDevice()
		if err != nil {
			return "", err
		}

		devFile, err := os.OpenFile(dev, os.O_RDWR, 0)
		if err != nil {
			// The node may have been removed between the ioctl and the open.
			lastErr = err
			continue
		}

		err = configureLoop(int(devFile.Fd()), backing.Fd(), image, readOnly)
		devFile.Close()
		if err == nil {
			return dev, nil
		}
		if !errors.Is(err, syscall.EBUSY) {
			return "", errors.WrapIff(err, "vdisk: failed to configure loop device %s", dev)
		}
		lastErr = err
	}
	return "", errors.WrapIf(lastErr, "vdisk: no loop device could be claimed")
}

// loopAttachRetries bounds how many times we re-ask the kernel for a free
// device when another process wins the race for the one we were given.
const loopAttachRetries = 8

// configureLoop binds a backing file descriptor to an open loop device.
//
// LOOP_CONFIGURE does this in one step but only exists from Linux 5.8. Older
// kernels — 5.4 is still the kernel on Ubuntu 20.04 — need the backing file and
// its metadata set separately, so fall back to that when the single-step ioctl
// is not recognised.
func configureLoop(devFd int, backingFd uintptr, image string, readOnly bool) error {
	var info unix.LoopInfo64
	copy(info.File_name[:], image)
	if readOnly {
		info.Flags |= unix.LO_FLAGS_READ_ONLY
	}

	// LoopConfig.Size is the logical block size, despite the name; a zero
	// leaves the kernel to pick one that suits the backing file.
	cfg := unix.LoopConfig{Fd: uint32(backingFd), Info: info}
	err := unix.IoctlLoopConfigure(devFd, &cfg)
	if err == nil {
		return nil
	}
	if !errors.Is(err, syscall.ENOTTY) && !errors.Is(err, syscall.EINVAL) {
		return err
	}

	if err := unix.IoctlSetInt(devFd, unix.LOOP_SET_FD, int(backingFd)); err != nil {
		return err
	}
	if err := unix.IoctlLoopSetStatus64(devFd, &info); err != nil {
		// Leaving the device bound but unnamed would make it invisible to
		// findLoopByBackingFile, so it could never be reused or released.
		_ = unix.IoctlSetInt(devFd, unix.LOOP_CLR_FD, 0)
		return err
	}
	return nil
}

// freeLoopDevice asks the kernel for an unused loop device, creating the node
// if necessary, and returns its path.
func freeLoopDevice() (string, error) {
	ctrl, err := os.OpenFile(loopControl, os.O_RDWR, 0)
	if err != nil {
		return "", errors.WrapIff(err, "vdisk: %s is unavailable", loopControl)
	}
	defer ctrl.Close()

	n, err := unix.IoctlRetInt(int(ctrl.Fd()), unix.LOOP_CTL_GET_FREE)
	if err != nil {
		return "", errors.WrapIf(err, "vdisk: kernel refused to allocate a loop device")
	}

	dev := fmt.Sprintf("/dev/loop%d", n)
	if err := ensureLoopNode(dev, n); err != nil {
		return "", err
	}
	return dev, nil
}

// ensureLoopNode creates the device node for a loop device the kernel has just
// allocated, when nothing else has.
//
// On a host with devtmpfs the node appears by itself. Inside a container whose
// /dev was populated by the runtime it does not, even though the device exists
// and the container is allowed to drive it — so creating it here is what makes
// virtual disks usable in that setting.
func ensureLoopNode(dev string, n int) error {
	if _, err := os.Stat(dev); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return errors.WrapIff(err, "vdisk: cannot stat %s", dev)
	}

	// Loop devices are block devices with major number 7.
	if err := unix.Mknod(dev, unix.S_IFBLK|0o600, int(unix.Mkdev(7, uint32(n)))); err != nil {
		if errors.Is(err, syscall.EEXIST) {
			return nil
		}
		return errors.WrapIff(err, "vdisk: %s is missing and could not be created", dev)
	}
	return nil
}

// detachLoop releases the backing file held by a loop device.
//
// A device that is already free reports ENXIO, which is treated as success so
// that teardown is idempotent.
func detachLoop(dev string) error {
	f, err := os.OpenFile(dev, os.O_RDWR, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return errors.WrapIff(err, "vdisk: failed to open loop device %s", dev)
	}
	defer f.Close()

	if err := unix.IoctlSetInt(int(f.Fd()), unix.LOOP_CLR_FD, 0); err != nil {
		if errors.Is(err, syscall.ENXIO) {
			return nil
		}
		return errors.WrapIff(err, "vdisk: failed to detach loop device %s", dev)
	}
	return nil
}

// refreshLoopCapacity tells the kernel to re-read the size of the backing
// file. It is required after the image has been grown on disk, otherwise the
// loop device keeps reporting the old size and resize2fs has nowhere to grow
// into.
func refreshLoopCapacity(dev string) error {
	f, err := os.OpenFile(dev, os.O_RDWR, 0)
	if err != nil {
		return errors.WrapIff(err, "vdisk: failed to open loop device %s", dev)
	}
	defer f.Close()

	if err := unix.IoctlSetInt(int(f.Fd()), unix.LOOP_SET_CAPACITY, 0); err != nil {
		return errors.WrapIff(err, "vdisk: failed to refresh capacity of %s", dev)
	}
	return nil
}

// findLoopByBackingFile returns the loop device currently backed by image, or
// an empty string when the image is not attached to anything.
//
// The sysfs backing_file attribute is authoritative here. It is compared
// against the resolved image path because the kernel stores the path as it was
// at attach time, and a value with the " (deleted)" suffix means the image was
// replaced underneath a still-running server.
func findLoopByBackingFile(image string) (string, error) {
	resolved, err := filepath.EvalSymlinks(image)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}

	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return "", errors.WrapIf(err, "vdisk: failed to enumerate block devices")
	}

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "loop") {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/sys/block", name, "loop", "backing_file"))
		if err != nil {
			// Not every loopN has a backing file; an unbound device has no
			// "loop" directory at all.
			continue
		}
		backing := strings.TrimSpace(string(b))
		backing = strings.TrimSuffix(backing, " (deleted)")
		if backing == resolved {
			return filepath.Join("/dev", name), nil
		}
	}
	return "", nil
}

// mountSource returns the device backing the mount at target, or an empty
// string when target is not a mount point in its own right.
//
// Reading mountinfo rather than comparing st_dev with the parent directory
// matters because a bind mount of the same filesystem would compare equal.
func mountSource(target string) (string, error) {
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}

	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return "", errors.WrapIf(err, "vdisk: failed to read mountinfo")
	}
	defer f.Close()

	// The last matching entry wins: mountinfo lists stacked mounts in order,
	// and the most recent one is what is actually visible at the path.
	var source string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		// Layout: id parent major:minor root mountpoint opts... - fstype source superopts
		if len(fields) < 10 {
			continue
		}
		if unescapeMountField(fields[4]) != resolved {
			continue
		}
		sep := -1
		for i := 6; i < len(fields); i++ {
			if fields[i] == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || sep+2 >= len(fields) {
			continue
		}
		source = unescapeMountField(fields[sep+2])
	}
	if err := scanner.Err(); err != nil {
		return "", errors.WrapIf(err, "vdisk: failed to scan mountinfo")
	}
	return source, nil
}

// unescapeMountField decodes the octal escapes the kernel uses in mountinfo
// for space, tab, newline and backslash.
func unescapeMountField(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
