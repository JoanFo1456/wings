//go:build linux

package vdisk

import (
	"os"
	"testing"
)

// TestProbeHostSupport reports what this host can actually do with loop
// devices. It is a diagnostic rather than an assertion, so it never fails, and
// it is skipped unless explicitly asked for.
func TestProbeHostSupport(t *testing.T) {
	if os.Getenv("VDISK_PROBE") == "" {
		t.Skip("set VDISK_PROBE=1 to probe the host")
	}
	if err := New().Supported(); err != nil {
		t.Logf("virtual disks UNSUPPORTED on this host: %v", err)
		return
	}
	t.Log("virtual disks are SUPPORTED on this host")
}
