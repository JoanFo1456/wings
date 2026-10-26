package system

import (
	"sync"

	"github.com/docker/docker/client"
)

var (
	dockerHostMu sync.RWMutex
	dockerHost   string
)

// SetDockerHost sets the Docker engine that clients created by NewDockerClient
// connect to, without changing the environment of the process. An empty host
// goes back to the standard Docker environment variables.
func SetDockerHost(host string) {
	dockerHostMu.Lock()
	dockerHost = host
	dockerHostMu.Unlock()
}

// DockerHost returns the host set with SetDockerHost, if any.
func DockerHost() string {
	dockerHostMu.RLock()
	defer dockerHostMu.RUnlock()
	return dockerHost
}

// NewDockerClient creates a Docker client for the default engine, which is the
// one set with SetDockerHost or else the one the Docker environment variables
// (DOCKER_HOST and friends) point to.
func NewDockerClient() (*client.Client, error) {
	opts := []client.Opt{client.WithAPIVersionNegotiation()}
	if host := DockerHost(); host != "" {
		opts = append(opts, client.WithHost(host))
	} else {
		opts = append(opts, client.FromEnv)
	}
	return client.NewClientWithOpts(opts...)
}
