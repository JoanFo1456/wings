//go:build windows

package cmd

import (
	"context"
	"errors"
	"os"
	"time"
	"unsafe"

	"github.com/apex/log"
	"github.com/docker/docker/client"
	"golang.org/x/sys/windows"

	"github.com/pelican/wings/environment"
	"github.com/pelican/wings/system"
)

// The named pipes of Docker Desktop's engines. Unlike the generic docker_engine
// pipe, which follows the engine that is currently selected, these always point
// at one specific kind of engine, and both engines can run at the same time.
const (
	windowsEnginePipe = "dockerDesktopWindowsEngine"
	linuxEnginePipe   = "dockerDesktopLinuxEngine"
)

var procWaitNamedPipe = windows.NewLazySystemDLL("kernel32.dll").NewProc("WaitNamedPipeW")

// pipeExists reports whether a named pipe exists. A busy pipe still exists.
func pipeExists(path string) bool {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	// WaitNamedPipeW returns zero on failure and ERROR_FILE_NOT_FOUND when the
	// pipe does not exist at all.
	r, _, callErr := procWaitNamedPipe.Call(uintptr(unsafe.Pointer(name)), 1)
	return r != 0 || !errors.Is(callErr, windows.ERROR_FILE_NOT_FOUND)
}

// engineResponds reports whether a Docker engine answers on the given host.
func engineResponds(host string) bool {
	cli, err := client.NewClientWithOpts(client.WithHost(host), client.WithAPIVersionNegotiation())
	if err != nil {
		return false
	}
	defer cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = cli.Ping(ctx)
	return err == nil
}

// runningEngine returns the host of the Docker Desktop engine behind the named
// pipe, or an empty string when that engine is not running.
func runningEngine(pipe string) string {
	if !pipeExists(`\\.\pipe\` + pipe) {
		return ""
	}
	host := "npipe:////./pipe/" + pipe
	if !engineResponds(host) {
		return ""
	}
	return host
}

// selectDockerEngine points the Docker client at the Docker Desktop engine that
// is currently running. When both the Linux and the Windows engine are running,
// the Linux engine stays the default and the Windows engine is registered next
// to it, so that every server is placed on the engine that can run its image.
// The engines are connected to directly, the environment of the process is not
// changed. An explicit DOCKER_HOST always wins, and if neither engine answers the
// default is left untouched.
func selectDockerEngine() {
	if host := os.Getenv("DOCKER_HOST"); host != "" {
		log.WithField("host", host).Debug("DOCKER_HOST is set, using that docker engine only")
		return
	}
	linux, windowsHost := runningEngine(linuxEnginePipe), runningEngine(windowsEnginePipe)
	switch {
	case linux != "" && windowsHost != "":
		system.SetDockerHost(linux)
		environment.UseWindowsEngine(windowsHost)
		log.WithFields(log.Fields{"default": linux, "windows": windowsHost}).
			Info("both Docker Desktop engines are running, using the Linux engine by default and the Windows engine for Windows images")
	case linux != "":
		system.SetDockerHost(linux)
		log.WithField("host", linux).Info("using the Linux Docker Desktop engine")
	case windowsHost != "":
		system.SetDockerHost(windowsHost)
		log.WithField("host", windowsHost).Info("using the Windows Docker Desktop engine")
	default:
		log.Warn("no Docker Desktop engine answered, using the default Docker host")
	}
}
