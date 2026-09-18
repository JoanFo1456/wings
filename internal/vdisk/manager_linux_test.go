//go:build linux

package vdisk

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestSplitMountOptionsSeparatesFlagsFromData(t *testing.T) {
	flags, data := splitMountOptions(DefaultMountOptions)

	for _, tc := range []struct {
		name string
		flag uintptr
	}{
		{"noatime", unix.MS_NOATIME},
		{"nosuid", unix.MS_NOSUID},
		{"nodev", unix.MS_NODEV},
	} {
		if flags&tc.flag == 0 {
			t.Errorf("%s was not translated into a mount flag", tc.name)
		}
	}

	// discard is an ext4 option rather than a mount(2) flag, so it has to
	// survive into the data string or images would never release the blocks
	// behind deleted files.
	if data != "discard" {
		t.Errorf("data = %q, want %q", data, "discard")
	}
}

func TestSplitMountOptionsIgnoresEmptyEntries(t *testing.T) {
	flags, data := splitMountOptions(",,rw, noatime ,,")
	if flags&unix.MS_NOATIME == 0 {
		t.Error("a padded option was not recognised")
	}
	// rw is the absence of MS_RDONLY, not an option to pass through.
	if data != "" {
		t.Errorf("data = %q, want empty", data)
	}
}

func TestSplitMountOptionsHandlesReadOnly(t *testing.T) {
	flags, _ := splitMountOptions("ro,noexec")
	if flags&unix.MS_RDONLY == 0 {
		t.Error("ro was not translated into MS_RDONLY")
	}
	if flags&unix.MS_NOEXEC == 0 {
		t.Error("noexec was not translated into MS_NOEXEC")
	}
}

func TestUnescapeMountField(t *testing.T) {
	// The kernel octal-escapes these four characters in mountinfo. A path
	// containing a space is the realistic case, and failing to decode it would
	// make a mounted disk look unmounted.
	for _, tc := range []struct{ in, want string }{
		{"/var/lib/pelican/volumes/abc", "/var/lib/pelican/volumes/abc"},
		{`/mnt/my\040disk`, "/mnt/my disk"},
		{`/mnt/tab\011here`, "/mnt/tab\there"},
		{`/mnt/back\134slash`, `/mnt/back\slash`},
		{`/mnt/trailing\`, `/mnt/trailing\`},
	} {
		if got := unescapeMountField(tc.in); got != tc.want {
			t.Errorf("unescapeMountField(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestValidateRejectsUndersizedDisks(t *testing.T) {
	opts := Options{Image: "/tmp/a.img", MountPoint: "/tmp/a", Size: MinimumSize - 1}
	if err := validate(&opts); err == nil {
		t.Fatal("expected a disk below the ext4 minimum to be rejected")
	}
}

func TestValidateRequiresPaths(t *testing.T) {
	for _, opts := range []Options{
		{MountPoint: "/tmp/a", Size: MinimumSize},
		{Image: "/tmp/a.img", Size: MinimumSize},
	} {
		if err := validate(&opts); err == nil {
			t.Errorf("expected %+v to be rejected", opts)
		}
	}
}

func TestValidateFillsDefaults(t *testing.T) {
	opts := Options{Image: "/tmp/a.img", MountPoint: "/tmp/a", Size: MinimumSize}
	if err := validate(&opts); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if opts.Timeout != DefaultTimeout {
		t.Errorf("Timeout = %v, want %v", opts.Timeout, DefaultTimeout)
	}
	if opts.OverheadPercent != DefaultOverheadPercent {
		t.Errorf("OverheadPercent = %d, want %d", opts.OverheadPercent, DefaultOverheadPercent)
	}
}

func TestMountSourceReportsNothingForAPlainDirectory(t *testing.T) {
	// A directory that is not a mount point in its own right must not be
	// mistaken for one, otherwise Ensure would skip creating the disk.
	src, err := mountSource(t.TempDir())
	if err != nil {
		t.Fatalf("mountSource: %v", err)
	}
	if src != "" {
		t.Errorf("mountSource of a plain directory = %q, want empty", src)
	}
}
