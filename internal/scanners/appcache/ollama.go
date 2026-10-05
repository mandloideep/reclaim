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

// ollamaFolder is a models folder the Ollama scanner reads.
type ollamaFolder struct {
	dir string
	// system marks the folder of the system wide service that the Linux
	// install script sets up. Its files belong to the ollama account and
	// its models serve every user, so its findings are printed as commands
	// for the user to run and never run by apply.
	system bool
}

// ollamaFolders returns the models folders to scan: the user's, from
// OLLAMA_MODELS or ~/.ollama/models, and on Linux the system wide service's.
func ollamaFolders(env *scan.Env) []ollamaFolder {
	user := resolve(firstNonEmpty(absVar(env, "OLLAMA_MODELS"), filepath.Join(env.Home, ".ollama", "models")))
	out := []ollamaFolder{{dir: user}}
	if env.GOOS == "linux" {
		if sys := resolve(plan.OllamaSystemModels); sys != user {
			out = append(out, ollamaFolder{dir: sys, system: true})
		}
	}
	return out
}

// scanOllama reports one finding per model, read from the manifests in the
// models folders, so the Ollama server does not need to run for a scan.
func scanOllama(ctx context.Context, env *scan.Env) ([]finding.Finding, error) {
	return scanOllamaFolders(ctx, env, ollamaFolders(env))
}

// scanOllamaFolders reports the models of every folder. A model's size is
// the bytes only it uses: blobs shared with other models of its folder are
// freed only when every model using them is removed.
//
// ollama rm removes a model from whichever server answers, and the system
// service usually is the one that answers. A model in the user's folder is
// therefore offered only when the system folder, if there is one, can be read
// and holds no model of the same name; apply checks this again.
func scanOllamaFolders(ctx context.Context, env *scan.Env, folders []ollamaFolder) ([]finding.Finding, error) {
	var system *ollamaScan
	scans := make([]ollamaScan, len(folders))
	for i, folder := range folders {
		s, err := readModels(ctx, env, folder)
		if err != nil {
			return nil, err
		}
		scans[i] = s
		if folder.system {
			system = &scans[i]
		}
	}
	var out []finding.Finding
	for _, s := range scans {
		for _, f := range modelFindings(env, s.folder, s.models) {
			model := f.Command[len(f.Command)-1]
			switch {
			case s.folder.system || system == nil:
			case system.blocked:
				env.Diag.Note("The Ollama models in " + s.folder.dir + " are not offered, because the system Ollama models in " +
					system.folder.dir + " cannot be read and ollama rm could remove a model of the same name there.")
				continue
			case system.has(model):
				env.Diag.Note("Ollama model " + model + " is in " + s.folder.dir + " and in " + system.folder.dir +
					"; ollama rm cannot choose which copy goes, so only the copy in " + system.folder.dir + " is offered.")
				continue
			}
			out = append(out, f)
		}
	}
	slices.SortFunc(out, func(a, b finding.Finding) int {
		return cmp.Or(cmp.Compare(a.Target, b.Target), cmp.Compare(a.Path, b.Path))
	})
	return out, nil
}

// ollamaScan is what the scanner read from one models folder.
type ollamaScan struct {
	folder ollamaFolder
	models []ollamaModel
	// blocked is set for a system folder that exists but cannot be read.
	blocked bool
}

func (s *ollamaScan) has(model string) bool {
	return slices.ContainsFunc(s.models, func(m ollamaModel) bool { return m.name == model })
}

// readModels reads the manifests of one models folder. A system folder the
// user cannot read is mentioned in a note, since it normally belongs to the
// ollama account.
func readModels(ctx context.Context, env *scan.Env, folder ollamaFolder) (ollamaScan, error) {
	manifests := filepath.Join(folder.dir, "manifests")
	res := ollamaScan{folder: folder}
	err := filepath.WalkDir(manifests, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			switch {
			case errors.Is(err, fs.ErrNotExist):
				return filepath.SkipDir
			case folder.system && errors.Is(err, fs.ErrPermission):
				if !res.blocked {
					env.Diag.Note("The system Ollama models in " + folder.dir + " cannot be read without root, so they are not listed.")
				}
				res.blocked = true
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
			res.models = append(res.models, m)
		}
		return nil
	})
	return res, err
}

// modelFindings turns the models of one folder into findings.
func modelFindings(env *scan.Env, folder ollamaFolder, models []ollamaModel) []finding.Finding {
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
		if folder.system {
			warning = "a model of the system wide Ollama service, shared by every user of this machine; run the command yourself while the service runs"
		}
		if shared > 0 {
			warning = "shares " + units.FormatSize(shared) + " with other models, freed only when they go too; " + warning
		}
		out = append(out, finding.Finding{
			Tier:      finding.TierB,
			Path:      folder.dir,
			Target:    plan.OllamaTarget(m.name),
			Name:      "model " + m.name,
			Size:      own,
			LastUsed:  m.modTime,
			Restore:   "ollama pull " + m.name,
			Action:    finding.ActionRunCommand,
			Command:   command,
			NeedsSudo: folder.system,
			Warning:   warning,
		})
	}
	return out
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
