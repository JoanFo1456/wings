package filesystem

import "testing"

// TestKernelEnforcedStopsPerWriteRejection checks that a filesystem whose limit
// the kernel applies does not also reject writes in userland.
//
// Rejecting in both places is not merely redundant: it replaces the operating
// system's ENOSPC with an error Wings invented, and SFTP clients handle a real
// ENOSPC far better than a synthetic one.
func TestKernelEnforcedStopsPerWriteRejection(t *testing.T) {
	fs, _ := NewFs()

	fs.SetDiskLimit(1024)
	fs.unixFS.SetUsage(1024)

	// Without kernel enforcement the write is refused here, before it reaches
	// the filesystem at all.
	if err := fs.reserveDisk(512); err == nil {
		t.Fatal("expected a userland rejection when the kernel is not enforcing the limit")
	}

	fs.SetKernelEnforced(true)
	if err := fs.reserveDisk(512); err != nil {
		t.Fatalf("expected the write to be allowed through to the kernel, got %v", err)
	}
}

// TestKernelEnforcedKeepsPreTransferChecks checks that marking a filesystem as
// kernel-enforced does not disarm the checks made before a transfer starts.
//
// Those are what let an upload be refused up front rather than part way
// through, which is the whole reason they exist; only the per-write rejection
// is redundant once the kernel is enforcing.
func TestKernelEnforcedKeepsPreTransferChecks(t *testing.T) {
	fs, _ := NewFs()

	fs.SetDiskLimit(1024)
	fs.unixFS.SetUsage(0)
	fs.SetKernelEnforced(true)

	if err := fs.HasSpaceFor(4096); err == nil {
		t.Fatal("HasSpaceFor should still refuse a transfer that cannot fit")
	} else if !IsErrorCode(err, ErrCodeDiskSpace) {
		t.Fatalf("expected a disk space error, got %v", err)
	}

	if free := fs.AvailableSpace(); free != 1024 {
		t.Errorf("AvailableSpace = %d, want 1024", free)
	}
}

// TestAvailableSpaceReportsUnlimited checks the sentinel used by callers that
// need to know there is no ceiling to budget against.
func TestAvailableSpaceReportsUnlimited(t *testing.T) {
	fs, _ := NewFs()

	fs.SetDiskLimit(0)
	if free := fs.AvailableSpace(); free != -1 {
		t.Errorf("AvailableSpace = %d, want -1 for an unlimited filesystem", free)
	}
}

// TestAvailableSpaceClampsWhenOverLimit checks that a filesystem already past
// its limit reports no room rather than a negative amount, which would be read
// as "unlimited" by callers checking for -1.
func TestAvailableSpaceClampsWhenOverLimit(t *testing.T) {
	fs, _ := NewFs()

	fs.SetDiskLimit(1024)
	fs.unixFS.SetUsage(4096)

	if free := fs.AvailableSpace(); free != 0 {
		t.Errorf("AvailableSpace = %d, want 0 when already over the limit", free)
	}
	if fs.HasSpaceFor(1) == nil {
		t.Error("HasSpaceFor should refuse when the filesystem is already over its limit")
	}
}
