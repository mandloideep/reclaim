package apply

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/project"
)

// errGone means the target no longer exists, so there is nothing to do.
var errGone = errors.New("already gone")

// minComponents is the fewest path components a removable path may have.
// "/Users/me/Code/app" has four.
const minComponents = 4

// protectedSystemPaths are never removed, whatever a plan says.
func protectedSystemPaths() []string {
	return []string{
		"/", "/Applications", "/Library", "/System", "/Users", "/Volumes", "/bin", "/cores", "/dev",
		"/etc", "/home", "/media", "/mnt", "/nix", "/opt", "/opt/homebrew", "/private", "/private/etc",
		"/private/tmp", "/private/var", "/root", "/run", "/sbin", "/snap", "/srv", "/tmp", "/usr",
		"/usr/local", "/var", "/var/lib", "/var/tmp",
	}
}

// protectedHomePaths are paths relative to the home directory that are never
// removed. Their contents may be, for example ~/Library/Caches/Homebrew.
func protectedHomePaths() []string {
	return []string{
		"", "Library", "Library/Caches", "Library/Application Support", "Library/Containers",
		"Library/Group Containers", "Library/Developer", "Library/Mobile Documents", "Library/CloudStorage",
		"Library/Preferences", "Library/Keychains", "Library/Mail", "Library/Messages", "Library/Photos",
		"Documents", "Desktop", "Downloads", "Pictures", "Movies", "Music", "Public", "Sites",
		".ssh", ".gnupg", ".config", ".local", ".local/share", ".local/state", ".cache", ".docker",
		".kube", ".aws", "Code", "Developer", "Projects", "src", "work", "go", ".cargo", ".rustup",
		".npm", ".gradle", ".m2", ".bun", ".yarn", ".orbstack", "OrbStack",
	}
}

// componentCount counts the components of a clean absolute path.
func componentCount(p string) int {
	trimmed := strings.Trim(p, string(filepath.Separator))
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, string(filepath.Separator)))
}

// checkProtected applies the rules that depend only on the path string: it
// must be clean and absolute, at least four components long, not on the
// denylist, not inside a .git directory, not the home directory or one of its
// ancestors, not a root or an ancestor of one, not the owning project or an
// ancestor of it, and inside one of the roots.
func checkProtected(p, home, projectRoot string, roots []string) error {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return fmt.Errorf("refusing %s: not a clean absolute path", p)
	}
	if componentCount(p) < minComponents {
		return fmt.Errorf("refusing %s: paths with fewer than %d components are never removed", p, minComponents)
	}
	if slices.Contains(protectedSystemPaths(), p) {
		return fmt.Errorf("refusing %s: protected system path", p)
	}
	if home != "" {
		if project.IsWithin(home, p) {
			return fmt.Errorf("refusing %s: it contains the home directory", p)
		}
		for _, rel := range protectedHomePaths() {
			if p == filepath.Join(home, rel) {
				return fmt.Errorf("refusing %s: protected folder in the home directory", p)
			}
		}
	}
	if slices.Contains(strings.Split(p, string(filepath.Separator)), ".git") {
		return fmt.Errorf("refusing %s: inside a .git directory", p)
	}
	if projectRoot != "" && project.IsWithin(projectRoot, p) {
		return fmt.Errorf("refusing %s: it is or contains the project root %s", p, projectRoot)
	}
	inside := false
	for _, r := range roots {
		if project.IsWithin(r, p) {
			return fmt.Errorf("refusing %s: it is or contains the scan root %s", p, r)
		}
		if project.IsWithin(p, r) {
			inside = true
		}
	}
	if !inside {
		return fmt.Errorf("refusing %s: outside the roots recorded at scan time", p)
	}
	return nil
}

// verifyPath re-checks a RemovePath target immediately before removal: the
// path rules above, then that no component of the path is a symbolic link,
// that the object is still the same kind the scanner found, and that a
// directory does not hold a git working tree.
func verifyPath(a *plan.Action, home string, roots []string) error {
	if err := checkProtected(a.Path, home, a.Project, roots); err != nil {
		return err
	}
	info, err := os.Lstat(a.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return errGone
	}
	if err != nil {
		return fmt.Errorf("inspect %s: %w", a.Path, err)
	}
	resolved, err := filepath.EvalSymlinks(a.Path)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", a.Path, err)
	}
	if resolved != a.Path {
		return fmt.Errorf("refusing %s: the path now goes through a symbolic link to %s", a.Path, resolved)
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("refusing %s: it is now a symbolic link", a.Path)
	case a.Kind == finding.KindDir && !info.IsDir():
		return fmt.Errorf("refusing %s: it was a directory at scan time and is not one now", a.Path)
	case a.Kind == finding.KindFile && !info.Mode().IsRegular():
		return fmt.Errorf("refusing %s: it was a file at scan time and is not one now", a.Path)
	}
	if info.IsDir() {
		if _, err := os.Lstat(filepath.Join(a.Path, ".git")); err == nil {
			return fmt.Errorf("refusing %s: it contains a .git entry, so it is a working tree", a.Path)
		}
	}
	return nil
}
