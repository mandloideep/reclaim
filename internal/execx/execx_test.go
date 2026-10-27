package execx

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOSOutput(t *testing.T) {
	o := OS{}
	out, err := o.Output(context.Background(), "sh", "-c", "pwd; echo second line")
	require.NoError(t, err)
	require.Equal(t, "/\nsecond line", out, "commands run in / so local tool config does not apply")

	_, err = o.Output(context.Background(), "sh", "-c", "echo oops >&2; exit 3")
	require.ErrorContains(t, err, "oops")

	_, err = o.Output(context.Background(), "definitely-not-a-real-tool-reclaim")
	require.ErrorIs(t, err, ErrNotInstalled)

	slow := OS{QueryTimeout: 50 * time.Millisecond}
	start := time.Now()
	_, err = slow.Output(context.Background(), "sh", "-c", "sleep 5")
	require.Error(t, err)
	require.Less(t, time.Since(start), 3*time.Second, "queries are bounded by the timeout")
}

func TestOSOutputSetsQuietEnvironment(t *testing.T) {
	out, err := OS{}.Output(context.Background(), "sh", "-c", "echo $COREPACK_ENABLE_DOWNLOAD_PROMPT$GIT_OPTIONAL_LOCKS")
	require.NoError(t, err)
	require.Equal(t, "00", out)
}

func TestOSRun(t *testing.T) {
	o := OS{Dir: t.TempDir()}
	out, err := o.Run(context.Background(), []string{"sh", "-c", "echo hello; echo world >&2"})
	require.NoError(t, err)
	require.Contains(t, string(out), "hello")
	require.Contains(t, string(out), "world")

	_, err = o.Run(context.Background(), []string{"sh", "-c", "exit 2"})
	require.Error(t, err)
	_, err = o.Run(context.Background(), nil)
	require.Error(t, err)
	_, err = o.Run(context.Background(), []string{"definitely-not-a-real-tool-reclaim"})
	require.ErrorIs(t, err, ErrNotInstalled)
}

func TestFake(t *testing.T) {
	f := &Fake{
		Tools:   map[string]string{"npm": "/bin/npm"},
		Outputs: map[string]string{"npm config get cache": " /x \n"},
	}
	out, err := f.Output(context.Background(), "npm", "config", "get", "cache")
	require.NoError(t, err)
	require.Equal(t, "/x", out)
	_, err = f.Output(context.Background(), "npm", "other")
	require.Error(t, err)
	_, err = f.Output(context.Background(), "pnpm", "store", "path")
	require.ErrorIs(t, err, ErrNotInstalled)

	f.OnRun = func([]string) error { return errors.New("boom") }
	_, err = f.Run(context.Background(), []string{"npm", "cache", "clean", "--force"})
	require.Error(t, err)
	require.Equal(t, [][]string{{"npm", "cache", "clean", "--force"}}, f.Ran())
}

func TestEnvironDropsRepositoryVariables(t *testing.T) {
	got := Environ([]string{"HOME=/h", "GIT_DIR=/r/.git", "GIT_WORK_TREE=/r", "GIT_INDEX_FILE=/i", "GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.worktree", "GIT_CONFIG_VALUE_0=/x", "GIT_CONFIG_GLOBAL=/g", "GIT_TERMINAL_PROMPT=0", "PATH=/bin"})
	require.Equal(t, []string{"HOME=/h", "GIT_CONFIG_GLOBAL=/g", "GIT_TERMINAL_PROMPT=0", "PATH=/bin"}, got)
}

func TestExitCode(t *testing.T) {
	_, err := OS{}.Output(context.Background(), "sh", "-c", "exit 3")
	code, ok := ExitCode(err)
	require.True(t, ok)
	require.Equal(t, 3, code)
	_, ok = ExitCode(errors.New("timeout"))
	require.False(t, ok)
	_, err = (&Fake{Tools: map[string]string{"git": "/g"}, Exits: map[string]int{"git x": 1}}).Output(context.Background(), "git", "x")
	code, ok = ExitCode(err)
	require.True(t, ok)
	require.Equal(t, 1, code)
}
