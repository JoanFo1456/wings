package vdisk

import (
	"math"
	"testing"
)

func TestSizeWithOverheadAddsHeadroom(t *testing.T) {
	const gib = 1 << 30

	// A server sold 10 GiB has to be able to store 10 GiB, so the image is
	// created larger than the allocation to pay for ext4's metadata.
	got := SizeWithOverhead(10*gib, 5)
	if want := int64(10*gib*105/100) + FixedOverhead; got != want {
		t.Fatalf("SizeWithOverhead(10GiB, 5) = %d, want %d", got, want)
	}
	if got <= 10*gib {
		t.Fatalf("image size %d does not exceed the allocation", got)
	}
}

func TestSizeWithOverheadFallsBackToDefault(t *testing.T) {
	const gib = 1 << 30
	want := SizeWithOverhead(gib, DefaultOverheadPercent)
	for _, pct := range []int{0, -1, -100} {
		if got := SizeWithOverhead(gib, pct); got != want {
			t.Errorf("SizeWithOverhead(1GiB, %d) = %d, want the default %d", pct, got, want)
		}
	}
}

func TestSizeWithOverheadDoesNotOverflow(t *testing.T) {
	// Overflowing here would wrap to a tiny or negative image, which would
	// silently give a server a fraction of the disk it asked for rather than
	// failing. Returning the requested size unchanged is the safe outcome.
	for _, size := range []int64{math.MaxInt64, math.MaxInt64 - 1, 1 << 62} {
		got := SizeWithOverhead(size, 5)
		if got < size {
			t.Errorf("SizeWithOverhead(%d, 5) = %d, which is smaller than the allocation", size, got)
		}
	}
}
