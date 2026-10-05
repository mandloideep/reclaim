// Package dockerx is a thin wrapper over the Docker Engine API client.
//
// It defines API, the narrow set of client methods reclaim uses, so the
// scanner and the apply executors can be tested against a fake, and it
// resolves the daemon address the same way the docker CLI does: DOCKER_HOST
// first, then the active docker context.
package dockerx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/system"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
)

// API is the subset of *client.Client that reclaim uses.
type API interface {
	// DiskUsage returns "docker system df" data.
	DiskUsage(ctx context.Context, options types.DiskUsageOptions) (types.DiskUsage, error)
	// ContainerList lists containers.
	ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error)
	// ContainerInspect returns the details of one container.
	ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error)
	// ContainerRemove removes one container.
	ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error
	// ImageInspect returns the details of one image.
	ImageInspect(ctx context.Context, imageID string, inspectOpts ...client.ImageInspectOption) (image.InspectResponse, error)
	// ImageList lists images.
	ImageList(ctx context.Context, options image.ListOptions) ([]image.Summary, error)
	// ImageRemove removes an image reference or an image.
	ImageRemove(ctx context.Context, imageID string, options image.RemoveOptions) ([]image.DeleteResponse, error)
	// VolumeInspect returns the details of one volume.
	VolumeInspect(ctx context.Context, volumeID string) (volume.Volume, error)
	// VolumeRemove removes one volume.
	VolumeRemove(ctx context.Context, volumeID string, force bool) error
	// BuildCachePrune prunes the build cache.
	BuildCachePrune(ctx context.Context, opts build.CachePruneOptions) (*build.CachePruneReport, error)
	// Info returns daemon information.
	Info(ctx context.Context) (system.Info, error)
	// DaemonHost returns the address of the daemon.
	DaemonHost() string
	// Close releases the client's resources.
	Close() error
}

var _ API = (*client.Client)(nil)

// New returns a client for the daemon selected by DOCKER_HOST or, when it is
// unset, by DOCKER_CONTEXT or the current context in the docker CLI
// configuration. Creating a client does not contact the daemon.
func New(getenv func(string) string, home string) (*client.Client, error) {
	opts := []client.Opt{client.FromEnv, client.WithAPIVersionNegotiation()}
	if getenv("DOCKER_HOST") == "" {
		host, err := ContextHost(getenv, home)
		if err != nil {
			return nil, err
		}
		if host != "" {
			opts = append(opts, client.WithHost(host))
		}
	}
	c, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return c, nil
}

// ContextHost returns the daemon address of the active docker context, or an
// empty string when the default context is active. The active context is
// DOCKER_CONTEXT when set, else "currentContext" in config.json inside
// DOCKER_CONFIG or ~/.docker.
func ContextHost(getenv func(string) string, home string) (string, error) {
	configDir := getenv("DOCKER_CONFIG")
	if configDir == "" {
		configDir = filepath.Join(home, ".docker")
	}
	name := getenv("DOCKER_CONTEXT")
	if name == "" {
		data, err := os.ReadFile(filepath.Join(configDir, "config.json"))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return "", nil
		case err != nil:
			return "", fmt.Errorf("read docker config %s: %w", configDir, err)
		}
		var cfg struct {
			CurrentContext string `json:"currentContext"`
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return "", fmt.Errorf("parse docker config %s: %w", configDir, err)
		}
		name = cfg.CurrentContext
	}
	if name == "" || name == "default" {
		return "", nil
	}
	sum := sha256.Sum256([]byte(name))
	metaPath := filepath.Join(configDir, "contexts", "meta", hex.EncodeToString(sum[:]), "meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return "", fmt.Errorf("docker context %q: %w", name, err)
	}
	var meta struct {
		Endpoints map[string]struct {
			Host string `json:"Host"`
		} `json:"Endpoints"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return "", fmt.Errorf("docker context %q: parse %s: %w", name, metaPath, err)
	}
	host := meta.Endpoints["docker"].Host
	if host == "" {
		return "", fmt.Errorf("docker context %q: no docker endpoint in %s", name, metaPath)
	}
	return host, nil
}

// Label is one --docker-label filter. A label without a value matches any
// object that has the key.
type Label struct {
	// Key is the label key.
	Key string
	// Value is the required value when HasValue is true.
	Value string
	// HasValue reports whether a value was given.
	HasValue bool
}

// String formats the label as it was given on the command line.
func (l Label) String() string {
	if l.HasValue {
		return l.Key + "=" + l.Value
	}
	return l.Key
}

// ParseLabel parses "key=value" or "key".
func ParseLabel(s string) (Label, error) {
	key, value, hasValue := strings.Cut(s, "=")
	key = strings.TrimSpace(key)
	if key == "" {
		return Label{}, fmt.Errorf("docker label %q: empty key", s)
	}
	return Label{Key: key, Value: value, HasValue: hasValue}, nil
}

// MatchLabels reports whether labels satisfy every filter. An empty filter
// list matches everything.
func MatchLabels(filters []Label, labels map[string]string) bool {
	for _, f := range filters {
		v, ok := labels[f.Key]
		if !ok || (f.HasValue && v != f.Value) {
			return false
		}
	}
	return true
}

// IsNotFound reports whether err says the Docker object does not exist. The
// client marks such errors so that errors.Is matches containerd's
// errdefs.ErrNotFound, and the fake uses the older convention of an error
// type with a NotFound method; both are recognized.
func IsNotFound(err error) bool {
	var nf interface{ NotFound() }
	if errors.As(err, &nf) {
		return true
	}
	// The suggested replacement lives in github.com/containerd/errdefs, which
	// is not an approved direct dependency, so the client helper stays.
	return client.IsErrNotFound(err) //nolint:staticcheck // see above
}

// IsActiveState reports whether a container in this state is running or about
// to run. Such containers and everything they use are never offered.
func IsActiveState(state string) bool {
	switch state {
	case container.StateExited, container.StateCreated, container.StateDead:
		return false
	default:
		return true
	}
}

// IsOrbStack reports whether the daemon is OrbStack, which returns freed disk
// space to the host automatically.
func IsOrbStack(host string, info system.Info) bool {
	return strings.Contains(host, ".orbstack") || strings.Contains(strings.ToLower(info.OperatingSystem), "orbstack")
}
