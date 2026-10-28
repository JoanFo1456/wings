package environment

import (
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/go-connections/nat"
)

func TestPullPlatform(t *testing.T) {
	yes := func() bool { return true }
	no := func() bool { return false }
	cases := []struct {
		goos, goarch string
		windowsEng   func() bool
		want         string
	}{
		{"windows", "amd64", no, "linux/amd64"},
		{"windows", "arm64", no, "linux/arm64"},
		{"windows", "amd64", yes, "windows/amd64"},
		// Other hosts are unchanged and never ask the engine.
		{"linux", "amd64", func() bool { t.Error("engine queried on linux"); return true }, "linux/amd64"},
		{"darwin", "arm64", yes, "darwin/arm64"},
	}
	for _, c := range cases {
		if got := pullPlatform(c.goos, c.goarch, c.windowsEng); got != c.want {
			t.Errorf("pullPlatform(%s, %s) = %s, want %s", c.goos, c.goarch, got, c.want)
		}
	}
}

func TestWindowsMountTarget(t *testing.T) {
	cases := map[string]string{
		"/home/container":        `C:\Users\ContainerUser`,
		"/home/container/f":      `C:\Users\ContainerUser\f`,
		"/home/container/a/b":    `C:\Users\ContainerUser\a\b`,
		"/home/containerx/other": `C:\home\containerx\other`,
		"/mnt/server":            `C:\mnt\server`,
		`C:\data`:                `C:\data`,
	}
	for in, want := range cases {
		if got := windowsMountTarget(in); got != want {
			t.Errorf("windowsMountTarget(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAdaptForWindowsEngine(t *testing.T) {
	pids := int64(512)
	conf := &container.Config{User: "988:988", Tty: true}
	hc := &container.HostConfig{
		Tmpfs:          map[string]string{"/tmp": "rw"},
		SecurityOpt:    []string{"no-new-privileges"},
		ReadonlyRootfs: true,
		CapDrop:        []string{"net_raw"},
		UsernsMode:     "host",
		NetworkMode:    "pelican_nw",
		PortBindings: nat.PortMap{
			"25565/tcp": {{HostIP: "0.0.0.0", HostPort: "25565"}},
			"25565/udp": {{HostIP: "192.168.1.10", HostPort: "25565"}},
		},
		Resources: container.Resources{Memory: 1024, MemorySwap: 2048, PidsLimit: &pids, CpusetCpus: "0-1"},
		Mounts: []mount.Mount{
			{Type: mount.TypeBind, Source: `D:\data\srv`, Target: "/home/container"},
			{Type: mount.TypeBind, Source: `D:\pelican\passwd`, Target: "/etc/passwd", ReadOnly: true},
		},
	}

	AdaptForWindowsEngine(conf, hc)

	if hc.NetworkMode != "nat" {
		t.Errorf("network mode = %q, want nat", hc.NetworkMode)
	}
	if conf.Tty {
		t.Error("windows containers must not have a tty")
	}
	if hc.Tmpfs != nil || hc.SecurityOpt != nil || hc.ReadonlyRootfs || hc.CapDrop != nil || hc.UsernsMode != "" {
		t.Errorf("Linux-only settings were not removed: %+v", hc)
	}
	// uid:gid pairs mean nothing to Windows containers, they run as ContainerUser.
	if conf.User != "ContainerUser" {
		t.Errorf("user = %q, want ContainerUser", conf.User)
	}
	if hc.Resources.Memory != 1024 || hc.Resources.MemorySwap != 0 || hc.Resources.PidsLimit != nil || hc.Resources.CpusetCpus != "" {
		t.Errorf("unexpected resources: %+v", hc.Resources)
	}
	for port, bindings := range hc.PortBindings {
		if len(bindings) != 1 || bindings[0].HostIP != "" || bindings[0].HostPort != "25565" {
			t.Errorf("port binding %s should keep its port but lose the host IP: %+v", port, bindings)
		}
	}
	if len(hc.Mounts) != 1 || hc.Mounts[0].Target != `C:\Users\ContainerUser` || hc.Mounts[0].Source != `D:\data\srv` {
		t.Errorf("unexpected mounts: %+v", hc.Mounts)
	}
}
