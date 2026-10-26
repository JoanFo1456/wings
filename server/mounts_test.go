package server

import (
	"testing"

	"github.com/pelican/wings/config"
)

// The target of a custom mount is a path inside the Linux container, it has to
// keep its forward slashes no matter which OS Wings itself runs on.
func TestCustomMountsKeepContainerPathSlashes(t *testing.T) {
	src := t.TempDir()
	c, err := config.NewAtPath("")
	if err != nil {
		t.Fatal(err)
	}
	c.AllowedMounts = []string{src}
	c.BlockBaseDirMount = true
	c.AuthenticationToken = "test"
	config.Set(c)

	srv, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.Config().Mounts = []Mount{
		{Source: src, Target: "/mnt/data/../data/", ReadOnly: true},
		{Source: src, Target: "/home/container/inner"},
	}

	got := srv.customMounts()
	if len(got) != 1 {
		t.Fatalf("expected the /home/container mount to be skipped, got %+v", got)
	}
	if got[0].Target != "/mnt/data" {
		t.Errorf("target = %q, want /mnt/data", got[0].Target)
	}
	if !got[0].ReadOnly {
		t.Error("read only flag was lost")
	}
}
