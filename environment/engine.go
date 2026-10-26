package environment

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
)

var (
	engineMu      sync.Mutex
	engineKnown   bool
	engineWindows bool
)

// DockerIsWindows reports whether the Docker engine Wings is connected to runs
// Windows containers. This is only ever true when Wings runs on Windows against
// Docker Desktop in Windows containers mode. The answer is cached once the
// engine has responded; if it can't be determined it is assumed to be Linux.
func DockerIsWindows() bool {
	engineMu.Lock()
	defer engineMu.Unlock()
	if engineKnown {
		return engineWindows
	}
	cli, err := Docker()
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	info, err := cli.Info(ctx)
	if err != nil {
		return false
	}
	engineKnown = true
	engineWindows = info.OSType == "windows"
	return engineWindows
}

// PullPlatform returns the platform to request when pulling an image on the
// given engine. This is the platform of the machine Wings runs on, except on
// Windows where it follows the engine: Docker Desktop's Linux engine needs
// linux images even though Wings itself is a Windows binary.
func PullPlatform(engineIsWindows bool) string {
	return pullPlatform(runtime.GOOS, runtime.GOARCH, func() bool { return engineIsWindows })
}

func pullPlatform(goos, goarch string, engineIsWindows func() bool) string {
	if goos == "windows" && !engineIsWindows() {
		goos = "linux"
	}
	return goos + "/" + goarch
}

// windowsMountTarget converts a Linux style container path to the drive letter
// path Windows containers require. The home directory of Linux containers,
// /home/container, is the profile folder of the container user on Windows, so
// it and everything below it ends up in C:\Users\ContainerUser.
func windowsMountTarget(target string) string {
	if !strings.HasPrefix(target, "/") {
		return target
	}
	const linuxHome = "/home/container"
	if target == linuxHome || strings.HasPrefix(target, linuxHome+"/") {
		target = `/Users/` + windowsContainerUser + strings.TrimPrefix(target, linuxHome)
	}
	return `C:` + strings.ReplaceAll(target, "/", `\`)
}

// WindowsContainerPath converts a Linux style path inside of a container, such
// as /mnt/install/install.ps1, to the path it has in a Windows container.
func WindowsContainerPath(p string) string {
	return windowsMountTarget(p)
}

// windowsResources reduces the resource limits to the ones Windows containers
// support. The swap, PID, cpuset and cgroup based limits are rejected by the
// engine.
func windowsResources(r container.Resources) container.Resources {
	return container.Resources{Memory: r.Memory}
}

// WindowsResources reduces the resource limits to the ones Windows containers
// support, for containers that run on the Windows engine.
func WindowsResources(r container.Resources) container.Resources {
	return windowsResources(r)
}

// windowsContainerUser is the user Windows containers run the server as. Windows
// containers don't know numeric uid:gid pairs, they only accept a user name and
// the unprivileged account of the Windows base images is called ContainerUser.
const windowsContainerUser = "ContainerUser"

// windowsNetworkMode is the network Windows containers are attached to.
const windowsNetworkMode = "nat"

// AdaptForWindowsEngine removes everything from a container configuration that
// only exists for Linux containers and rewrites mount targets to Windows paths.
// Mounts of Linux system files (such as /etc/passwd) are dropped.
func AdaptForWindowsEngine(conf *container.Config, hc *container.HostConfig) {
	conf.User = windowsContainerUser

	hc.Tmpfs = nil
	hc.SecurityOpt = nil
	hc.ReadonlyRootfs = false
	hc.CapDrop = nil
	hc.UsernsMode = ""
	hc.Resources = windowsResources(hc.Resources)

	// The bridge network Wings creates only exists on the Linux engine, Windows
	// containers use the nat network every Windows engine provides.
	hc.NetworkMode = windowsNetworkMode

	// The nat network of Windows containers rejects host IP addresses ("Windows
	// does not support host IP addresses in NAT settings"), only the ports are
	// accepted, so the ports are published on all addresses.
	for port, bindings := range hc.PortBindings {
		for i := range bindings {
			bindings[i].HostIP = ""
		}
		hc.PortBindings[port] = bindings
	}

	mounts := make([]mount.Mount, 0, len(hc.Mounts))
	for _, m := range hc.Mounts {
		if strings.HasPrefix(m.Target, "/etc/") {
			continue
		}
		m.Target = windowsMountTarget(m.Target)
		mounts = append(mounts, m)
	}
	hc.Mounts = mounts
}
