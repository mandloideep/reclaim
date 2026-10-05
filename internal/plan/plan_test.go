package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mandloideep/reclaim/internal/finding"
)

func mk(scanner, target string, tier finding.Tier, action finding.Action, size int64) finding.Finding {
	f := finding.Finding{
		ID: finding.MakeID(scanner, target), Scanner: scanner, Tier: tier, Action: action, Target: target, Size: size,
	}
	if action == finding.ActionRemovePath {
		f.Path, f.Kind = target, finding.KindDir
	}
	if action == finding.ActionRunCommand {
		f.Path = target
		f.Command = []string{"npm", "cache", "clean", "--force"}
	}
	return f
}

func sampleReport() *finding.Report {
	return &finding.Report{
		Version: finding.ReportVersion,
		Created: time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC),
		Roots:   []string{"/Users/me/Code"},
		Findings: []finding.Finding{
			mk("node_modules", "/Users/me/Code/a/node_modules", finding.TierA, finding.ActionRemovePath, 100),
			mk("dist", "/Users/me/Code/a/dist", finding.TierC, finding.ActionRemovePath, 999),
			mk("npm-cache", "/Users/me/.npm/_cacache", finding.TierB, finding.ActionRunCommand, 50),
			mk("docker", "sha256:img", finding.TierB, finding.ActionDockerRemoveImage, 70),
			mk("docker", "c1", finding.TierA, finding.ActionDockerRemoveContainer, 5),
			mk("docker", "vol", finding.TierB, finding.ActionDockerRemoveVolume, 7),
		},
	}
}

func targets(p *Plan) []string {
	out := make([]string, 0, len(p.Actions))
	for _, a := range p.Actions {
		out = append(out, a.Target)
	}
	return out
}

func TestPresets(t *testing.T) {
	r := sampleReport()
	safe, err := Preset("safe")
	require.NoError(t, err)
	aggressive, err := Preset("aggressive")
	require.NoError(t, err)
	_, err = Preset("yolo")
	require.Error(t, err)

	now := r.Created.Add(time.Hour)
	p, err := New(r, SelectTiers(r.Findings, safe), "mac", now)
	require.NoError(t, err)
	require.Equal(t, []string{"c1", "/Users/me/Code/a/node_modules"}, targets(p))

	p, err = New(r, SelectTiers(r.Findings, aggressive), "mac", now)
	require.NoError(t, err)
	require.Equal(t, []string{"c1", "sha256:img", "vol", "/Users/me/Code/a/node_modules", "/Users/me/.npm/_cacache"}, targets(p),
		"containers go first so their images and volumes are free, tier C is never preset")
	require.Equal(t, int64(5+70+7+100+50), p.TotalSize())

	require.Equal(t, Version, p.Version)
	require.Equal(t, "mac", p.Host)
	require.Equal(t, r.Created, p.Scanned)
	require.Equal(t, now.UTC(), p.Created)
	require.Equal(t, r.Roots, p.Roots)
	require.NoError(t, p.Validate())

	require.Empty(t, SelectTiers(r.Findings, []finding.Tier{finding.TierC}), "tier C cannot be selected by tier")
}

func TestSaveLoadRoundTrip(t *testing.T) {
	r := sampleReport()
	p, err := New(r, r.Findings, "mac", r.Created)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "plan.json")
	require.NoError(t, Save(path, p))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), `"action": "RemovePath"`)
	require.Contains(t, string(data), `"version": 1`)

	got, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, p, got)
}

func TestLoadRejects(t *testing.T) {
	valid := func() string {
		r := sampleReport()
		p, err := New(r, r.Findings[:1], "mac", r.Created)
		require.NoError(t, err)
		path := filepath.Join(t.TempDir(), "p.json")
		require.NoError(t, Save(path, p))
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		return string(data)
	}
	tests := []struct {
		name    string
		edit    func(string) string
		wantErr string
	}{
		{name: "not json", edit: func(string) string { return "{" }, wantErr: "parse plan"},
		{name: "unknown field", edit: func(s string) string { return strings.Replace(s, `"host"`, `"hostname"`, 1) }, wantErr: "unknown field"},
		{name: "future version", edit: func(s string) string { return strings.Replace(s, `"version": 1`, `"version": 2`, 1) }, wantErr: "unsupported plan version"},
		{name: "unknown action", edit: func(s string) string { return strings.Replace(s, `"RemovePath"`, `"Shred"`, 1) }, wantErr: "unknown action"},
		{name: "attention only", edit: func(s string) string { return strings.Replace(s, `"RemovePath"`, `"None"`, 1) }, wantErr: "attention only"},
		{name: "path edited without id", edit: func(s string) string {
			return strings.ReplaceAll(s, "/Users/me/Code/a/node_modules", "/Users/me/Documents")
		}, wantErr: "id does not match"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "plan.json")
			require.NoError(t, os.WriteFile(path, []byte(tt.edit(valid())), 0o644))
			_, err := Load(path)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
	_, err := Load(filepath.Join(t.TempDir(), "missing.json"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestValidateActions(t *testing.T) {
	base := func() Action {
		return FromFinding(&finding.Finding{
			ID: finding.MakeID("s", "/a/b/c/d"), Scanner: "s", Tier: finding.TierA, Action: finding.ActionRemovePath,
			Path: "/a/b/c/d", Target: "/a/b/c/d", Kind: finding.KindDir,
		})
	}
	withID := func(a Action) Action {
		a.ID = finding.MakeID(a.Scanner, a.Target)
		return a
	}
	tests := []struct {
		name    string
		action  Action
		wantErr string
	}{
		{name: "valid", action: base()},
		{name: "relative path", action: func() Action { a := base(); a.Path, a.Target = "a/b", "a/b"; return withID(a) }(), wantErr: "clean absolute"},
		{name: "unclean path", action: func() Action { a := base(); a.Path, a.Target = "/a/b/../c", "/a/b/../c"; return withID(a) }(), wantErr: "clean absolute"},
		{name: "path differs from target", action: func() Action { a := base(); a.Path = "/a/b/c/e"; return a }(), wantErr: "equal to its target"},
		{name: "missing kind", action: func() Action { a := base(); a.Kind = ""; return a }(), wantErr: "kind"},
		{name: "remove with command", action: func() Action { a := base(); a.Command = []string{"rm"}; return a }(), wantErr: "must not carry a command"},
		{name: "bad tier", action: func() Action { a := base(); a.Tier = "X"; return a }(), wantErr: "unknown tier"},
		{name: "arbitrary command", action: func() Action {
			a := base()
			a.Action, a.Kind, a.Command = finding.ActionRunCommand, "", []string{"rm", "-rf", "/"}
			return a
		}(), wantErr: "not one of the clean commands"},
		{name: "allowed command", action: func() Action {
			a := base()
			a.Action, a.Kind, a.Command = finding.ActionRunCommand, "", []string{"brew", "cleanup", "-s"}
			return a
		}()},
		{name: "docker with path", action: func() Action { a := base(); a.Action = finding.ActionDockerRemoveImage; return a }(), wantErr: "must not carry a path"},
		{name: "docker", action: withID(Action{Scanner: "docker", Target: "sha256:x", Tier: finding.TierB, Action: finding.ActionDockerRemoveImage})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Plan{Version: Version, Created: time.Now(), Actions: []Action{tt.action}}
			err := p.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}

	t.Run("duplicates", func(t *testing.T) {
		p := &Plan{Version: Version, Created: time.Now(), Actions: []Action{base(), base()}}
		require.ErrorContains(t, p.Validate(), "duplicate")
	})
	t.Run("relative root", func(t *testing.T) {
		p := &Plan{Version: Version, Created: time.Now(), Roots: []string{"code"}}
		require.ErrorContains(t, p.Validate(), "root")
	})
	t.Run("no creation time", func(t *testing.T) {
		p := &Plan{Version: Version}
		require.ErrorContains(t, p.Validate(), "creation time")
	})
}

func TestNewRefusesAttentionFindings(t *testing.T) {
	r := sampleReport()
	old := finding.Finding{
		ID: finding.MakeID("old-downloads", "/Users/me/Downloads/big.mov"), Scanner: "old-downloads", Tier: finding.TierC,
		Action: finding.ActionNone, Path: "/Users/me/Downloads/big.mov", Target: "/Users/me/Downloads/big.mov", Kind: finding.KindFile, Size: 1 << 30,
	}
	r.Findings = append(r.Findings, old)
	_, err := New(r, []finding.Finding{r.Findings[0], old}, "mac", r.Created)
	require.ErrorContains(t, err, "attention only")
	all := []finding.Tier{finding.TierA, finding.TierB, finding.TierC}
	require.NotContains(t, targets(func() *Plan {
		p, err := New(r, SelectTiers(r.Findings, all), "mac", r.Created)
		require.NoError(t, err)
		return p
	}()), old.Target, "presets never select attention findings")

	a := FromFinding(&old)
	p := &Plan{Version: Version, Created: time.Now(), Actions: []Action{a}}
	require.ErrorContains(t, p.Validate(), "attention only", "a hand edited plan cannot carry one")
}

func TestAllowedCommand(t *testing.T) {
	tests := []struct {
		argv []string
		want bool
	}{
		{argv: []string{"go", "clean", "-modcache"}, want: true},
		{argv: []string{"go", "clean", "-modcache", "-x"}},
		{argv: []string{"/usr/bin/go", "clean", "-modcache"}},
		{argv: nil},
		{argv: []string{"ollama", "rm", "llama3:latest"}, want: true},
		{argv: []string{"ollama", "rm", "library/qwen2.5-coder:7b"}, want: true},
		{argv: []string{"ollama", "rm", "--help"}},
		{argv: []string{"ollama", "rm", "a", "b"}},
		{argv: []string{"ollama", "rm"}},
		{argv: []string{"ollama", "rm", "x;rm -rf /"}},
		{argv: []string{"xcrun", "simctl", "delete", "0A1B2C3D-0000-4000-8000-1234567890AB"}, want: true},
		{argv: []string{"xcrun", "simctl", "delete", "unavailable"}},
		{argv: []string{"xcrun", "simctl", "delete", "all"}},
		{argv: []string{"xcrun", "simctl", "runtime", "delete", "0a1b2c3d-0000-4000-8000-1234567890ab"}, want: true},
		{argv: []string{"xcrun", "simctl", "runtime", "delete", "all"}},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.argv, " "), func(t *testing.T) {
			require.Equal(t, tt.want, AllowedCommand(tt.argv))
		})
	}
}

func TestCommandArgumentsMatchTheTarget(t *testing.T) {
	cmd := func(scanner, path, target string, argv ...string) Action {
		return Action{ID: finding.MakeID(scanner, target), Scanner: scanner, Tier: finding.TierB, Action: finding.ActionRunCommand,
			Path: path, Target: target, Command: argv}
	}
	dev := "/Users/me/Library/Developer/CoreSimulator/Devices/11111111-2222-4333-8444-555555555555"
	models := "/Users/me/.ollama/models"
	tests := []struct {
		name    string
		action  Action
		wantErr bool
	}{
		{name: "ollama", action: cmd("ollama", models, OllamaTarget("llama3:latest"), "ollama", "rm", "llama3:latest")},
		{name: "ollama edited to another model", action: cmd("ollama", models, OllamaTarget("llama3:latest"), "ollama", "rm", "mistral:latest"), wantErr: true},
		{name: "ollama without its folder", action: cmd("ollama", "", OllamaTarget("llama3:latest"), "ollama", "rm", "llama3:latest"), wantErr: true},
		{name: "simulator", action: cmd("simulator-devices", dev, dev, "xcrun", "simctl", "delete", "11111111-2222-4333-8444-555555555555")},
		{name: "simulator edited to another device", action: cmd("simulator-devices", dev, dev, "xcrun", "simctl", "delete", "AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE"), wantErr: true},
		{name: "runtime", action: cmd("simulator-runtimes", "", SimulatorRuntimeTarget("0F0F0F0F-1111-4222-8333-444444444444"),
			"xcrun", "simctl", "runtime", "delete", "0F0F0F0F-1111-4222-8333-444444444444")},
		{name: "runtime edited", action: cmd("simulator-runtimes", "", SimulatorRuntimeTarget("0F0F0F0F-1111-4222-8333-444444444444"),
			"xcrun", "simctl", "runtime", "delete", "0F0F0F0F-1111-4222-8333-555555555555"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Plan{Version: Version, Created: time.Now(), Actions: []Action{tt.action}}
			if tt.wantErr {
				require.ErrorContains(t, p.Validate(), "does not act on the target")
				return
			}
			require.NoError(t, p.Validate())
		})
	}
}

func TestOllamaManifest(t *testing.T) {
	for model, want := range map[string]string{
		"llama3:latest":      "registry.ollama.ai/library/llama3/latest",
		"me/tuned:v1":        "registry.ollama.ai/me/tuned/v1",
		"hf.co/org/model:q4": "hf.co/org/model/q4",
		"llama3":             "",
		"a/b/c/d:x":          "",
		"../x:y":             "",
		"x:a/b":              "",
	} {
		got, ok := OllamaManifest(model)
		require.Equal(t, want != "", ok, model)
		require.Equal(t, want, got, model)
	}
}

func TestLocateQuery(t *testing.T) {
	q, sub, ok := LocateQuery([]string{"npm", "cache", "clean", "--force"})
	require.True(t, ok)
	require.Equal(t, []string{"npm", "config", "get", "cache"}, q)
	require.Equal(t, "_cacache", sub)

	q, sub, ok = LocateQuery([]string{"brew", "cleanup", "-s"})
	require.True(t, ok)
	require.Equal(t, []string{"brew", "--cache"}, q)
	require.Empty(t, sub)

	_, _, ok = LocateQuery([]string{"ollama", "rm", "llama3:latest"})
	require.False(t, ok, "removal commands name their target and need no location")
	_, _, ok = LocateQuery([]string{"rm", "-rf", "/"})
	require.False(t, ok)

	for _, c := range cleanCommands() {
		if c.arg == nil {
			require.NotEmpty(t, c.locate, "every clean command of a cache can be located again: %v", c.argv)
		}
	}
}

func TestCheckAge(t *testing.T) {
	scanned := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	p := &Plan{Created: scanned.Add(23 * time.Hour), Scanned: scanned}
	require.NoError(t, p.CheckAge(scanned.Add(23*time.Hour), false))
	err := p.CheckAge(scanned.Add(25*time.Hour), false)
	require.ErrorContains(t, err, "--stale-ok")
	require.NoError(t, p.CheckAge(scanned.Add(25*time.Hour), true))

	noScan := &Plan{Created: scanned}
	require.Error(t, noScan.CheckAge(scanned.Add(48*time.Hour), false))
}

func TestActionLabel(t *testing.T) {
	require.Equal(t, "img x", (&Action{Name: "img", Path: "x"}).Label())
	require.Equal(t, "img", (&Action{Name: "img", Target: "t"}).Label())
	require.Equal(t, "/p", (&Action{Path: "/p", Target: "/p"}).Label())
	require.Equal(t, "t", (&Action{Target: "t"}).Label())
}
