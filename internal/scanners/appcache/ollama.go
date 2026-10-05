package appcache

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/scan"
	"github.com/mandloideep/reclaim/internal/units"
)

// maxManifestSize bounds how much of a manifest file is read.
const maxManifestSize = 1 << 20

// ollamaManifest is the part of an Ollama model manifest the scanner reads.
type ollamaManifest struct {
	Config ollamaLayer   `json:"config"`
	Layers []ollamaLayer `json:"layers"`
}

type ollamaLayer struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

type ollamaModel struct {
	name    string
	blobs   map[string]int64
	modTime time.Time
}

// scanOllama reports one finding per model, read from the manifests in the
// models folder, so the Ollama server does not need to run for a scan. A
// model's size is the bytes only it uses: blobs shared with other models are
// freed only when every model using them is removed.
func scanOllama(ctx context.Context, env *scan.Env) ([]finding.Finding, error) {
	dir := resolve(firstNonEmpty(absVar(env, "OLLAMA_MODELS"), filepath.Join(env.Home, ".ollama", "models")))
	manifests := filepath.Join(dir, "manifests")
	var models []ollamaModel
	err := filepath.WalkDir(manifests, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return filepath.SkipDir
			}
			env.Diag.Warn(path, "could not read: "+err.Error())
			return nil
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel := strings.TrimPrefix(path, manifests+string(filepath.Separator))
		name, ok := ollamaName(filepath.ToSlash(rel))
		if !ok {
			return nil
		}
		if m, ok := loadModel(env, path, name, d); ok {
			models = append(models, m)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	users := map[string]int{}
	for _, m := range models {
		for digest := range m.blobs {
			users[digest]++
		}
	}
	var out []finding.Finding
	for _, m := range models {
		command := []string{"ollama", "rm", m.name}
		if !plan.AllowedCommand(command) {
			env.Diag.Warn("", "model name "+m.name+" is not one reclaim can pass to ollama rm, skipped")
			continue
		}
		var own, shared int64
		for digest, size := range m.blobs {
			if users[digest] == 1 {
				own += size
			} else {
				shared += size
			}
		}
		if own == 0 {
			continue
		}
		warning := "ollama rm needs the Ollama app or ollama serve running"
		if shared > 0 {
			warning = "shares " + units.FormatSize(shared) + " with other models, freed only when they go too; " + warning
		}
		out = append(out, finding.Finding{
			Tier:     finding.TierB,
			Path:     dir,
			Target:   plan.OllamaTarget(m.name),
			Name:     "model " + m.name,
			Size:     own,
			LastUsed: m.modTime,
			Restore:  "ollama pull " + m.name,
			Action:   finding.ActionRunCommand,
			Command:  command,
			Warning:  warning,
		})
	}
	slices.SortFunc(out, func(a, b finding.Finding) int { return cmp.Compare(a.Target, b.Target) })
	return out, nil
}

// loadModel reads one manifest. An unreadable manifest adds a warning and
// is skipped.
func loadModel(env *scan.Env, path, name string, d fs.DirEntry) (ollamaModel, bool) {
	m, err := readManifest(path)
	if err != nil {
		env.Diag.Warn(path, "could not read model manifest: "+err.Error())
		return ollamaModel{}, false
	}
	blobs := map[string]int64{}
	for _, l := range append([]ollamaLayer{m.Config}, m.Layers...) {
		if l.Digest != "" && l.Size > 0 {
			blobs[l.Digest] = l.Size
		}
	}
	var mod time.Time
	if info, err := d.Info(); err == nil {
		mod = info.ModTime()
	}
	return ollamaModel{name: name, blobs: blobs, modTime: mod}, true
}

// ollamaName turns a manifest path relative to the manifests folder, such as
// "registry.ollama.ai/library/llama3/latest", into the name Ollama uses for
// the model, here "llama3:latest".
func ollamaName(rel string) (string, bool) {
	parts := strings.Split(rel, "/")
	if len(parts) != 4 || slices.Contains(parts, "") {
		return "", false
	}
	registry, namespace, model, tag := parts[0], parts[1], parts[2], parts[3]
	switch {
	case registry == "registry.ollama.ai" && namespace == "library":
		return model + ":" + tag, true
	case registry == "registry.ollama.ai":
		return namespace + "/" + model + ":" + tag, true
	default:
		return registry + "/" + namespace + "/" + model + ":" + tag, true
	}
}

func readManifest(path string) (ollamaManifest, error) {
	var m ollamaManifest
	info, err := os.Lstat(path)
	if err != nil {
		return m, err
	}
	if info.Size() > maxManifestSize {
		return m, errors.New("manifest too large")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(data, &m)
	return m, err
}
