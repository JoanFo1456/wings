//go:build windows

package cmd

import (
	"os"
	"testing"

	"github.com/pelican/wings/environment"
	"github.com/pelican/wings/system"
)

func TestPipeExists(t *testing.T) {
	if pipeExists(`\\.\pipe\wings-test-pipe-that-does-not-exist`) {
		t.Error("a pipe that does not exist was reported as existing")
	}
	// Named pipes that exist on a running Windows system.
	if !pipeExists(`\\.\pipe\lsass`) && !pipeExists(`\\.\pipe\wkssvc`) {
		t.Skip("no well-known pipe available to test the positive case")
	}
}

func TestSelectDockerEngine(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	system.SetDockerHost("")
	environment.UseWindowsEngine("")
	t.Cleanup(func() {
		system.SetDockerHost("")
		environment.UseWindowsEngine("")
	})

	selectDockerEngine()
	host := system.DockerHost()
	if host == "" {
		t.Skip("no Docker Desktop engine is running")
	}
	t.Logf("default engine: %s", host)
	if !engineResponds(host) {
		t.Errorf("selected engine %s does not respond", host)
	}
	// The engines are connected to directly, the environment must stay untouched.
	if got := os.Getenv("DOCKER_HOST"); got != "" {
		t.Errorf("DOCKER_HOST was changed to %s", got)
	}

	// An explicit DOCKER_HOST must never be replaced.
	system.SetDockerHost("")
	t.Setenv("DOCKER_HOST", "npipe:////./pipe/custom")
	selectDockerEngine()
	if got := system.DockerHost(); got != "" {
		t.Errorf("an explicit DOCKER_HOST was overridden by %s", got)
	}
}
