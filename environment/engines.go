package environment

import (
	"context"
	"runtime"
	"strings"
	"sync"

	"github.com/apex/log"
	"github.com/docker/docker/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/pelican/wings/config"
)

// Engine is a Docker engine together with the kind of containers it runs.
type Engine struct {
	Client  *client.Client
	Windows bool
}

var (
	secondaryMu sync.Mutex
	// secondaryHost is the Windows engine that is used next to the default Linux
	// engine. It is only set on Windows, when Docker Desktop runs both engines.
	secondaryHost   string
	secondaryClient *client.Client

	// windowsImages remembers which images have a Windows version.
	windowsImages sync.Map
)

// UseWindowsEngine registers the Docker engine on host as the Windows engine
// that is used next to the default engine, which then has to be the Linux one.
// Passing an empty host removes it again. Servers are then placed on the engine
// that can run their image, see EngineFor.
func UseWindowsEngine(host string) {
	secondaryMu.Lock()
	defer secondaryMu.Unlock()
	if secondaryClient != nil {
		_ = secondaryClient.Close()
	}
	secondaryHost = host
	secondaryClient = nil
	windowsImages = sync.Map{}
}

// windowsEngine returns the client of the registered Windows engine.
func windowsEngine() (*client.Client, bool) {
	secondaryMu.Lock()
	defer secondaryMu.Unlock()
	if secondaryHost == "" {
		return nil, false
	}
	if secondaryClient == nil {
		c, err := client.NewClientWithOpts(client.WithHost(secondaryHost), client.WithAPIVersionNegotiation())
		if err != nil {
			return nil, false
		}
		secondaryClient = c
	}
	return secondaryClient, true
}

// EngineFor returns the Docker engine that has to be used for a container with
// the given image. When only one engine is available, which is always the case
// on Linux and macOS, that is the default engine. When Docker Desktop runs both
// its Windows and its Linux engine, a container goes to the Windows engine if it
// already exists there or if its image has a Windows version, and otherwise to
// the Linux engine. containerID can be empty for containers that do not exist yet.
func EngineFor(ctx context.Context, image, containerID string) (*Engine, error) {
	def, err := Docker()
	if err != nil {
		return nil, err
	}
	defaultEngine := &Engine{Client: def, Windows: DockerIsWindows()}

	winCli, ok := windowsEngine()
	if !ok || defaultEngine.Windows {
		return defaultEngine, nil
	}

	if useWindowsEngine(ctx, image, containerID, windowsProbe{
		containerExists: func(ctx context.Context, id string) bool {
			_, err := winCli.ContainerInspect(ctx, id)
			return err == nil
		},
		imageIsWindows: func(ctx context.Context, image string) bool { return imageHasWindowsVersion(ctx, winCli, image) },
	}) {
		log.WithFields(log.Fields{"image": image, "container": containerID}).Info("using the Windows engine")
		return &Engine{Client: winCli, Windows: true}, nil
	}
	return defaultEngine, nil
}

// windowsProbe holds the questions that are asked to the Windows engine.
type windowsProbe struct {
	containerExists func(ctx context.Context, id string) bool
	imageIsWindows  func(ctx context.Context, image string) bool
}

// useWindowsEngine decides whether a container belongs on the Windows engine.
func useWindowsEngine(ctx context.Context, image, containerID string, p windowsProbe) bool {
	// A container that already exists on the Windows engine stays there.
	if containerID != "" && p.containerExists(ctx, containerID) {
		return true
	}
	return p.imageIsWindows(ctx, image)
}

// imageHasWindowsVersion reports whether the image can run on the Windows
// engine. An image that is already on the engine is checked locally, otherwise
// the registry is asked which platforms the image is published for. When that
// is not possible the answer is no, and the Linux engine is used.
func imageHasWindowsVersion(ctx context.Context, cli *client.Client, image string) bool {
	local := strings.HasPrefix(image, "~")
	image = strings.TrimPrefix(image, "~")
	if v, ok := windowsImages.Load(image); ok {
		return v.(bool)
	}

	if inspect, err := cli.ImageInspect(ctx, image); err == nil {
		windows := inspect.Os == "windows"
		windowsImages.Store(image, windows)
		return windows
	}
	// Local images can't be looked up in a registry.
	if local {
		return false
	}

	var auth string
	if _, registryAuth := config.Get().Docker.RegistryCredentialsForImage(image); registryAuth != nil {
		auth, _ = registryAuth.Base64()
	}
	dist, err := cli.DistributionInspect(ctx, image, auth)
	if err != nil {
		return false
	}
	windows := hasWindowsVariant(dist.Platforms, runtime.GOARCH)
	windowsImages.Store(image, windows)
	return windows
}

// hasWindowsVariant reports whether an image is published for Windows on the
// given architecture. The Windows engine is tried first, so an image that is
// published for both Windows and Linux runs in a native Windows container, and
// only an image without a Windows version falls back to the Linux engine.
func hasWindowsVariant(platforms []ocispec.Platform, arch string) bool {
	for _, p := range platforms {
		if p.OS == "windows" && p.Architecture == arch {
			return true
		}
	}
	return false
}
