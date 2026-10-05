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
