package server

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"emperror.dev/errors"

	"github.com/pelican/wings/config"
	"github.com/pelican/wings/environment"
	"github.com/pelican/wings/internal/vdisk"
	"github.com/pelican/wings/server/filesystem"
)

var (
	virtualDisksOnce sync.Once
	virtualDisks     vdisk.Manager
)

// VirtualDisks returns the process-wide virtual disk manager.
func VirtualDisks() vdisk.Manager {
	virtualDisksOnce.Do(func() {
		virtualDisks = vdisk.New()
	})
	return virtualDisks
}

// virtualDisksEnabled reports whether servers on this node are backed by a disk
// of their own.
//
// This is answered entirely by the host. There is no setting: a node that can
// mount loop-backed ext4 uses virtual disks, and one that cannot keeps plain
// directories. Making it configurable would only allow a node to be told to do
// something its kernel will refuse, and the failure would show up much later
// as servers that will not start.
func virtualDisksEnabled() bool {
	return VirtualDisks().Supported() == nil
}

// virtualDiskImage returns the path of a server's backing image.
func virtualDiskImage(uuid string) string {
	return filepath.Join(config.Get().System.VirtualDisks.Directory, uuid+".img")
}

// virtualDiskSize returns the capacity this server's disk should have.
//
// A server with an unlimited allocation still gets a disk, sized to the
// filesystem less a reserve. Leaving it on a plain directory instead would mean
// that giving it a real limit later did nothing, because the data would already
// be outside any disk — and an unenforced limit is the exact failure this
// feature exists to prevent. With a disk from the start, applying a limit is
// just a resize.
func (s *Server) virtualDiskSize() (int64, error) {
	if size := s.DiskSpace(); size > 0 {
		return size, nil
	}

	// Measured from the machine, not configured, so the disk reflects the real
	// volume and follows it if the node's storage is later grown.
	total, err := filesystemSize(config.Get().System.Data)
	if err != nil {
		return 0, err
	}

	reserve := config.Get().System.VirtualDisks.UnlimitedReserveMb << 20
	if reserve >= total {
		// A reserve larger than the volume would produce a negative size.
		// Refusing is better than silently handing out a tiny disk.
		return 0, errors.Errorf(
			"server: unlimited_reserve_mb (%d MiB) leaves no room on a %d byte filesystem",
			config.Get().System.VirtualDisks.UnlimitedReserveMb, total,
		)
	}
	return total - reserve, nil
}

// virtualDiskOptions describes this server's disk to the vdisk package.
func (s *Server) virtualDiskOptions(size int64) vdisk.Options {
	cfg := config.Get().System
	return vdisk.Options{
		Image:           virtualDiskImage(s.ID()),
		MountPoint:      filepath.Join(cfg.Data, s.ID()),
		Size:            size,
		OverheadPercent: cfg.VirtualDisks.OverheadPercent,
		MountOptions:    cfg.VirtualDisks.MountOptions,
		UID:             cfg.User.Uid,
		GID:             cfg.User.Gid,
		Timeout:         time.Duration(cfg.VirtualDisks.OperationTimeout) * time.Second,
	}
}

// HasVirtualDiskImage reports whether a backing image exists for this server,
// whether or not it is currently mounted.
//
// This is the difference between "this server has never had a disk" and "this
// server has a disk that is not working", which are handled very differently:
// the first can safely fall back to a plain directory, the second must not.
func (s *Server) HasVirtualDiskImage() bool {
	_, err := os.Stat(virtualDiskImage(s.ID()))
	return err == nil
}

// UsesVirtualDisk reports whether this server's data directory is currently a
// mounted virtual disk.
func (s *Server) UsesVirtualDisk() bool {
	if !virtualDisksEnabled() {
		return false
	}
	mounted, err := VirtualDisks().Mounted(filepath.Join(config.Get().System.Data, s.ID()))
	if err != nil {
		s.Log().WithField("error", err).Warn("failed to determine whether the server is on a virtual disk")
		return false
	}
	return mounted
}

// EnsureVirtualDisk creates and mounts this server's disk if virtual disks are
// enabled, migrating an existing plain data directory into it when configured
// to do so.
//
// A server whose allocation is unlimited or too small to hold a filesystem
// keeps a plain directory: there is no size for the kernel to enforce, and an
// image below the ext4 minimum would be full the moment it was created.
func (s *Server) EnsureVirtualDisk(ctx context.Context) error {
	if !virtualDisksEnabled() {
		return nil
	}
	size, err := s.virtualDiskSize()
	if err != nil {
		return err
	}
	if size < vdisk.MinimumSize {
		// Too small to hold a filesystem at all; an image this size would be
		// full the moment it was created.
		s.Log().WithField("bytes", size).
			Info("disk allocation is below the minimum for a virtual disk; leaving the server on a plain directory")
		return nil
	}

	opts := s.virtualDiskOptions(size)

	mounted, err := VirtualDisks().Mounted(opts.MountPoint)
	if err != nil {
		return err
	}
	if mounted {
		return nil
	}

	// Decide about migration before the mount exists: once the disk is mounted
	// over the directory, whatever was underneath is invisible and would look
	// like an empty server.
	migrate, err := needsMigration(opts.MountPoint, opts.Image)
	if err != nil {
		return err
	}
	if migrate && !config.Get().System.VirtualDisks.MigrateExisting {
		s.Log().Warn("server has existing files and migrate_existing is disabled; leaving it on a plain directory")
		return nil
	}

	var staged string
	if migrate {
		// Move the existing tree aside so the empty mount point is free, then
		// copy it back in once the disk is mounted. A rename within the data
		// directory is atomic and instant; the copy afterwards is not, which
		// is why this is opt-in.
		staged = opts.MountPoint + ".premigration"
		if err := os.Rename(opts.MountPoint, staged); err != nil {
			return errors.WrapIf(err, "server: failed to stage existing data for migration")
		}
	}

	if err := VirtualDisks().Ensure(ctx, opts); err != nil {
		if staged != "" {
			// Put the data back where Wings expects to find it, otherwise the
			// server comes up empty and the operator has to know about the
			// staging directory to recover.
			if rerr := os.Rename(staged, opts.MountPoint); rerr != nil {
				s.Log().WithField("staged_path", staged).WithField("error", rerr).
					Error("failed to restore server data after the virtual disk could not be created")
			}
		}
		return err
	}

	if staged != "" {
		if err := s.migrateIntoVirtualDisk(ctx, staged, opts); err != nil {
			return err
		}
	}

	s.Log().WithField("image", opts.Image).Debug("server is backed by a virtual disk")
	return nil
}

// needsMigration reports whether a plain data directory holds files that would
// be hidden by mounting a disk over it.
func needsMigration(mountPoint, image string) (bool, error) {
	if _, err := os.Stat(image); err == nil {
		// The disk already exists, so the directory is just its mount point.
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, errors.WrapIf(err, "server: failed to stat virtual disk image")
	}

	entries, err := os.ReadDir(mountPoint)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, errors.WrapIf(err, "server: failed to read the server data directory")
	}
	return len(entries) > 0, nil
}

// migrateIntoVirtualDisk copies a staged data directory onto the freshly
// mounted disk and removes the staging copy once every file has landed.
//
// The staged tree is only deleted after a clean copy, so a failure part way
// through leaves the original data intact and the server recoverable by hand.
func (s *Server) migrateIntoVirtualDisk(ctx context.Context, staged string, opts vdisk.Options) error {
	s.Log().WithField("staged_path", staged).Info("migrating existing server files onto a virtual disk")

	if err := vdisk.CopyTree(ctx, staged, opts.MountPoint); err != nil {
		return errors.WrapIf(err, "server: failed to copy server files onto the virtual disk")
	}
	if err := os.RemoveAll(staged); err != nil {
		s.Log().WithField("staged_path", staged).WithField("error", err).
			Warn("server files were migrated but the staging directory could not be removed")
	}

	s.Log().Info("finished migrating server files onto a virtual disk")
	return nil
}

// ResizeVirtualDisk brings a server's disk in line with its current
// allocation. Growing is online; shrinking needs the server stopped, so a
// shrink on a running server is reported and deferred rather than forced.
func (s *Server) ResizeVirtualDisk(ctx context.Context) error {
	if !virtualDisksEnabled() {
		return nil
	}

	size, err := s.virtualDiskSize()
	if err != nil {
		return err
	}
	if size < vdisk.MinimumSize {
		return nil
	}
	opts := s.virtualDiskOptions(size)

	st, err := os.Stat(opts.Image)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return errors.WrapIf(err, "server: failed to stat virtual disk image")
	}

	shrinking := vdisk.SizeWithOverhead(opts.Size, opts.OverheadPercent) < st.Size()
	if shrinking && s.Environment != nil && s.Environment.State() != environment.ProcessOfflineState {
		// ext4 cannot shrink a mounted filesystem, and taking a running server
		// offline to apply a panel-side change would be a worse surprise than
		// applying it late.
		s.Log().Info("virtual disk cannot be shrunk while the server is running; it will be resized when the server next stops")
		return nil
	}

	if err := VirtualDisks().Resize(ctx, opts); err != nil {
		if errors.Is(err, vdisk.ErrWouldNotFit) {
			// Reported, not fatal: the server keeps its larger disk and the
			// operator has to free space before the new limit can apply.
			s.Log().WithField("error", err).Warn("virtual disk could not be shrunk to the new limit")
			return nil
		}
		return err
	}

	if !shrinking {
		// Growing happens in place, so the mount and the descriptors Wings
		// holds on it are both still valid. The capacity changed though, so the
		// userland limit has to follow it.
		s.ApplyDiskLimit()
		return nil
	}

	// Shrinking unmounts the disk, so it has to be brought back and the
	// filesystem reopened against the new mount.
	if err := s.EnsureVirtualDisk(ctx); err != nil {
		return err
	}
	return s.rebuildFilesystem()
}

// DestroyVirtualDisk unmounts a server's disk and deletes its image.
func (s *Server) DestroyVirtualDisk(ctx context.Context) error {
	// Not gated on host support: an image left behind by a node that has since
	// lost its loop devices still holds its blocks, and deleting the server is
	// the only moment anything would clean it up.
	//
	// Capacity is irrelevant when tearing a disk down; only the image path and
	// mount point are used.
	opts := s.virtualDiskOptions(0)
	if _, err := os.Stat(opts.Image); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return VirtualDisks().Destroy(ctx, opts)
}

// EnsureVirtualDiskMounted makes sure a server that is about to start has its
// disk mounted, and reports whether it is backed by one.
//
// EnsureVirtualDisk alone is not enough here. If the disk is not mounted, the
// server's filesystem was opened against the bare mount point, so the handle
// has to be reopened once the disk is back or every path Wings resolves would
// land on the host volume underneath it.
func (s *Server) EnsureVirtualDiskMounted(ctx context.Context) (bool, error) {
	if !virtualDisksEnabled() {
		return false, nil
	}

	// Only the image path and mount point matter here; EnsureVirtualDisk
	// resolves the real capacity if it has to bring the disk back.
	opts := s.virtualDiskOptions(0)
	if _, err := os.Stat(opts.Image); err != nil {
		if os.IsNotExist(err) {
			// No image, so this server still lives in a plain directory.
			return false, nil
		}
		return false, errors.WrapIf(err, "server: failed to stat virtual disk image")
	}

	mounted, err := VirtualDisks().Mounted(opts.MountPoint)
	if err != nil {
		return false, err
	}
	if mounted {
		return true, nil
	}

	s.Log().Warn("virtual disk was not mounted; remounting it before the server starts")
	if err := s.EnsureVirtualDisk(ctx); err != nil {
		return false, err
	}
	if err := s.rebuildFilesystem(); err != nil {
		return false, err
	}
	return true, nil
}

// Virtual disks are deliberately left mounted when Wings exits.
//
// A mount is kernel state that outlives the process, and Wings does not stop
// containers when it shuts down — servers are expected to keep running across
// a restart or an upgrade. Unmounting on exit would pull the filesystem out
// from under every running container for no benefit, since EnsureVirtualDisk
// treats an already-mounted disk as nothing to do, and a host reboot clears
// the mounts anyway.

// useVirtualDiskUsage points the server's filesystem at the kernel for disk
// usage when it is backed by a virtual disk.
//
// This is what removes the periodic walk over every file a server owns. The
// walk is the reason disk usage was only ever a stale approximation, and on a
// server with a large number of files it is also the single most expensive
// thing Wings does on a timer.
func (s *Server) useVirtualDiskUsage() {
	fs := s.Filesystem()
	if fs == nil {
		return
	}
	if !s.UsesVirtualDisk() {
		fs.SetUsageProvider(nil)
		s.ApplyDiskLimit()
		return
	}

	mountPoint := filepath.Join(config.Get().System.Data, s.ID())
	fs.SetUsageProvider(func() (int64, error) {
		m, err := VirtualDisks().Usage(mountPoint)
		if err != nil {
			return 0, err
		}
		return m.Used, nil
	})
	s.ApplyDiskLimit()
}

// ApplyDiskLimit sets the limit Wings enforces in userland, which is not always
// the limit the Panel configured.
//
// A server on a virtual disk is bounded by the size of that disk whatever the
// Panel says. That matters most for an "unlimited" server: its Panel limit is
// zero, which every userland check reads as "no ceiling", so uploads and SFTP
// writes would sail past every check and only stop when the kernel returned
// ENOSPC part-way through a transfer. Reporting the disk's real capacity means
// those checks reject the write up front, with a message that says the server
// is out of space rather than an I/O error mid-upload.
func (s *Server) ApplyDiskLimit() {
	fs := s.Filesystem()
	if fs == nil {
		return
	}

	onVirtualDisk := s.UsesVirtualDisk()

	// The kernel enforces the limit on a virtual disk, so Wings stops
	// rejecting individual writes and lets ENOSPC come from the filesystem.
	fs.SetKernelEnforced(onVirtualDisk)

	limit := s.DiskSpace()
	if onVirtualDisk {
		m, err := VirtualDisks().Usage(filepath.Join(config.Get().System.Data, s.ID()))
		switch {
		case err != nil:
			s.Log().WithField("error", err).
				Warn("failed to read virtual disk capacity; falling back to the configured disk limit")
		case limit <= 0 || m.Total < limit:
			// Either the Panel says unlimited, or the disk is somehow smaller
			// than the allocation. The disk wins: it is what physically bounds
			// the server.
			limit = m.Total
		}
	}
	fs.SetDiskLimit(limit)
}

// rebuildFilesystem reopens the server's filesystem against its data
// directory.
//
// ufs keeps an open descriptor on the directory it was created with. After a
// disk is unmounted and mounted again — which is what shrinking one involves —
// that descriptor still refers to the old, now-hidden filesystem, so every
// path Wings resolves would land on the wrong disk.
func (s *Server) rebuildFilesystem() error {
	old := s.Filesystem()
	fs, err := filesystem.New(
		filepath.Join(config.Get().System.Data, s.ID()),
		s.DiskSpace(),
		s.Config().Egg.FileDenylist,
	)
	if err != nil {
		return errors.WrapIf(err, "server: failed to reopen the server filesystem")
	}

	s.fs.Store(fs)
	s.useVirtualDiskUsage()

	if old != nil {
		if err := old.UnixFS().Close(); err != nil {
			s.Log().WithField("error", err).Debug("failed to close the previous filesystem handle")
		}
	}
	return nil
}
