// Package execx runs external tools on behalf of scanners and apply.
//
// Scanners ask tools where their caches live, for example "npm config get
// cache", and apply runs a tool's own clean command. Both go through Runner so
// tests can substitute a fake and never start real processes.
package execx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrNotInstalled is returned when the requested tool is not on PATH.
var ErrNotInstalled = errors.New("not installed")

// Runner runs external commands.
type Runner interface {
	// LookPath reports the full path of a tool, or ErrNotInstalled.
	LookPath(name string) (string, error)
	// Output runs a short read only query, such as "go env GOCACHE", and
	// returns its trimmed standard output.
	Output(ctx context.Context, name string, args ...string) (string, error)
	// Run runs a command to completion and returns its combined output.
	Run(ctx context.Context, argv []string) ([]byte, error)
}

// OS runs real processes.
type OS struct {
	// QueryTimeout bounds every Output call. Zero means ten seconds.
	QueryTimeout time.Duration
	// Dir is the working directory for every command. Empty means "/", so a
	// project's local tool configuration never changes the result.
	Dir string
}

// quietEnv is added to the environment of every command. It stops tools from
// downloading themselves, checking for updates or taking optional locks while
// they are only being asked a question.
func quietEnv() []string {
	return []string{
		"COREPACK_ENABLE_DOWNLOAD_PROMPT=0",
		"COREPACK_ENABLE_NETWORK=0",
		"NO_UPDATE_NOTIFIER=1",
		"npm_config_update_notifier=false",
		"HOMEBREW_NO_AUTO_UPDATE=1",
		"HOMEBREW_NO_ENV_HINTS=1",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
	}
}

// LookPath implements Runner.
func (o OS) LookPath(name string) (string, error) {
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, ErrNotInstalled)
	}
	return p, nil
}

// Output implements Runner.
func (o OS) Output(ctx context.Context, name string, args ...string) (string, error) {
	path, err := o.LookPath(name)
	if err != nil {
		return "", err
	}
	timeout := o.QueryTimeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = o.dir()
	cmd.Env = append(os.Environ(), quietEnv()...)
	// A tool that leaves a child holding stdout open must not outlive the
	// timeout.
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, firstLine(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// Run implements Runner.
func (o OS) Run(ctx context.Context, argv []string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, errors.New("empty command")
	}
	path, err := o.LookPath(argv[0])
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, path, argv[1:]...)
	cmd.Dir = o.dir()
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = append(os.Environ(), "COREPACK_ENABLE_DOWNLOAD_PROMPT=0", "HOMEBREW_NO_AUTO_UPDATE=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
	}
	return out, nil
}

func (o OS) dir() string {
	if o.Dir == "" {
		return "/"
	}
	return o.Dir
}

// FirstAbsPath returns the first line of a tool's output that is an absolute
// path, cleaned, or an empty string when there is none. Tools such as npm may
// print notices around the answer.
func FirstAbsPath(out string) string {
	for line := range strings.Lines(out) {
		line = strings.TrimSpace(line)
		if filepath.IsAbs(line) {
			return filepath.Clean(line)
		}
	}
	return ""
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	first, _, _ := strings.Cut(s, "\n")
	return first
}
