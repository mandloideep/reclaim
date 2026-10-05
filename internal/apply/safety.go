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
	"github.com/mandloideep/reclaim/internal/fsx"
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
		".npm", ".gradle", ".m2", ".bun", ".yarn", ".orbstack", "OrbStack", "code", "projects", "dev",
		"Library/pnpm",
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
	if slices.Contains(protectedSystemPaths(), p) {
		return fmt.Errorf("refusing %s: protected system path", p)
	}
	if componentCount(p) < minComponents {
		return fmt.Errorf("refusing %s: paths with fewer than %d components are never removed", p, minComponents)
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
func verifyPath(a *plan.Action, home string, roots []string) (fs.FileInfo, error) {
	if err := checkProtected(a.Path, home, a.Project, roots); err != nil {
		return nil, err
	}
	info, err := os.Lstat(a.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errGone
	}
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", a.Path, err)
	}
	if err := checkObject(a, info); err != nil {
		return nil, err
	}
	return info, nil
}

// checkObject applies the checks that depend on what is on disk now.
func checkObject(a *plan.Action, info fs.FileInfo) error {
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
		for _, name := range project.ManifestNames() {
			if _, err := os.Lstat(filepath.Join(a.Path, name)); err == nil {
				if name == ".git" {
					return fmt.Errorf("refusing %s: it contains a .git entry, so it is a working tree", a.Path)
				}
				return fmt.Errorf("refusing %s: it contains %s, so it is a project", a.Path, name)
			}
		}
	}
	parent, err := os.Lstat(filepath.Dir(a.Path))
	if err != nil {
		return fmt.Errorf("inspect %s: %w", filepath.Dir(a.Path), err)
	}
	dev, ok := fsx.DeviceOf(info)
	parentDev, parentOK := fsx.DeviceOf(parent)
	if ok && parentOK && dev != parentDev {
		return fmt.Errorf("refusing %s: it is a mount point", a.Path)
	}
	return nil
}

// containingRoot returns the deepest root that holds path.
func containingRoot(path string, roots []string) string {
	best := ""
	for _, r := range roots {
		if project.IsWithin(path, r) && path != r && len(r) > len(best) {
			best = r
		}
	}
	return best
}

// removeInRoot removes path through an os.Root opened at root, after checking
// once more, immediately before acting, that no component of the path became
// a symbolic link and that the object is the one verified earlier. Removal
// through the root cannot escape it even if the path changes underneath.
func removeInRoot(root, path string, checked fs.FileInfo) error {
	if resolved, err := filepath.EvalSymlinks(path); err != nil || resolved != path {
		return fmt.Errorf("refusing %s: the path changed since it was checked", path)
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("open root %s: %w", root, err)
	}
	defer func() { _ = r.Close() }()
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("refusing %s: %w", path, err)
	}
	info, err := r.Lstat(rel)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	if !os.SameFile(info, checked) {
		return fmt.Errorf("refusing %s: it was replaced since it was checked", path)
	}
	return r.RemoveAll(rel)
}
