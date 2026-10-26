//go:build windows

package cmd

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/pelican/wings/config"
	"github.com/pelican/wings/environment"
	"github.com/pelican/wings/system"
)

// TestEngineForLive checks the placement of images against a real Docker Desktop
// that runs both its Linux and its Windows engine. It needs internet access and
// is only run when WINGS_LIVE_DOCKER is set.
func TestEngineForLive(t *testing.T) {
	if os.Getenv("WINGS_LIVE_DOCKER") == "" {
		t.Skip("set WINGS_LIVE_DOCKER=1 to run this test against Docker Desktop")
	}

	t.Setenv("DOCKER_HOST", "")
	system.SetDockerHost("")
	environment.UseWindowsEngine("")
	t.Cleanup(func() {
		system.SetDockerHost("")
		environment.UseWindowsEngine("")
	})

	c, err := config.NewAtPath("")
	if err != nil {
		t.Fatal(err)
	}
	c.AuthenticationToken = "test"
	config.Set(c)

	selectDockerEngine()
	if system.DockerHost() == "" {
		t.Skip("no Docker Desktop engine is running")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	linux, err := environment.EngineFor(ctx, "ghcr.io/pelican-eggs/yolks:java_21", "")
	if err != nil {
		t.Fatal(err)
	}
	if linux.Windows {
		t.Error("a Linux-only image was placed on the Windows engine")
	}

	win, err := environment.EngineFor(ctx, "mcr.microsoft.com/windows/nanoserver:ltsc2022", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("nanoserver on the Windows engine: %v (both engines running: %v)", win.Windows, linux.Client != win.Client)
	if linux.Client != win.Client && !win.Windows {
		t.Error("a Windows image was not placed on the Windows engine although both engines are running")
	}
}
