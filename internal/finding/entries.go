package finding

import "strings"

// Prefixes of the target entries that exclude and the ignore list accept
// besides paths, for findings that have no path of their own to name.
const (
	// DockerEntryPrefix starts an entry naming a Docker object: a
	// container or image id, at least its first 12 hex digits, a volume
	// name, or the build cache target.
	DockerEntryPrefix = "docker:"
	// OllamaEntryPrefix starts an entry naming an Ollama model, such as
	// ollama:llama3:latest; a model without a tag means its latest tag.
	OllamaEntryPrefix = "ollama:"
)

// IsTargetEntry reports whether an exclude or ignore entry names a target,
// docker:<id> or ollama:<model>, rather than a path.
func IsTargetEntry(entry string) bool {
	return strings.HasPrefix(entry, DockerEntryPrefix) || strings.HasPrefix(entry, OllamaEntryPrefix)
}

// MatchesTargetEntry reports whether a target entry names target: for a
// Docker object, when docker is set, the id, a prefix of at least 12 hex
// digits of it, or the volume name; for an Ollama model, the model with
// its tag, or without one for its latest tag. A path entry matches no
// target.
func MatchesTargetEntry(entry string, docker bool, target string) bool {
	switch {
	case strings.HasPrefix(entry, DockerEntryPrefix):
		id := strings.TrimPrefix(entry, DockerEntryPrefix)
		if !docker || id == "" {
			return false
		}
		if id == target {
			return true
		}
		short, full := strings.TrimPrefix(id, "sha256:"), strings.TrimPrefix(target, "sha256:")
		return len(short) >= 12 && isHex(short) && strings.HasPrefix(full, short)
	case strings.HasPrefix(entry, OllamaEntryPrefix):
		model := strings.TrimPrefix(entry, OllamaEntryPrefix)
		if docker || model == "" {
			return false
		}
		return entry == target || (!strings.Contains(model, ":") && target == entry+":latest")
	}
	return false
}

// EntryFor returns the exclude or ignore entry that names the finding: the
// target entry of a Docker object or an Ollama model, else its path. It
// reports false for a finding with neither, such as the stale worktree
// registrations of a repository, which are offered only when every one of
// them is gone.
func EntryFor(f *Finding) (string, bool) {
	switch {
	case f.Category == CategoryDocker && f.Target != "":
		return DockerEntryPrefix + f.Target, true
	case strings.HasPrefix(f.Target, OllamaEntryPrefix):
		return f.Target, true
	case f.Path != "":
		return f.Path, true
	}
	return "", false
}

func isHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
