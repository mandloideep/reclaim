package project

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/mandloideep/reclaim/internal/execx"
)

// GitCommitTime returns a CommitTime function that asks git for the time of
// the last commit. When git is not installed or fails, it falls back to the
// modification time of the HEAD reflog, which changes on every commit,
// checkout and pull.
func GitCommitTime(r execx.Runner) func(ctx context.Context, root string) (time.Time, error) {
	return func(ctx context.Context, root string) (time.Time, error) {
		out, err := r.Output(ctx, "git", "-C", root, "--no-pager", "log", "-1", "--format=%ct")
		if err == nil && out != "" {
			secs, perr := strconv.ParseInt(out, 10, 64)
			if perr == nil {
				return time.Unix(secs, 0), nil
			}
		}
		info, serr := os.Stat(filepath.Join(root, ".git", "logs", "HEAD"))
		if serr != nil {
			return time.Time{}, fmt.Errorf("last commit of %s: %w", root, serr)
		}
		return info.ModTime(), nil
	}
}
