package apply

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/volume"
	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/dockerx"
	"github.com/mandloideep/reclaim/internal/execx"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/fsx"
	"github.com/mandloideep/reclaim/internal/plan"
)

// env is a fixture: a scan root and a fake home, both with resolved paths.
type env struct {
	root string
	home string
}

func newEnv(t *testing.T) env {
	t.Helper()
	resolve := func(p string) string {
		r, err := filepath.EvalSymlinks(p)
		require.NoError(t, err)
		return r
	}
	// The fake home sits as deep as a real one, /Users/me or /home/me, below
	// the temp directory, so the minimum depth rule never masks the rule a
	// test is about, even where the temp directory itself is shallow.
	home := filepath.Join(resolve(t.TempDir()), "home", "me")
	require.NoError(t, os.MkdirAll(home, 0o755))
	return env{root: filepath.Join(resolve(t.TempDir()), "Code"), home: home}
}

func write(t *testing.T, path string, size int) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, make([]byte, size), 0o644))
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func removeAction(path string, kind finding.Kind) plan.Action {
	return plan.Action{
		ID: finding.MakeID("test", path), Action: finding.ActionRemovePath, Path: path, Target: path,
		Kind: kind, Tier: finding.TierA, Scanner: "test",
	}
}

func dockerAction(action finding.Action, target string, size int64) plan.Action {
	return plan.Action{ID: finding.MakeID("docker", target), Action: action, Target: target, Tier: finding.TierA, Scanner: "docker", Size: size}
}

func withTags(a plan.Action, tags ...string) plan.Action {
	a.Tags = tags
	return a
}

func newPlan(roots []string, actions ...plan.Action) *plan.Plan {
	return &plan.Plan{Version: plan.Version, Created: time.Now(), Roots: roots, Actions: actions}
}

func run(t *testing.T, e env, p *plan.Plan, mod func(*Options)) (Summary, []map[string]any, error) {
	t.Helper()
	require.NoError(t, p.Validate(), "test plans must be valid plans")
	var log bytes.Buffer
	opts := Options{Home: e.home, Walker: fsx.NewWalker(2), Exec: &execx.Fake{}, Log: &log}
	if mod != nil {
		mod(&opts)
	}
	sum, err := Run(context.Background(), p, opts)
	var entries []map[string]any
	sc := bufio.NewScanner(&log)
	for sc.Scan() {
		var m map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &m))
		entries = append(entries, m)
	}
	return sum, entries, err
}

func TestRemovePathRemovesOnlyTheTarget(t *testing.T) {
	e := newEnv(t)
	app := filepath.Join(e.root, "app")
	target := filepath.Join(app, "node_modules")
	write(t, filepath.Join(target, "a", "index.js"), 300)
	write(t, filepath.Join(target, "b", "node_modules", "c", "x"), 700)
	keep := []string{
		filepath.Join(app, "package.json"),
		filepath.Join(app, "src", "index.js"),
		filepath.Join(app, "node_modules.bak", "x"),
		filepath.Join(e.root, "other", "node_modules", "y"),
	}
	for _, k := range keep {
		write(t, k, 10)
	}
	// A symlink inside the target pointing outside must be removed as a link,
	// never followed.
	outside := filepath.Join(e.home, "precious")
	write(t, filepath.Join(outside, "data"), 50)
	require.NoError(t, os.Symlink(outside, filepath.Join(target, "link")))

	a := removeAction(target, finding.KindDir)
	a.Project = app
	sum, log, err := run(t, e, newPlan([]string{e.root}, a), nil)
	require.NoError(t, err)
	require.Len(t, sum.Outcomes, 1)
	require.Equal(t, StatusDone, sum.Outcomes[0].Status)
	require.False(t, exists(target))
	for _, k := range keep {
		require.True(t, exists(k), "%s must survive", k)
	}
	require.True(t, exists(filepath.Join(outside, "data")), "symlink targets must survive")
	require.GreaterOrEqual(t, sum.Freed, int64(1000))
	require.Less(t, sum.Freed, int64(1000+4096), "the link counts as itself, not its target")

	require.Len(t, log, 3)
	require.Equal(t, "start", log[0]["event"])
	require.Equal(t, "action", log[1]["event"])
	require.Equal(t, "done", log[1]["status"])
	require.Equal(t, target, log[1]["path"])
	require.InDelta(t, float64(sum.Freed), log[1]["freed"], 0)
	require.NotEmpty(t, log[1]["time"])
	require.Equal(t, "end", log[2]["event"])
}

func TestRemoveFileTarget(t *testing.T) {
	e := newEnv(t)
	target := filepath.Join(e.root, "dl", "big.dmg")
	write(t, target, 123)
	write(t, filepath.Join(e.root, "dl", "small.dmg"), 1)
	sum, _, err := run(t, e, newPlan([]string{e.root}, removeAction(target, finding.KindFile)), nil)
	require.NoError(t, err)
	require.Equal(t, int64(123), sum.Freed)
	require.False(t, exists(target))
	require.True(t, exists(filepath.Join(e.root, "dl", "small.dmg")))
}

func TestRemovePathRefusals(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, e env) (plan.Action, []string)
		roots   func(e env) []string
		wantErr string
	}{
		{
			name: "outside the roots",
			setup: func(t *testing.T, e env) (plan.Action, []string) {
				p := filepath.Join(e.home, "elsewhere", "proj", "node_modules")
				write(t, filepath.Join(p, "x"), 1)
				return removeAction(p, finding.KindDir), []string{p}
			},
			wantErr: "outside the roots",
		},
		{
			name: "a root itself",
			setup: func(t *testing.T, e env) (plan.Action, []string) {
				p := filepath.Join(e.root, "proj")
				write(t, filepath.Join(p, "x"), 1)
				return removeAction(p, finding.KindDir), []string{p}
			},
			roots:   func(e env) []string { return []string{e.root, filepath.Join(e.root, "proj")} },
			wantErr: "scan root",
		},
		{
			name: "the project root",
			setup: func(t *testing.T, e env) (plan.Action, []string) {
				p := filepath.Join(e.root, "proj")
				write(t, filepath.Join(p, "package.json"), 1)
				a := removeAction(p, finding.KindDir)
				a.Project = p
				return a, []string{p}
			},
			wantErr: "project root",
		},
		{
			name: "a parent of the project root",
			setup: func(t *testing.T, e env) (plan.Action, []string) {
				p := filepath.Join(e.root, "group")
				write(t, filepath.Join(p, "proj", "package.json"), 1)
				a := removeAction(p, finding.KindDir)
				a.Project = filepath.Join(p, "proj")
				return a, []string{p}
			},
			wantErr: "project root",
		},
		{
			name: "a git working tree",
			setup: func(t *testing.T, e env) (plan.Action, []string) {
				p := filepath.Join(e.root, "proj", "dist")
				write(t, filepath.Join(p, ".git"), 1)
				return removeAction(p, finding.KindDir), []string{p}
			},
			wantErr: ".git entry",
		},
		{
			name: "inside a .git directory",
			setup: func(t *testing.T, e env) (plan.Action, []string) {
				p := filepath.Join(e.root, "proj", ".git", "objects")
				write(t, filepath.Join(p, "x"), 1)
				return removeAction(p, finding.KindDir), []string{p}
			},
			wantErr: "inside a .git directory",
		},
		{
			name: "a protected home folder",
			setup: func(t *testing.T, e env) (plan.Action, []string) {
				p := filepath.Join(e.home, "Library", "Caches")
				write(t, filepath.Join(p, "x"), 1)
				return removeAction(p, finding.KindDir), []string{p}
			},
			roots:   func(e env) []string { return []string{e.home} },
			wantErr: "protected folder",
		},
		{
			name: "a short path",
			setup: func(*testing.T, env) (plan.Action, []string) {
				return removeAction("/Users/someone", finding.KindDir), nil
			},
			roots:   func(env) []string { return []string{"/Users"} },
			wantErr: "fewer than 4 components",
		},
		{
			name: "a system path",
			setup: func(*testing.T, env) (plan.Action, []string) {
				return removeAction("/opt/homebrew", finding.KindDir), nil
			},
			roots:   func(env) []string { return []string{"/opt"} },
			wantErr: "protected system path",
		},
		{
			name: "the home directory",
			setup: func(_ *testing.T, e env) (plan.Action, []string) {
				return removeAction(e.home, finding.KindDir), []string{e.home}
			},
			roots:   func(e env) []string { return []string{filepath.Dir(e.home)} },
			wantErr: "home directory",
		},
		{
			name: "a target that became a symlink",
			setup: func(t *testing.T, e env) (plan.Action, []string) {
				victim := filepath.Join(e.home, "victim")
				write(t, filepath.Join(victim, "data"), 1)
				p := filepath.Join(e.root, "proj", "node_modules")
				require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
				require.NoError(t, os.Symlink(victim, p))
				return removeAction(p, finding.KindDir), []string{p, filepath.Join(victim, "data")}
			},
			wantErr: "symbolic link",
		},
		{
			name: "a parent that became a symlink",
			setup: func(t *testing.T, e env) (plan.Action, []string) {
				actual := filepath.Join(e.root, "real")
				write(t, filepath.Join(actual, "node_modules", "x"), 1)
				link := filepath.Join(e.root, "proj")
				require.NoError(t, os.Symlink(actual, link))
				return removeAction(filepath.Join(link, "node_modules"), finding.KindDir), []string{filepath.Join(actual, "node_modules", "x")}
			},
			wantErr: "symbolic link",
		},
		{
			name: "a project folder that holds a virtual environment",
			setup: func(t *testing.T, e env) (plan.Action, []string) {
				p := filepath.Join(e.root, "pyapp")
				write(t, filepath.Join(p, "pyproject.toml"), 1)
				write(t, filepath.Join(p, "pyvenv.cfg"), 1)
				return removeAction(p, finding.KindDir), []string{filepath.Join(p, "pyproject.toml")}
			},
			wantErr: "so it is a project",
		},
		{
			name: "a directory that became a file",
			setup: func(t *testing.T, e env) (plan.Action, []string) {
				p := filepath.Join(e.root, "proj", "build")
				write(t, p, 5)
				return removeAction(p, finding.KindDir), []string{p}
			},
			wantErr: "was a directory",
		},
		{
			name: "a file that became a directory",
			setup: func(t *testing.T, e env) (plan.Action, []string) {
				p := filepath.Join(e.root, "proj", "a.zip")
				write(t, filepath.Join(p, "x"), 5)
				return removeAction(p, finding.KindFile), []string{p}
			},
			wantErr: "was a file",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			require.NoError(t, os.MkdirAll(e.root, 0o755))
			a, mustSurvive := tt.setup(t, e)
			roots := []string{e.root}
			if tt.roots != nil {
				roots = tt.roots(e)
			}
			sum, log, err := run(t, e, newPlan(roots, a), nil)
			require.Error(t, err)
			require.Equal(t, StatusFailed, sum.Outcomes[0].Status)
			require.ErrorContains(t, sum.Outcomes[0].Err, tt.wantErr)
			require.Zero(t, sum.Freed)
			for _, p := range mustSurvive {
				require.True(t, exists(p), "%s must survive", p)
			}
			require.Equal(t, "failed", log[1]["status"])
			require.Contains(t, log[1]["error"], tt.wantErr)
		})
	}
}

func TestGoneTargetIsNotAnError(t *testing.T) {
	e := newEnv(t)
	sum, _, err := run(t, e, newPlan([]string{e.root}, removeAction(filepath.Join(e.root, "proj", "node_modules"), finding.KindDir)), nil)
	require.NoError(t, err)
	require.Equal(t, StatusGone, sum.Outcomes[0].Status)
}

func TestStopOnFirstFailureUnlessKeepGoing(t *testing.T) {
	for _, keepGoing := range []bool{false, true} {
		t.Run(map[bool]string{false: "stop", true: "keep going"}[keepGoing], func(t *testing.T) {
			e := newEnv(t)
			bad := filepath.Join(e.root, "proj", "dist")
			write(t, filepath.Join(bad, ".git"), 1)
			good := filepath.Join(e.root, "proj", "node_modules")
			write(t, filepath.Join(good, "x"), 1)
			sum, _, err := run(t, e, newPlan([]string{e.root}, removeAction(bad, finding.KindDir), removeAction(good, finding.KindDir)),
				func(o *Options) { o.KeepGoing = keepGoing })
			require.ErrorContains(t, err, "1 of 2 actions failed")
			require.Equal(t, StatusFailed, sum.Outcomes[0].Status)
			if keepGoing {
				require.Equal(t, StatusDone, sum.Outcomes[1].Status)
				require.False(t, exists(good))
			} else {
				require.Equal(t, StatusNotRun, sum.Outcomes[1].Status)
				require.True(t, exists(good))
			}
		})
	}
}

func TestRemovesReadOnlyDirectories(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permission bits")
	}
	e := newEnv(t)
	target := filepath.Join(e.root, "proj", ".cache", "gomod")
	inner := filepath.Join(target, "github.com", "x@v1.0.0")
	write(t, filepath.Join(inner, "go.mod"), 20)
	require.NoError(t, os.Chmod(inner, 0o555))
	require.NoError(t, os.Chmod(filepath.Dir(inner), 0o555))
	sibling := filepath.Join(e.root, "proj", ".cache", "other")
	write(t, filepath.Join(sibling, "f"), 1)
	t.Cleanup(func() {
		_ = os.Chmod(filepath.Dir(inner), 0o755)
		_ = os.Chmod(inner, 0o755)
	})

	sum, _, err := run(t, e, newPlan([]string{e.root}, removeAction(target, finding.KindDir)), nil)
	require.NoError(t, err)
	require.Equal(t, StatusDone, sum.Outcomes[0].Status)
	require.False(t, exists(target))
	require.True(t, exists(filepath.Join(sibling, "f")))
}

func commandAction(path string, argv ...string) plan.Action {
	return plan.Action{
		ID: finding.MakeID("npm-cache", path), Action: finding.ActionRunCommand, Path: path, Target: path,
		Tier: finding.TierB, Scanner: "npm-cache", Command: argv,
	}
}

func TestRunCommand(t *testing.T) {
	e := newEnv(t)
	cache := filepath.Join(e.home, ".npm", "_cacache")
	write(t, filepath.Join(cache, "a"), 400)
	fake := &execx.Fake{
		Tools:   map[string]string{"npm": "/usr/bin/npm"},
		Outputs: map[string]string{"npm config get cache": filepath.Dir(cache)},
		OnRun:   func([]string) error { return os.Remove(filepath.Join(cache, "a")) },
	}
	sum, log, err := run(t, e, newPlan([]string{e.root, e.home}, commandAction(cache, "npm", "cache", "clean", "--force")),
		func(o *Options) { o.Exec = fake })
	require.NoError(t, err)
	require.Equal(t, StatusDone, sum.Outcomes[0].Status)
	require.Equal(t, int64(400), sum.Freed, "freed is measured before and after")
	require.Equal(t, [][]string{{"npm", "cache", "clean", "--force"}}, fake.Ran())
	require.Equal(t, "ok", log[1]["output"])

	t.Run("tool missing", func(t *testing.T) {
		fake := &execx.Fake{}
		sum, _, err := run(t, e, newPlan(nil, commandAction(cache, "npm", "cache", "clean", "--force")), func(o *Options) { o.Exec = fake })
		require.Error(t, err)
		require.ErrorIs(t, sum.Outcomes[0].Err, execx.ErrNotInstalled)
		require.Empty(t, fake.Ran())
	})
	t.Run("command fails", func(t *testing.T) {
		fake := &execx.Fake{Tools: map[string]string{"npm": "/usr/bin/npm"}, RunErr: errors.New("exit status 1"),
			Outputs: map[string]string{"npm config get cache": filepath.Dir(cache)}}
		sum, _, err := run(t, e, newPlan(nil, commandAction(cache, "npm", "cache", "clean", "--force")), func(o *Options) { o.Exec = fake })
		require.Error(t, err)
		require.Equal(t, StatusFailed, sum.Outcomes[0].Status)
	})
	t.Run("commands outside the allowlist never run", func(t *testing.T) {
		fake := &execx.Fake{Tools: map[string]string{"rm": "/bin/rm"}}
		p := newPlan(nil, commandAction(cache, "rm", "-rf", cache))
		// Bypass plan validation, which already refuses this, to prove apply
		// refuses it on its own as well.
		sum, err := Run(context.Background(), p, Options{Exec: fake, Walker: fsx.NewWalker(1)})
		require.Error(t, err)
		require.ErrorContains(t, sum.Outcomes[0].Err, "not an allowed clean command")
		require.Empty(t, fake.Ran())
	})
}

// TestRunCommandAsksAgainWhereTheCacheIs checks that apply asks the tool for
// its cache location right before running its clean command and refuses
// when the answer is not the directory the plan was made for.
func TestRunCommandAsksAgainWhereTheCacheIs(t *testing.T) {
	e := newEnv(t)
	cache := filepath.Join(e.home, ".npm", "_cacache")
	write(t, filepath.Join(cache, "a"), 400)
	moved := filepath.Join(e.home, "elsewhere", "npm")
	write(t, filepath.Join(moved, "_cacache", "b"), 10)
	link := filepath.Join(e.home, "npm-link")
	require.NoError(t, os.Symlink(filepath.Dir(cache), link))

	tests := []struct {
		name     string
		answer   string
		queryErr bool
		action   func() plan.Action
		want     Status
		wantErr  string
	}{
		{name: "same place", answer: filepath.Dir(cache), want: StatusDone},
		{name: "same place through a symbolic link", answer: link, want: StatusDone},
		{name: "same place after notices", answer: "npm notice: new version\n" + filepath.Dir(cache), want: StatusDone},
		{name: "moved", answer: moved, want: StatusFailed, wantErr: "now reports " + filepath.Join(moved, "_cacache")},
		{name: "moved somewhere missing", answer: filepath.Join(e.home, "nowhere"), want: StatusFailed, wantErr: "now reports"},
		{name: "no answer", answer: "not a path", want: StatusFailed, wantErr: "did not report a cache directory"},
		{name: "query fails", queryErr: true, want: StatusFailed, wantErr: "could not ask npm"},
		{name: "plan without a path", answer: filepath.Dir(cache), action: func() plan.Action {
			a := commandAction(cache, "npm", "cache", "clean", "--force")
			a.Path = ""
			return a
		}, want: StatusFailed, wantErr: "does not record the directory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &execx.Fake{Tools: map[string]string{"npm": "/usr/bin/npm"}, Outputs: map[string]string{}}
			if !tt.queryErr {
				fake.Outputs["npm config get cache"] = tt.answer
			}
			a := commandAction(cache, "npm", "cache", "clean", "--force")
			if tt.action != nil {
				a = tt.action()
			}
			sum, _, err := run(t, e, newPlan(nil, a), func(o *Options) { o.Exec = fake })
			require.Equal(t, tt.want, sum.Outcomes[0].Status)
			if tt.wantErr != "" {
				require.Error(t, err)
				require.ErrorContains(t, sum.Outcomes[0].Err, tt.wantErr)
				require.Empty(t, fake.Ran(), "the clean command never runs when the cache moved")
				return
			}
			require.NoError(t, err)
			require.Equal(t, [][]string{{"npm", "cache", "clean", "--force"}}, fake.Ran())
		})
	}

	t.Run("the cache is gone", func(t *testing.T) {
		gone := filepath.Join(e.home, ".npm2", "_cacache")
		fake := &execx.Fake{Tools: map[string]string{"npm": "/usr/bin/npm"}, Outputs: map[string]string{"npm config get cache": filepath.Dir(gone)}}
		sum, _, err := run(t, e, newPlan(nil, commandAction(gone, "npm", "cache", "clean", "--force")), func(o *Options) { o.Exec = fake })
		require.NoError(t, err)
		require.Equal(t, StatusGone, sum.Outcomes[0].Status)
		require.Empty(t, fake.Ran())
	})

}

func ollamaAction(models, model string) plan.Action {
	return plan.Action{ID: finding.MakeID("ollama", plan.OllamaTarget(model)), Action: finding.ActionRunCommand, Path: models,
		Target: plan.OllamaTarget(model), Tier: finding.TierB, Scanner: "ollama", Command: []string{"ollama", "rm", model}}
}

// TestOllamaRemove checks that ollama rm runs only for a model whose
// manifest is still in the scanned folder, only against this machine and
// only while the system wide service holds no model of the same name, and
// that it frees what the command removed and nothing else.
func TestOllamaRemove(t *testing.T) {
	e := newEnv(t)
	models := filepath.Join(e.home, ".ollama", "models")
	rel := filepath.Join("manifests", "registry.ollama.ai", "library", "llama3", "latest")
	manifest := filepath.Join(models, rel)
	system := filepath.Join(t.TempDir(), "ollama", ".ollama", "models")
	tests := []struct {
		name     string
		host     string
		manifest bool
		// system is the state of the system wide models folder: "" for none,
		// "other" with another model, "same" with this model, "unreadable".
		system  string
		want    Status
		wantErr string
	}{
		{name: "local", manifest: true, want: StatusDone},
		{name: "system folder with other models", manifest: true, system: "other", want: StatusDone},
		{name: "system folder with the same model", manifest: true, system: "same", want: StatusFailed, wantErr: "has a model of the same name"},
		{name: "unreadable system folder", manifest: true, system: "unreadable", want: StatusFailed, wantErr: "cannot check the system Ollama models"},
		{name: "loopback host", host: "http://127.0.0.1:11434", manifest: true, want: StatusDone},
		{name: "localhost", host: "localhost:11434", manifest: true, want: StatusDone},
		{name: "remote host", host: "gpu-box.lan:11434", manifest: true, want: StatusFailed, wantErr: "not this machine"},
		{name: "model already gone", want: StatusGone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, os.RemoveAll(models))
			_ = os.Chmod(filepath.Dir(system), 0o755)
			require.NoError(t, os.RemoveAll(system))
			switch tt.system {
			case "other":
				write(t, filepath.Join(system, "manifests", "registry.ollama.ai", "library", "mistral", "latest"), 10)
			case "same":
				write(t, filepath.Join(system, rel), 10)
			case "unreadable":
				if os.Geteuid() == 0 {
					t.Skip("root reads every folder")
				}
				write(t, filepath.Join(system, rel), 10)
				require.NoError(t, os.Chmod(filepath.Dir(system), 0o000))
				t.Cleanup(func() { _ = os.Chmod(filepath.Dir(system), 0o755) })
			}
			write(t, filepath.Join(models, "blobs", "sha256-1"), 500)
			write(t, filepath.Join(models, "blobs", "sha256-2"), 70)
			if tt.manifest {
				write(t, manifest, 10)
			}
			fake := &execx.Fake{
				Tools: map[string]string{"ollama": "/usr/local/bin/ollama"},
				OnRun: func([]string) error {
					if err := os.Remove(manifest); err != nil {
						return err
					}
					return os.Remove(filepath.Join(models, "blobs", "sha256-1"))
				},
			}
			env := func(k string) string {
				if k == "OLLAMA_HOST" {
					return tt.host
				}
				return ""
			}
			sum, _, err := run(t, e, newPlan(nil, ollamaAction(models, "llama3:latest")), func(o *Options) {
				o.Exec = fake
				o.Getenv = env
				o.OllamaSystemModels = system
			})
			require.Equal(t, tt.want, sum.Outcomes[0].Status)
			if tt.want != StatusDone {
				require.Empty(t, fake.Ran())
				if tt.wantErr != "" {
					require.Error(t, err)
					require.ErrorContains(t, sum.Outcomes[0].Err, tt.wantErr)
				}
				return
			}
			require.NoError(t, err)
			require.Equal(t, int64(510), sum.Freed, "the manifest and the blob only this model used")
			require.Equal(t, [][]string{{"ollama", "rm", "llama3:latest"}}, fake.Ran())
			require.True(t, exists(filepath.Join(models, "blobs", "sha256-2")))
			if tt.system == "other" {
				require.True(t, exists(filepath.Join(system, "manifests", "registry.ollama.ai", "library", "mistral", "latest")))
			}
		})
	}
}

// TestOllamaSystemModelsAreManual checks that a model of the system wide
// service is printed for the user and never run, even when its manifest is
// there.
func TestOllamaSystemModelsAreManual(t *testing.T) {
	e := newEnv(t)
	system := filepath.Join(e.home, "system", "models")
	write(t, filepath.Join(system, "manifests", "registry.ollama.ai", "library", "llama3", "latest"), 10)
	a := ollamaAction(system, "llama3:latest")
	a.NeedsSudo = true
	fake := &execx.Fake{Tools: map[string]string{"ollama": "/usr/local/bin/ollama"}}
	sum, _, err := run(t, e, newPlan(nil, a), func(o *Options) { o.Exec = fake; o.OllamaSystemModels = system })
	require.NoError(t, err)
	require.Equal(t, StatusManual, sum.Outcomes[0].Status)
	require.Empty(t, fake.Ran())
	require.True(t, exists(filepath.Join(system, "manifests", "registry.ollama.ai", "library", "llama3", "latest")))
}

func TestIsLocalHost(t *testing.T) {
	for host, want := range map[string]bool{
		"": true, "localhost": true, "127.0.0.1": true, "127.0.0.1:11434": true, "[::1]:11434": true, "0.0.0.0": true,
		"http://localhost:11434": true, "https://10.0.0.5:11434": false, "example.com": false, "192.168.1.9:11434": false,
	} {
		require.Equal(t, want, isLocalHost(host), host)
	}
}

// TestSimulatorDelete checks that xcrun simctl delete runs only for a
// simulator that is still listed as unavailable, and that only its folder
// goes.
func TestSimulatorDelete(t *testing.T) {
	e := newEnv(t)
	udid := "11111111-2222-4333-8444-555555555555"
	devices := filepath.Join(e.home, "Library", "Developer", "CoreSimulator", "Devices")
	dir := filepath.Join(devices, udid)
	other := filepath.Join(devices, "AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE")
	list := func(available bool) string {
		return `{"devices":{"com.apple.CoreSimulator.SimRuntime.iOS-16-4":[{"udid":"` + udid + `","isAvailable":` +
			strconv.FormatBool(available) + `}]}}`
	}
	tests := []struct {
		name    string
		list    string
		want    Status
		wantErr string
	}{
		{name: "unavailable", list: list(false), want: StatusDone},
		{name: "available again", list: list(true), want: StatusFailed, wantErr: "available again"},
		{name: "no longer listed", list: `{"devices":{}}`, want: StatusGone},
		{name: "list fails", want: StatusFailed, wantErr: "could not list simulators"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			write(t, filepath.Join(dir, "data", "x"), 700)
			write(t, filepath.Join(other, "data", "x"), 900)
			fake := &execx.Fake{
				Tools:   map[string]string{"xcrun": "/usr/bin/xcrun"},
				Outputs: map[string]string{},
				OnRun:   func([]string) error { return os.RemoveAll(dir) },
			}
			if tt.list != "" {
				fake.Outputs["xcrun simctl list -j devices"] = tt.list
			}
			a := plan.Action{ID: finding.MakeID("simulator-devices", dir), Action: finding.ActionRunCommand, Path: dir, Target: dir,
				Tier: finding.TierB, Scanner: "simulator-devices", Command: []string{"xcrun", "simctl", "delete", udid}}
			sum, _, err := run(t, e, newPlan(nil, a), func(o *Options) { o.Exec = fake })
			require.Equal(t, tt.want, sum.Outcomes[0].Status)
			require.True(t, exists(filepath.Join(other, "data", "x")), "other simulators survive")
			if tt.want != StatusDone {
				require.Empty(t, fake.Ran())
				require.True(t, exists(dir))
				if tt.wantErr != "" {
					require.Error(t, err)
					require.ErrorContains(t, sum.Outcomes[0].Err, tt.wantErr)
				}
				return
			}
			require.NoError(t, err)
			require.Equal(t, int64(700), sum.Freed)
			require.False(t, exists(dir))
		})
	}
}

func TestAttentionFindingsAreNeverApplied(t *testing.T) {
	e := newEnv(t)
	target := filepath.Join(e.root, "Downloads", "old.mov")
	write(t, target, 100)
	a := removeAction(target, finding.KindFile)
	a.Action = finding.ActionNone
	// Plan validation refuses it; apply refuses it on its own as well.
	sum, err := Run(context.Background(), newPlan([]string{e.root}, a), Options{Home: e.home, Exec: &execx.Fake{}, Walker: fsx.NewWalker(1)})
	require.Error(t, err)
	require.Equal(t, StatusFailed, sum.Outcomes[0].Status)
	require.ErrorContains(t, sum.Outcomes[0].Err, "cannot be applied")
	require.True(t, exists(target))
}

func TestNeedsSudoIsNeverExecuted(t *testing.T) {
	e := newEnv(t)
	fake := &execx.Fake{Tools: map[string]string{"brew": "/opt/homebrew/bin/brew"}}
	a := commandAction(filepath.Join(e.home, "x"), "brew", "cleanup", "-s")
	a.NeedsSudo = true
	sum, log, err := run(t, e, newPlan(nil, a), func(o *Options) { o.Exec = fake })
	require.NoError(t, err)
	require.Equal(t, StatusManual, sum.Outcomes[0].Status)
	require.Empty(t, fake.Ran())
	require.Equal(t, "manual", log[1]["status"])
}

func dockerFixture() *dockerx.Fake {
	return &dockerx.Fake{
		Containers: []container.Summary{
			{ID: "c-stopped", ImageID: "sha256:old", State: container.StateExited,
				Mounts: []container.MountPoint{{Name: "vol-stopped"}}},
			{ID: "c-running", ImageID: "sha256:live", State: container.StateRunning,
				Mounts: []container.MountPoint{{Name: "vol-live"}}},
		},
		Images: []image.Summary{
			{ID: "sha256:old", RepoTags: []string{"old:1"}},
			{ID: "sha256:live", RepoTags: []string{"live:1"}},
			{ID: "sha256:multi", RepoTags: []string{"busybox:latest", "busybox:1.36"}},
			{ID: "sha256:dangling", RepoTags: []string{"<none>:<none>"}},
			{ID: "sha256:bystander", RepoTags: []string{"keep:me"}},
			{ID: "sha256:base", RepoTags: []string{"base:1"}},
			{ID: "sha256:child", RepoTags: []string{"child:1"}, ParentID: "sha256:base"},
		},
		Volumes: []volume.Volume{{Name: "vol-stopped"}, {Name: "vol-live"}, {Name: "vol-free"}, {Name: "vol-bystander"}},
		BuildCache: []build.CacheRecord{
			{ID: "r1", Size: 300},
			{ID: "r2", Size: 100, InUse: true},
		},
	}
}

func TestDockerActions(t *testing.T) {
	tests := []struct {
		name        string
		actions     []plan.Action
		wantStatus  []Status
		wantRemoved []string
		wantFreed   int64
		wantErr     string
	}{
		{
			name:        "stopped container then its volume",
			actions:     []plan.Action{dockerAction(finding.ActionDockerRemoveContainer, "c-stopped", 10), dockerAction(finding.ActionDockerRemoveVolume, "vol-stopped", 20)},
			wantStatus:  []Status{StatusDone, StatusDone},
			wantRemoved: []string{"container:c-stopped", "volume:vol-stopped"},
			wantFreed:   30,
		},
		{
			name:       "running container is refused",
			actions:    []plan.Action{dockerAction(finding.ActionDockerRemoveContainer, "c-running", 10)},
			wantStatus: []Status{StatusFailed},
			wantErr:    "running now",
		},
		{
			name:       "image used by a container is refused",
			actions:    []plan.Action{dockerAction(finding.ActionDockerRemoveImage, "sha256:old", 10)},
			wantStatus: []Status{StatusFailed},
			wantErr:    "uses it now",
		},
		{
			name:        "image with several tags is untagged then removed",
			actions:     []plan.Action{withTags(dockerAction(finding.ActionDockerRemoveImage, "sha256:multi", 40), "busybox:1.36", "busybox:latest")},
			wantStatus:  []Status{StatusDone},
			wantRemoved: []string{"untag:busybox:latest", "image:busybox:1.36"},
			wantFreed:   40,
		},
		{
			name:        "dangling image",
			actions:     []plan.Action{dockerAction(finding.ActionDockerRemoveImage, "sha256:dangling", 5)},
			wantStatus:  []Status{StatusDone},
			wantRemoved: []string{"image:sha256:dangling"},
			wantFreed:   5,
		},
		{
			name:       "image tagged since the scan is refused",
			actions:    []plan.Action{withTags(dockerAction(finding.ActionDockerRemoveImage, "sha256:multi", 40), "busybox:latest")},
			wantStatus: []Status{StatusFailed},
			wantErr:    "tags changed since the scan",
		},
		{
			name:       "image other images are built on is refused before untagging",
			actions:    []plan.Action{withTags(dockerAction(finding.ActionDockerRemoveImage, "sha256:base", 40), "base:1")},
			wantStatus: []Status{StatusFailed},
			wantErr:    "is built on it",
		},
		{
			name:       "volume of a running container is refused",
			actions:    []plan.Action{dockerAction(finding.ActionDockerRemoveVolume, "vol-live", 10)},
			wantStatus: []Status{StatusFailed},
			wantErr:    "running container",
		},
		{
			name:       "volume of a stopped container is refused by the daemon",
			actions:    []plan.Action{dockerAction(finding.ActionDockerRemoveVolume, "vol-stopped", 10)},
			wantStatus: []Status{StatusFailed},
			wantErr:    "in use",
		},
		{
			name:        "unused volume",
			actions:     []plan.Action{dockerAction(finding.ActionDockerRemoveVolume, "vol-free", 10)},
			wantStatus:  []Status{StatusDone},
			wantRemoved: []string{"volume:vol-free"},
			wantFreed:   10,
		},
		{
			name:        "build cache reports what the daemon freed",
			actions:     []plan.Action{dockerAction(finding.ActionDockerPruneBuildCache, "docker-build-cache", 999)},
			wantStatus:  []Status{StatusDone},
			wantRemoved: []string{"build-cache"},
			wantFreed:   300,
		},
		{
			name: "objects already gone",
			actions: []plan.Action{
				dockerAction(finding.ActionDockerRemoveContainer, "c-gone", 1),
				dockerAction(finding.ActionDockerRemoveImage, "sha256:gone", 1),
				dockerAction(finding.ActionDockerRemoveVolume, "vol-gone", 1),
			},
			wantStatus: []Status{StatusGone, StatusGone, StatusGone},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			fake := dockerFixture()
			sum, _, err := run(t, e, newPlan(nil, tt.actions...), func(o *Options) {
				o.Docker = func() (dockerx.API, error) { return fake, nil }
			})
			if tt.wantErr != "" {
				require.Error(t, err)
				require.ErrorContains(t, sum.Outcomes[0].Err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			got := make([]Status, 0, len(sum.Outcomes))
			for _, o := range sum.Outcomes {
				got = append(got, o.Status)
			}
			require.Equal(t, tt.wantStatus, got)
			require.Equal(t, tt.wantRemoved, fake.Removed(), "exactly the planned objects are removed")
			require.Equal(t, tt.wantFreed, sum.Freed)
		})
	}
}

func TestDockerUnavailable(t *testing.T) {
	e := newEnv(t)
	calls := 0
	sum, _, err := run(t, e, newPlan(nil,
		dockerAction(finding.ActionDockerRemoveImage, "a", 1),
		dockerAction(finding.ActionDockerRemoveImage, "b", 1),
	), func(o *Options) {
		o.KeepGoing = true
		o.Docker = func() (dockerx.API, error) {
			calls++
			return nil, errors.New("cannot connect")
		}
	})
	require.Error(t, err)
	require.Equal(t, 2, sum.Failed)
	require.Equal(t, 1, calls, "the client is created once")
}

func TestCanceledRunDoesNothing(t *testing.T) {
	e := newEnv(t)
	target := filepath.Join(e.root, "proj", "node_modules")
	write(t, filepath.Join(target, "x"), 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sum, err := Run(ctx, newPlan([]string{e.root}, removeAction(target, finding.KindDir)), Options{Home: e.home})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, StatusNotRun, sum.Outcomes[0].Status)
	require.True(t, exists(target))
}

func TestProgressIsReported(t *testing.T) {
	e := newEnv(t)
	var seen []int
	_, _, err := run(t, e, newPlan([]string{e.root},
		removeAction(filepath.Join(e.root, "p", "a"), finding.KindDir),
		removeAction(filepath.Join(e.root, "p", "b"), finding.KindDir),
	), func(o *Options) {
		o.Progress = func(i int, _ Outcome) { seen = append(seen, i) }
	})
	require.NoError(t, err)
	require.Equal(t, []int{0, 1}, seen)
}

func TestRemoveInRootRefusesReplacedTargets(t *testing.T) {
	e := newEnv(t)
	target := filepath.Join(e.root, "proj", "node_modules")
	write(t, filepath.Join(target, "x"), 1)
	checked, err := os.Lstat(target)
	require.NoError(t, err)

	// Swap the directory for a different one with the same name.
	require.NoError(t, os.Rename(target, target+".old"))
	write(t, filepath.Join(target, "y"), 1)
	err = removeInRoot(e.root, target, checked)
	require.ErrorContains(t, err, "replaced")
	require.FileExists(t, filepath.Join(target, "y"))
	require.FileExists(t, filepath.Join(target+".old", "x"))

	// Swap a parent for a symbolic link to a look-alike tree.
	require.NoError(t, os.RemoveAll(target))
	require.NoError(t, os.Rename(target+".old", target))
	checked, err = os.Lstat(target)
	require.NoError(t, err)
	other := filepath.Join(e.root, "other")
	write(t, filepath.Join(other, "node_modules", "z"), 1)
	require.NoError(t, os.Rename(filepath.Join(e.root, "proj"), filepath.Join(e.root, "proj.real")))
	require.NoError(t, os.Symlink(other, filepath.Join(e.root, "proj")))
	err = removeInRoot(e.root, target, checked)
	require.ErrorContains(t, err, "changed since it was checked")
	require.FileExists(t, filepath.Join(other, "node_modules", "z"))
}

func TestContainingRoot(t *testing.T) {
	roots := []string{"/a", "/a/b", "/c"}
	require.Equal(t, "/a/b", containingRoot("/a/b/x/y", roots))
	require.Equal(t, "/a", containingRoot("/a/x", roots))
	require.Equal(t, "", containingRoot("/d/x", roots))
	require.Equal(t, "/a", containingRoot("/a/b", roots), "a root is held by its parent root, never by itself")
}

func TestComponentCount(t *testing.T) {
	require.Equal(t, 0, componentCount("/"))
	require.Equal(t, 2, componentCount("/Users/me"))
	require.Equal(t, 4, componentCount("/Users/me/Code/app"))
}
