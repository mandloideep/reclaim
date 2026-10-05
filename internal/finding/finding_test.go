package finding

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMakeIDIsStable(t *testing.T) {
	a := MakeID("node_modules", "/x/node_modules")
	require.Equal(t, a, MakeID("node_modules", "/x/node_modules"))
	require.NotEqual(t, a, MakeID("dist", "/x/node_modules"))
	require.NotEqual(t, MakeID("ab", "c"), MakeID("a", "bc"), "the separator keeps fields apart")
	require.Equal(t, "sha256:", a[:7])
	require.Len(t, a, 7+64)
}

func TestFindingJSONIsStable(t *testing.T) {
	f := Finding{
		ID:       MakeID("node_modules", "/p/node_modules"),
		Scanner:  "node_modules",
		Category: CategoryProject,
		Tier:     TierA,
		Path:     "/p/node_modules",
		Kind:     KindDir,
		Target:   "/p/node_modules",
		Size:     42,
		Project:  "/p",
		LastUsed: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC),
		Restore:  "npm install",
		Action:   ActionRemovePath,
	}
	data, err := json.Marshal(f)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"id": "`+f.ID+`",
		"scanner": "node_modules",
		"category": "project",
		"tier": "A",
		"path": "/p/node_modules",
		"kind": "dir",
		"target": "/p/node_modules",
		"size": 42,
		"project": "/p",
		"last_used": "2026-10-05T12:00:00Z",
		"restore": "npm install",
		"action": "RemovePath"
	}`, string(data))

	t.Run("zero values are omitted", func(t *testing.T) {
		data, err := json.Marshal(Finding{ID: "x", Scanner: "docker", Category: CategoryDocker, Tier: TierB, Target: "img", Action: ActionDockerRemoveImage})
		require.NoError(t, err)
		require.JSONEq(t, `{"id":"x","scanner":"docker","category":"docker","tier":"B","target":"img","size":0,"action":"DockerRemoveImage"}`, string(data))
	})
}

func TestParseTier(t *testing.T) {
	for in, want := range map[string]Tier{"a": TierA, "B": TierB, "c": TierC} {
		got, err := ParseTier(" " + in + " ")
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	_, err := ParseTier("D")
	require.Error(t, err)
}

func TestActions(t *testing.T) {
	require.True(t, ActionRemovePath.Valid())
	require.False(t, Action("Format").Valid())
	require.True(t, ActionDockerPruneBuildCache.IsDocker())
	require.False(t, ActionRunCommand.IsDocker())
}

func TestSort(t *testing.T) {
	fs := []Finding{
		{ID: "1", Category: CategoryDocker, Size: 100},
		{ID: "2", Category: CategoryProject, Size: 10},
		{ID: "3", Category: CategoryProject, Size: 20},
		{ID: "4", Category: CategoryPackageCache, Size: 5},
		{ID: "0", Category: CategoryProject, Size: 20},
	}
	Sort(fs)
	ids := make([]string, 0, len(fs))
	for _, f := range fs {
		ids = append(ids, f.ID)
	}
	require.Equal(t, []string{"0", "3", "2", "4", "1"}, ids)
}

func TestReportValidate(t *testing.T) {
	good := Finding{Scanner: "s", Target: "t", Tier: TierA, Action: ActionRemovePath}
	good.ID = MakeID("s", "t")
	tests := []struct {
		name    string
		report  Report
		wantErr string
	}{
		{name: "valid", report: Report{Version: ReportVersion, Findings: []Finding{good}}},
		{name: "wrong version", report: Report{Version: 2}, wantErr: "unsupported report version"},
		{name: "bad tier", report: Report{Version: 1, Findings: []Finding{func() Finding { f := good; f.Tier = "Z"; return f }()}}, wantErr: "unknown tier"},
		{name: "bad action", report: Report{Version: 1, Findings: []Finding{func() Finding { f := good; f.Action = "Nuke"; return f }()}}, wantErr: "unknown action"},
		{name: "edited target", report: Report{Version: 1, Findings: []Finding{func() Finding { f := good; f.Target = "/other"; return f }()}}, wantErr: "id does not match"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.report.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestReportTotalSize(t *testing.T) {
	r := Report{Findings: []Finding{
		{Size: 3, Action: ActionRemovePath},
		{Size: 4, Action: ActionDockerRemoveImage},
		{Size: 1000, Action: ActionNone},
	}}
	require.Equal(t, int64(7), r.TotalSize(), "attention findings are not reclaimable")
}

func TestActionable(t *testing.T) {
	for _, a := range Actions() {
		f := Finding{Action: a}
		require.Equal(t, a != ActionNone, f.Actionable(), a)
		require.True(t, a.Valid(), "reports may carry every action, %s included", a)
	}
	require.False(t, (&Finding{Action: "Nuke"}).Actionable())
}

func TestDockerKind(t *testing.T) {
	tests := []struct {
		f    Finding
		want string
	}{
		{f: Finding{Action: ActionDockerPruneBuildCache}, want: DockerKindBuildCache},
		{f: Finding{Action: ActionDockerRemoveContainer}, want: DockerKindContainers},
		{f: Finding{Action: ActionDockerRemoveImage}, want: DockerKindDangling},
		{f: Finding{Action: ActionDockerRemoveImage, Tags: []string{"x:1"}}, want: DockerKindImages},
		{f: Finding{Action: ActionDockerRemoveVolume}, want: DockerKindVolumes},
		{f: Finding{Action: ActionRemovePath}, want: ""},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, tt.f.DockerKind())
	}
	require.Equal(t, []string{"build cache", "stopped containers", "dangling images", "unused images", "volumes"}, DockerKinds())
}

func TestScopePartial(t *testing.T) {
	var none *Scope
	require.False(t, none.Partial())
	require.False(t, (&Scope{Command: "scan"}).Partial())
	require.True(t, (&Scope{Command: "scan", Paths: []string{"/x"}}).Partial())
	require.True(t, (&Scope{Command: "here", Paths: []string{"/x"}}).Partial())
}

func TestWarningString(t *testing.T) {
	require.Equal(t, "docker: /x: boom", Warning{Scanner: "docker", Path: "/x", Message: "boom"}.String())
	require.Equal(t, "boom", Warning{Message: "boom"}.String())
}
