package apply

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
			wantErr: "fewer than 4 components",
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
		Tools: map[string]string{"npm": "/usr/bin/npm"},
		OnRun: func([]string) error { return os.Remove(filepath.Join(cache, "a")) },
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
		fake := &execx.Fake{Tools: map[string]string{"npm": "/usr/bin/npm"}, RunErr: errors.New("exit status 1")}
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
			actions:     []plan.Action{dockerAction(finding.ActionDockerRemoveImage, "sha256:multi", 40)},
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

func TestComponentCount(t *testing.T) {
	require.Equal(t, 0, componentCount("/"))
	require.Equal(t, 2, componentCount("/Users/me"))
	require.Equal(t, 4, componentCount("/Users/me/Code/app"))
}
