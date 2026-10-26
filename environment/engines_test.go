package environment

import (
	"context"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestHasWindowsVariant(t *testing.T) {
	linux := ocispec.Platform{OS: "linux", Architecture: "amd64"}
	linuxArm := ocispec.Platform{OS: "linux", Architecture: "arm64"}
	windows := ocispec.Platform{OS: "windows", Architecture: "amd64"}
	windowsArm := ocispec.Platform{OS: "windows", Architecture: "arm64"}
	unknown := ocispec.Platform{OS: "unknown", Architecture: "unknown"}

	cases := []struct {
		name      string
		platforms []ocispec.Platform
		arch      string
		want      bool
	}{
		{"windows only", []ocispec.Platform{windows}, "amd64", true},
		{"windows only with attestation entries", []ocispec.Platform{windows, unknown}, "amd64", true},
		{"windows for another architecture", []ocispec.Platform{windowsArm}, "amd64", false},
		// Windows is tried first, so an image that is also published for Linux still
		// runs in a Windows container.
		{"linux and windows", []ocispec.Platform{linux, windows}, "amd64", true},
		{"linux of another architecture and windows", []ocispec.Platform{linuxArm, windows}, "amd64", true},
		{"linux only", []ocispec.Platform{linux, linuxArm}, "amd64", false},
		{"no platforms", nil, "amd64", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hasWindowsVariant(c.platforms, c.arch); got != c.want {
				t.Errorf("hasWindowsVariant = %v, want %v", got, c.want)
			}
		})
	}
}

func TestUseWindowsEngine(t *testing.T) {
	cases := []struct {
		name            string
		containerID     string
		containerExists bool
		imageIsWindows  bool
		want            bool
	}{
		{"linux image, no container", "abc", false, false, false},
		{"windows image, no container", "abc", false, true, true},
		// A container that already exists on the Windows engine stays there, even
		// if the image can't be looked up (for example when offline).
		{"existing windows container", "abc", true, false, true},
		// Containers that do not exist yet have no id to look up.
		{"windows image, no id", "", false, true, true},
		{"linux image, no id", "", false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lookedUp := false
			got := useWindowsEngine(context.Background(), "image", c.containerID, windowsProbe{
				containerExists: func(_ context.Context, id string) bool {
					lookedUp = true
					if id != c.containerID {
						t.Errorf("looked up container %q, want %q", id, c.containerID)
					}
					return c.containerExists
				},
				imageIsWindows: func(context.Context, string) bool { return c.imageIsWindows },
			})
			if got != c.want {
				t.Errorf("useWindowsEngine = %v, want %v", got, c.want)
			}
			if c.containerID == "" && lookedUp {
				t.Error("the Windows engine was asked about a container without an id")
			}
		})
	}
}

func TestPullPlatformFollowsEngine(t *testing.T) {
	// Whatever the host is, a Windows engine pulls Windows images.
	if got := pullPlatform("windows", "amd64", func() bool { return true }); got != "windows/amd64" {
		t.Errorf("pullPlatform = %q", got)
	}
}
