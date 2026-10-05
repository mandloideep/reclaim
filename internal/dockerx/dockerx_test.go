package dockerx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/docker/docker/api/types/system"
	"github.com/docker/docker/client"
	"github.com/stretchr/testify/require"
)

func writeContext(t *testing.T, configDir, name, host string) {
	t.Helper()
	sum := sha256.Sum256([]byte(name))
	dir := filepath.Join(configDir, "contexts", "meta", hex.EncodeToString(sum[:]))
	require.NoError(t, os.MkdirAll(dir, 0o755))
	meta := `{"Name":"` + name + `","Endpoints":{"docker":{"Host":"` + host + `","SkipTLSVerify":false}}}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o644))
}

func TestContextHost(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		env     map[string]string
		want    string
		wantErr bool
	}{
		{name: "no config", want: ""},
		{name: "default context", config: `{"currentContext":"default"}`, want: ""},
		{name: "current context from config", config: `{"currentContext":"orbstack"}`, want: "unix:///orb.sock"},
		{name: "DOCKER_CONTEXT wins over config", config: `{"currentContext":"orbstack"}`, env: map[string]string{"DOCKER_CONTEXT": "colima"}, want: "unix:///colima.sock"},
		{name: "unknown context", config: `{"currentContext":"missing"}`, wantErr: true},
		{name: "bad config", config: `{`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			configDir := filepath.Join(home, ".docker")
			require.NoError(t, os.MkdirAll(configDir, 0o755))
			if tt.config != "" {
				require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.json"), []byte(tt.config), 0o644))
			}
			writeContext(t, configDir, "orbstack", "unix:///orb.sock")
			writeContext(t, configDir, "colima", "unix:///colima.sock")
			getenv := func(k string) string { return tt.env[k] }
			got, err := ContextHost(getenv, home)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestContextHostHonorsDockerConfig(t *testing.T) {
	configDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"currentContext":"x"}`), 0o644))
	writeContext(t, configDir, "x", "tcp://10.0.0.1:2375")
	got, err := ContextHost(func(k string) string {
		if k == "DOCKER_CONFIG" {
			return configDir
		}
		return ""
	}, t.TempDir())
	require.NoError(t, err)
	require.Equal(t, "tcp://10.0.0.1:2375", got)
}

func TestParseAndMatchLabels(t *testing.T) {
	l, err := ParseLabel("reclaim.test=1")
	require.NoError(t, err)
	require.Equal(t, Label{Key: "reclaim.test", Value: "1", HasValue: true}, l)
	require.Equal(t, "reclaim.test=1", l.String())

	k, err := ParseLabel("com.example")
	require.NoError(t, err)
	require.False(t, k.HasValue)

	_, err = ParseLabel("=x")
	require.Error(t, err)

	tests := []struct {
		name    string
		filters []Label
		labels  map[string]string
		want    bool
	}{
		{name: "no filters", labels: nil, want: true},
		{name: "match", filters: []Label{l}, labels: map[string]string{"reclaim.test": "1"}, want: true},
		{name: "wrong value", filters: []Label{l}, labels: map[string]string{"reclaim.test": "2"}, want: false},
		{name: "missing", filters: []Label{l}, labels: map[string]string{}, want: false},
		{name: "key only", filters: []Label{k}, labels: map[string]string{"com.example": ""}, want: true},
		{name: "all must match", filters: []Label{l, k}, labels: map[string]string{"reclaim.test": "1"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, MatchLabels(tt.filters, tt.labels))
		})
	}
}

func TestIsActiveState(t *testing.T) {
	for _, s := range []string{"running", "paused", "restarting", "removing", "something-new"} {
		require.True(t, IsActiveState(s), s)
	}
	for _, s := range []string{"exited", "created", "dead"} {
		require.False(t, IsActiveState(s), s)
	}
}

func TestIsOrbStack(t *testing.T) {
	require.True(t, IsOrbStack("unix:///Users/me/.orbstack/run/docker.sock", system.Info{}))
	require.True(t, IsOrbStack("unix:///var/run/docker.sock", system.Info{OperatingSystem: "OrbStack"}))
	require.False(t, IsOrbStack("unix:///var/run/docker.sock", system.Info{OperatingSystem: "Docker Desktop"}))
}

func TestIsNotFound(t *testing.T) {
	t.Run("fake", func(t *testing.T) {
		_, err := (&Fake{}).ContainerInspect(context.Background(), "nope")
		require.True(t, IsNotFound(err))
	})
	t.Run("real client against a daemon answering 404", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"No such container: nope"}`))
		}))
		defer srv.Close()
		c, err := client.NewClientWithOpts(client.WithHost("tcp://"+srv.Listener.Addr().String()), client.WithVersion("1.45"))
		require.NoError(t, err)
		defer func() { _ = c.Close() }()
		_, err = c.ContainerInspect(context.Background(), "nope")
		require.Error(t, err)
		require.True(t, IsNotFound(err))
	})
	t.Run("other errors", func(t *testing.T) {
		require.False(t, IsNotFound(errors.New("boom")))
		require.False(t, IsNotFound(nil))
	})
}

func TestNewDoesNotContactDaemon(t *testing.T) {
	c, err := New(func(k string) string {
		if k == "DOCKER_HOST" {
			return "unix:///nonexistent.sock"
		}
		return ""
	}, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, c.Close())
}
