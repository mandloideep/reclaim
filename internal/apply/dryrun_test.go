package apply

import (
	"path/filepath"
	"testing"

	"github.com/docker/docker/api/types/image"
	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/dockerx"
	"github.com/mandloideep/reclaim/internal/execx"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/plan"
)

// dryRunFixture builds a plan with an action of every kind apply knows, some
// that pass every check, one that is gone and some that a real run refuses.
func dryRunFixture(t *testing.T, e env) (*plan.Plan, *execx.Fake, *dockerx.Fake) {
	t.Helper()
	nm := filepath.Join(e.root, "web", "node_modules")
	write(t, filepath.Join(e.root, "web", "package.json"), 1)
	write(t, filepath.Join(nm, "react", "index.js"), 300)
	file := filepath.Join(e.root, "dl", "setup.dmg")
	write(t, file, 50)
	emptyDir := filepath.Join(e.root, "task")
	write(t, filepath.Join(emptyDir, ".keep-me"), 1) // not empty, so refused
	project := filepath.Join(e.root, "lib")
	write(t, filepath.Join(project, "package.json"), 1)
	refused := removeAction(project, finding.KindDir) // holds package.json
	gone := removeAction(filepath.Join(e.root, "old", "node_modules"), finding.KindDir)
	empty := removeAction(emptyDir, finding.KindEmptyDir)
	cache := filepath.Join(e.home, ".npm", "_cacache")
	write(t, filepath.Join(cache, "a"), 400)
	const runtime = "6F3C8F5A-1C2B-4D8E-9A1B-2C3D4E5F6A7B"
	sudo := plan.Action{ID: finding.MakeID("simulator-runtimes", plan.SimulatorRuntimeTarget(runtime)), Action: finding.ActionRunCommand,
		Target: plan.SimulatorRuntimeTarget(runtime), Tier: finding.TierB, Scanner: "simulator-runtimes", NeedsSudo: true,
		Command: []string{"xcrun", "simctl", "runtime", "delete", runtime}}
	p := newPlan([]string{e.root, e.home},
		dockerAction(finding.ActionDockerRemoveContainer, "c-stopped", 10),
		withTags(dockerAction(finding.ActionDockerRemoveImage, "sha256:old", 10), "old:1"),
		dockerAction(finding.ActionDockerRemoveVolume, "vol-free", 20),
		dockerAction(finding.ActionDockerPruneBuildCache, "docker-build-cache", 300),
		removeAction(nm, finding.KindDir),
		removeAction(file, finding.KindFile),
		empty,
		refused,
		gone,
		commandAction(cache, "npm", "cache", "clean", "--force"),
		sudo,
	)
	ex := &execx.Fake{
		Tools:   map[string]string{"npm": "/usr/bin/npm"},
		Outputs: map[string]string{"npm config get cache": filepath.Dir(cache)},
	}
	return p, ex, dockerFixture()
}

// TestDryRunRemovesNothing proves a dry run runs every check of every
// action, reports checked, gone, manual or refused for each, logs that it
// is a dry run, and changes nothing: no file, no command, no Docker object.
func TestDryRunRemovesNothing(t *testing.T) {
	e := newEnv(t)
	p, ex, dk := dryRunFixture(t, e)
	before := snapshot(t, filepath.Dir(e.root))
	homeBefore := snapshot(t, e.home)
	sum, log, err := run(t, e, p, func(o *Options) {
		o.DryRun = true
		o.Exec = ex
		o.Docker = func() (dockerx.API, error) { return dk, nil }
	})
	require.ErrorContains(t, err, "2 of 11 actions would be refused")
	require.True(t, sum.DryRun)
	statuses := make([]Status, len(sum.Outcomes))
	for i, o := range sum.Outcomes {
		statuses[i] = o.Status
		require.NotEmpty(t, o.Checks, "every outcome carries its checks: %s", o.Action.Label())
		require.Zero(t, o.Freed)
		if o.Status == StatusRefused {
			require.False(t, o.Checks[len(o.Checks)-1].OK, "the failed check is the last")
			require.Error(t, o.Err)
		}
	}
	require.Equal(t, []Status{
		StatusChecked, StatusChecked, StatusChecked, StatusChecked, // Docker: the image is freed by the container before it
		StatusChecked, StatusChecked, StatusRefused, StatusRefused, StatusGone, // paths
		StatusChecked, StatusManual, // the clean command and the sudo one
	}, statuses)

	// Nothing changed anywhere.
	require.Equal(t, before, snapshot(t, filepath.Dir(e.root)))
	require.Equal(t, homeBefore, snapshot(t, e.home))
	require.Empty(t, ex.Ran(), "no command ran")
	require.Empty(t, dk.Removed(), "no Docker object was removed or pruned")
	require.Len(t, dk.BuildCache, 2, "the build cache was not pruned")

	// The log says it is a dry run, and every action entry its status and
	// checks.
	require.Equal(t, "start", log[0]["event"])
	require.Equal(t, true, log[0]["dry_run"])
	for _, entry := range log[1 : len(log)-1] {
		require.Contains(t, []any{"checked", "refused", "gone", "manual"}, entry["status"])
		require.NotEmpty(t, entry["checks"])
	}
	require.Equal(t, true, log[len(log)-1]["dry_run"])

	// The checks of a folder that passed, by name and in order.
	names := func(cs []Check) []string {
		out := make([]string, len(cs))
		for i, c := range cs {
			out[i] = c.Name
		}
		return out
	}
	require.Equal(t, []string{CheckRules, CheckExcluded, CheckObject, CheckMounts, CheckLastLook}, names(sum.Outcomes[4].Checks))
	require.Equal(t, []string{CheckAllowed, CheckExcluded, CheckTool, CheckLocation}, names(sum.Outcomes[9].Checks))
	require.Equal(t, []string{CheckExcluded, CheckDaemon, CheckDockerUse, CheckTags}, names(sum.Outcomes[1].Checks), "the image the container before it uses counts as unused")
}

// TestDryRunReportsWhatARealRunDoes runs the same plan dry and then for
// real: every action a dry run refuses fails in the real run, and every
// action it checked is done.
func TestDryRunReportsWhatARealRunDoes(t *testing.T) {
	e := newEnv(t)
	p, ex, dk := dryRunFixture(t, e)
	mod := func(dry bool) func(*Options) {
		return func(o *Options) {
			o.DryRun, o.KeepGoing = dry, true
			o.Exec = ex
			o.Docker = func() (dockerx.API, error) { return dk, nil }
		}
	}
	dry, _, _ := run(t, e, p, mod(true))
	actual, _, _ := run(t, e, p, mod(false))
	for i := range dry.Outcomes {
		want := map[Status]Status{StatusChecked: StatusDone, StatusRefused: StatusFailed, StatusGone: StatusGone, StatusManual: StatusManual}[dry.Outcomes[i].Status]
		require.Equal(t, want, actual.Outcomes[i].Status, p.Actions[i].Label())
		if dry.Outcomes[i].Err != nil {
			require.Equal(t, dry.Outcomes[i].Err.Error(), actual.Outcomes[i].Err.Error(), "the same refusal")
		}
	}
	require.False(t, exists(p.Actions[4].Path), "the real run removed what the dry run checked")
	require.True(t, exists(p.Actions[7].Path), "and kept what it refused")
}

// TestExcludedTargetsAreRefused proves apply refuses a target that is, lies
// under or holds an exclude entry, whatever the plan says, and removes
// nothing of it.
func TestExcludedTargetsAreRefused(t *testing.T) {
	e := newEnv(t)
	keep := filepath.Join(e.root, "archive")
	inside := filepath.Join(keep, "old", "node_modules")
	write(t, filepath.Join(inside, "x"), 10)
	holder := filepath.Join(e.root, "big")
	nested := filepath.Join(holder, "keep")
	write(t, filepath.Join(nested, "y"), 10)
	cache := filepath.Join(e.home, ".npm", "_cacache")
	write(t, filepath.Join(cache, "a"), 10)
	ex := &execx.Fake{Tools: map[string]string{"npm": "/usr/bin/npm"}, Outputs: map[string]string{"npm config get cache": filepath.Dir(cache)}}
	p := newPlan([]string{e.root, e.home},
		removeAction(inside, finding.KindDir),
		removeAction(holder, finding.KindDir),
		commandAction(cache, "npm", "cache", "clean", "--force"),
	)
	for _, dry := range []bool{true, false} {
		sum, _, err := run(t, e, p, func(o *Options) {
			o.DryRun, o.KeepGoing, o.Exec = dry, true, ex
			o.Excluded = Exclusions{Exclude: []string{keep, nested, cache}}
		})
		require.Error(t, err)
		for _, o := range sum.Outcomes {
			require.Contains(t, []Status{StatusFailed, StatusRefused}, o.Status)
			require.ErrorContains(t, o.Err, "exclude entry")
			require.Equal(t, CheckExcluded, o.Checks[len(o.Checks)-1].Name)
		}
	}
	require.True(t, exists(filepath.Join(inside, "x")))
	require.True(t, exists(filepath.Join(nested, "y")))
	require.True(t, exists(filepath.Join(cache, "a")))
	require.Empty(t, ex.Ran())
}

// TestIgnoredTargetsAreRefused proves apply refuses what the ignore list
// names, exactly as it refuses exclude entries: a path that is, lies under
// or holds an entry, and a Docker object or Ollama model a target entry
// names, dry and real, and that nothing of them is touched.
func TestIgnoredTargetsAreRefused(t *testing.T) {
	e := newEnv(t)
	kept := filepath.Join(e.root, "web", "dist")
	write(t, filepath.Join(kept, "app.js"), 10)
	inside := filepath.Join(e.root, "keep", "py", ".venv")
	write(t, filepath.Join(inside, "x"), 10)
	other := filepath.Join(e.root, "other", "node_modules")
	write(t, filepath.Join(other, "y"), 10)
	models := filepath.Join(e.home, ".ollama", "models")
	write(t, filepath.Join(models, "blobs", "b"), 10)
	model := ollamaAction(models, "llama3:latest")
	const runtime = "6F3C8F5A-1C2B-4D8E-9A1B-2C3D4E5F6A7B"
	runtimePath := filepath.Join(e.home, "Library", "Developer", "CoreSimulator", "Volumes", "iOS_21A328")
	sudo := plan.Action{ID: finding.MakeID("simulator-runtimes", plan.SimulatorRuntimeTarget(runtime)), Action: finding.ActionRunCommand,
		Target: plan.SimulatorRuntimeTarget(runtime), Tier: finding.TierB, Scanner: "simulator-runtimes", NeedsSudo: true, Path: runtimePath,
		Command: []string{"xcrun", "simctl", "runtime", "delete", runtime}}
	p := newPlan([]string{e.root, e.home},
		removeAction(kept, finding.KindDir),
		removeAction(inside, finding.KindDir),
		dockerAction(finding.ActionDockerRemoveContainer, "c-stopped", 10),
		dockerAction(finding.ActionDockerRemoveVolume, "vol-free", 20),
		model,
		withTags(dockerAction(finding.ActionDockerRemoveImage, "sha256:4f2a9c1e7b3d5a6b", 10), "app:1"),
		withTags(dockerAction(finding.ActionDockerRemoveImage, "sha256:multi", 10), "busybox:1.36", "busybox:latest"),
		dockerAction(finding.ActionDockerPruneBuildCache, "docker-build-cache", 300),
		sudo,
		removeAction(other, finding.KindDir),
	)
	ignore := Exclusions{IgnorePath: "/Users/me/.config/reclaim/ignore", Ignore: []string{
		kept, filepath.Join(e.root, "keep"), "docker:c-stopped", "docker:vol-free", "ollama:llama3",
		"docker:4F2A9C1E7B3D", "docker:busybox", "docker:docker-build-cache", runtimePath,
	}}
	ex := &execx.Fake{Tools: map[string]string{"ollama": "/usr/local/bin/ollama"}}
	for _, dry := range []bool{true, false} {
		dk := dockerFixture()
		dk.Images = append(dk.Images, image.Summary{ID: "sha256:4f2a9c1e7b3d5a6b", RepoTags: []string{"app:1"}})
		sum, _, err := run(t, e, p, func(o *Options) {
			o.DryRun, o.KeepGoing, o.Exec = dry, true, ex
			o.Docker = func() (dockerx.API, error) { return dk, nil }
			o.Excluded = ignore
		})
		require.Error(t, err)
		for _, o := range sum.Outcomes[:9] {
			require.Contains(t, []Status{StatusFailed, StatusRefused}, o.Status, o.Action.Label())
			require.ErrorContains(t, o.Err, "your ignore list (/Users/me/.config/reclaim/ignore)")
			require.Equal(t, CheckExcluded, o.Checks[len(o.Checks)-1].Name)
		}
		require.Contains(t, []Status{StatusChecked, StatusDone}, sum.Outcomes[9].Status, "what the list does not name goes on")
		require.Empty(t, dk.Removed(), "no Docker object was removed")
		require.Len(t, dk.BuildCache, 2, "the build cache was not pruned")
	}
	require.True(t, exists(filepath.Join(kept, "app.js")))
	require.True(t, exists(filepath.Join(inside, "x")))
	require.False(t, exists(other), "only the entry the list does not name was removed")
	require.Empty(t, ex.Ran(), "the model was not removed")
}

func TestCheckExcluded(t *testing.T) {
	ex := Exclusions{
		Exclude:    []string{"/Users/me/Code/archive", "docker:4f2a9c1e7b3d"},
		Ignore:     []string{"/Users/me/Code/web/dist", "docker:data", "ollama:llama3"},
		IgnorePath: "/Users/me/.config/reclaim/ignore",
	}
	image := &plan.Action{Action: finding.ActionDockerRemoveImage, Target: "sha256:4f2a9c1e7b3d5a6b7c8d9e0f"}
	volume := &plan.Action{Action: finding.ActionDockerRemoveVolume, Target: "data"}
	model := &plan.Action{Action: finding.ActionRunCommand, Target: "ollama:llama3:latest"}
	other := &plan.Action{Action: finding.ActionDockerRemoveVolume, Target: "cache"}
	tests := []struct {
		name string
		path string
		a    *plan.Action
		want string
	}{
		{"exclude entry", "/Users/me/Code/archive", nil, "refusing /Users/me/Code/archive: it is an exclude entry of your configuration"},
		{"inside an exclude entry", "/Users/me/Code/archive/x/node_modules", nil, "lies inside the exclude entry /Users/me/Code/archive"},
		{"holds an exclude entry", "/Users/me/Code", nil, "holds the exclude entry /Users/me/Code/archive"},
		{"sibling with a longer name", "/Users/me/Code/archived/node_modules", nil, ""},
		{"ignore entry", "/Users/me/Code/web/dist", nil, "it is on your ignore list (/Users/me/.config/reclaim/ignore)"},
		{"inside an ignore entry", "/Users/me/Code/web/dist/assets", nil, "lies inside /Users/me/Code/web/dist, which is on your ignore list"},
		{"holds an ignore entry", "/Users/me/Code/web", nil, "holds /Users/me/Code/web/dist, which is on your ignore list"},
		{"image by a short id", "", image, "refusing sha256:4f2a9c1e7b3d5a6b7c8d9e0f: the exclude entry docker:4f2a9c1e7b3d of your configuration names it"},
		{"volume by name", "", volume, "the entry docker:data on your ignore list"},
		{"model without its tag", "", model, "the entry ollama:llama3 on your ignore list"},
		{"another volume", "", other, ""},
		{"a model entry never names a Docker object", "", &plan.Action{Action: finding.ActionDockerRemoveVolume, Target: "ollama:llama3"}, ""},
		{"nothing excluded", "/Users/me/Code/app/node_modules", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkExcluded(tc.path, tc.a, ex)
			if tc.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.want)
		})
	}
	require.NoError(t, checkExcluded("/Users/me/Code/archive", nil, Exclusions{}))
}

// TestDryRunCountsFoldersEmptiedByEarlierActions checks a folder removed
// once empty, such as a Codex task folder: a dry run counts it empty when
// all it holds is removed by actions before it that passed their checks, as
// the real run will find it, and refuses it otherwise.
func TestDryRunCountsFoldersEmptiedByEarlierActions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(task string)
		dry   []Status
		real  []Status
	}{
		{"emptied by the action before it", func(string) {}, []Status{StatusChecked, StatusChecked}, []Status{StatusDone, StatusDone}},
		{"also holds a file no action removes", func(task string) { write(t, filepath.Join(task, "notes.txt"), 1) },
			[]Status{StatusChecked, StatusRefused}, []Status{StatusDone, StatusFailed}},
		{"the action before it is refused", func(task string) { write(t, filepath.Join(task, "repo", "package.json"), 1) },
			[]Status{StatusRefused, StatusRefused}, []Status{StatusFailed, StatusFailed}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			task := filepath.Join(e.root, "tasks", "8595")
			inner := filepath.Join(task, "repo")
			write(t, filepath.Join(inner, "a.txt"), 10)
			tc.setup(task)
			p := newPlan([]string{e.root}, removeAction(inner, finding.KindDir), removeAction(task, finding.KindEmptyDir))
			before := snapshot(t, e.root)
			dry, _, _ := run(t, e, p, func(o *Options) { o.DryRun = true })
			require.Equal(t, tc.dry, []Status{dry.Outcomes[0].Status, dry.Outcomes[1].Status})
			require.Equal(t, before, snapshot(t, e.root), "the dry run removed nothing")
			actual, _, _ := run(t, e, p, func(o *Options) { o.KeepGoing = true })
			require.Equal(t, tc.real, []Status{actual.Outcomes[0].Status, actual.Outcomes[1].Status}, "the real run agrees")
		})
	}
}
