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
	p := New(r, SelectTiers(r.Findings, safe), "mac", now)
	require.Equal(t, []string{"c1", "/Users/me/Code/a/node_modules"}, targets(p))

	p = New(r, SelectTiers(r.Findings, aggressive), "mac", now)
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
	p := New(r, r.Findings, "mac", r.Created)
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
		p := New(r, r.Findings[:1], "mac", r.Created)
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

func TestAllowedCommand(t *testing.T) {
	require.True(t, AllowedCommand([]string{"go", "clean", "-modcache"}))
	require.False(t, AllowedCommand([]string{"go", "clean", "-modcache", "-x"}))
	require.False(t, AllowedCommand([]string{"/usr/bin/go", "clean", "-modcache"}))
	require.False(t, AllowedCommand(nil))
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
