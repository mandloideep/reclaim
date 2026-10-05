package execx

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// Fake is a Runner for tests. It never starts a process.
type Fake struct {
	// Tools maps a tool name to its full path. Tools not listed are not installed.
	Tools map[string]string
	// Outputs maps a full command line, such as "npm config get cache", to the
	// output Output returns. Commands not listed fail.
	Outputs map[string]string
	// RunErr, when set, is returned by every Run call.
	RunErr error
	// OnRun, when set, is called by Run, for example to simulate the effect of
	// a clean command on a fixture.
	OnRun func(argv []string) error

	mu  sync.Mutex
	ran [][]string
}

// LookPath implements Runner.
func (f *Fake) LookPath(name string) (string, error) {
	if p, ok := f.Tools[name]; ok {
		return p, nil
	}
	return "", fmt.Errorf("%s: %w", name, ErrNotInstalled)
}

// Output implements Runner.
func (f *Fake) Output(_ context.Context, name string, args ...string) (string, error) {
	if _, err := f.LookPath(name); err != nil {
		return "", err
	}
	line := strings.Join(append([]string{name}, args...), " ")
	if out, ok := f.Outputs[line]; ok {
		return strings.TrimSpace(out), nil
	}
	return "", errors.New(line + ": no fake output")
}

// Run implements Runner.
func (f *Fake) Run(_ context.Context, argv []string) ([]byte, error) {
	f.mu.Lock()
	f.ran = append(f.ran, slices.Clone(argv))
	f.mu.Unlock()
	if f.RunErr != nil {
		return nil, f.RunErr
	}
	if f.OnRun != nil {
		if err := f.OnRun(argv); err != nil {
			return nil, err
		}
	}
	return []byte("ok\n"), nil
}

// Ran returns every argv passed to Run, in order.
func (f *Fake) Ran() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.ran)
}
