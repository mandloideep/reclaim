package apply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/mandloideep/reclaim/internal/plan"
)

// verifyArgument checks, right before a removal command that names its
// target runs, that the target is still what the scan found:
//
//   - ollama rm: the model's manifest still exists in the models folder the
//     scan read, the ollama client talks to this machine, and the system
//     wide service, if any, holds no model of the same name, so the command
//     cannot remove a same named model of another server.
//   - xcrun simctl delete: the simulator still exists and is still
//     unavailable, so a simulator whose runtime was installed again since the
//     scan, and which may hold data, is not deleted.
//
// A target that no longer exists is reported as already gone.
func (r *runner) verifyArgument(ctx context.Context, a *plan.Action) error {
	argv := a.Command
	switch {
	case len(argv) == 3 && argv[0] == "ollama" && argv[1] == "rm":
		return r.verifyOllama(argv[2], a.Path)
	case len(argv) == 4 && argv[0] == "xcrun" && argv[1] == "simctl" && argv[2] == "delete":
		return r.verifySimulator(ctx, argv[3])
	default:
		return nil
	}
}

func (r *runner) verifyOllama(model, models string) error {
	if host := r.getenv("OLLAMA_HOST"); host != "" && !isLocalHost(host) {
		return fmt.Errorf("refusing ollama rm %s: OLLAMA_HOST points at %s, not this machine, whose models were scanned", model, host)
	}
	rel, ok := plan.OllamaManifest(model)
	if !ok || models == "" {
		return fmt.Errorf("refusing ollama rm %s: cannot tell where its manifest is", model)
	}
	info, err := os.Lstat(filepath.Join(models, "manifests", filepath.FromSlash(rel)))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return errGone
	case err != nil:
		return fmt.Errorf("refusing ollama rm %s: %w", model, err)
	case !info.Mode().IsRegular():
		return fmt.Errorf("refusing ollama rm %s: its manifest is not a regular file", model)
	}
	// The system wide service usually is the server that answers, so a
	// model of the same name there would be the one removed.
	if sys := r.opts.OllamaSystemModels; sys != "" && sys != models {
		_, err := os.Lstat(filepath.Join(sys, "manifests", filepath.FromSlash(rel)))
		switch {
		case err == nil:
			return fmt.Errorf("refusing ollama rm %s: the system Ollama service in %s has a model of the same name, which the command could remove instead", model, sys)
		case !errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("refusing ollama rm %s: cannot check the system Ollama models in %s for a model of the same name: %w", model, sys, err)
		}
	}
	return nil
}

// isLocalHost reports whether an OLLAMA_HOST value names this machine.
func isLocalHost(value string) bool {
	v := strings.TrimSpace(value)
	if strings.Contains(v, "://") {
		u, err := url.Parse(v)
		if err != nil {
			return false
		}
		v = u.Host
	}
	host := v
	if h, _, err := net.SplitHostPort(v); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "" || host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

func (r *runner) verifySimulator(ctx context.Context, udid string) error {
	out, err := r.opts.Exec.Output(ctx, "xcrun", "simctl", "list", "-j", "devices")
	if err != nil {
		return fmt.Errorf("refusing xcrun simctl delete %s: could not list simulators: %w", udid, err)
	}
	var list struct {
		Devices map[string][]struct {
			UDID        string `json:"udid"`
			IsAvailable bool   `json:"isAvailable"`
		} `json:"devices"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return fmt.Errorf("refusing xcrun simctl delete %s: could not read the simulator list: %w", udid, err)
	}
	for _, devices := range list.Devices {
		for _, d := range devices {
			if !strings.EqualFold(d.UDID, udid) {
				continue
			}
			if d.IsAvailable {
				return fmt.Errorf("refusing xcrun simctl delete %s: the simulator is available again", udid)
			}
			return nil
		}
	}
	return errGone
}

func (r *runner) getenv(key string) string {
	if r.opts.Getenv == nil {
		return ""
	}
	return r.opts.Getenv(key)
}
