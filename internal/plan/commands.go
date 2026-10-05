package plan

import (
	"regexp"
	"slices"
)

// cleanCommand is one command apply may run.
type cleanCommand struct {
	// argv is the exact command, or its fixed prefix when arg is set.
	argv []string
	// arg, when set, means the command takes exactly one more argument, which
	// must match it in full.
	arg *regexp.Regexp
	// locate is the read only query that reports the directory the command
	// cleans, such as "npm config get cache". Apply runs it again right
	// before the command and refuses when the answer moved.
	locate []string
	// sub is joined to the answer of locate to get the cleaned directory.
	sub string
}

var (
	// uuidArg matches a simulator device or runtime identifier.
	uuidArg = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)
	// modelArg matches an Ollama model name such as "llama3:latest" or
	// "namespace/model:tag". It never starts with a dash, so it cannot be
	// read as a flag.
	modelArg = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:-]{0,199}$`)
)

// cleanCommands is the complete list of commands that plans may contain.
func cleanCommands() []cleanCommand {
	return []cleanCommand{
		{argv: []string{"npm", "cache", "clean", "--force"}, locate: []string{"npm", "config", "get", "cache"}, sub: "_cacache"},
		{argv: []string{"yarn", "cache", "clean"}, locate: []string{"yarn", "cache", "dir"}},
		{argv: []string{"bun", "pm", "cache", "rm"}, locate: []string{"bun", "pm", "cache"}},
		{argv: []string{"uv", "cache", "clean"}, locate: []string{"uv", "cache", "dir"}},
		{argv: []string{"pip3", "cache", "purge"}, locate: []string{"pip3", "cache", "dir"}},
		{argv: []string{"pip", "cache", "purge"}, locate: []string{"pip", "cache", "dir"}},
		{argv: []string{"go", "clean", "-modcache"}, locate: []string{"go", "env", "GOMODCACHE"}},
		{argv: []string{"go", "clean", "-cache"}, locate: []string{"go", "env", "GOCACHE"}},
		{argv: []string{"brew", "cleanup", "-s"}, locate: []string{"brew", "--cache"}},
		{argv: []string{"composer", "clear-cache"}, locate: []string{"composer", "config", "--global", "cache-dir"}},
		{argv: []string{"xcrun", "simctl", "delete"}, arg: uuidArg},
		{argv: []string{"xcrun", "simctl", "runtime", "delete"}, arg: uuidArg},
		{argv: []string{"ollama", "rm"}, arg: modelArg},
	}
}

func (c cleanCommand) matches(argv []string) bool {
	if c.arg == nil {
		return slices.Equal(c.argv, argv)
	}
	n := len(c.argv)
	return len(argv) == n+1 && slices.Equal(c.argv, argv[:n]) && c.arg.MatchString(argv[n])
}

func lookupCommand(argv []string) (cleanCommand, bool) {
	for _, c := range cleanCommands() {
		if c.matches(argv) {
			return c, true
		}
	}
	return cleanCommand{}, false
}

// AllowedCommand reports whether argv is one of the commands that scanners
// emit: a tool's own clean command, or a removal command whose single
// argument has the expected form, such as "ollama rm llama3:latest". Apply
// refuses every other command, so a plan file cannot run arbitrary programs.
func AllowedCommand(argv []string) bool {
	_, ok := lookupCommand(argv)
	return ok
}

// LocateQuery returns the read only query that reports the directory a clean
// command acts on, and the relative path to join to its answer. ok is false
// for commands whose target is not a tool's cache directory.
func LocateQuery(argv []string) (query []string, sub string, ok bool) {
	c, found := lookupCommand(argv)
	if !found || len(c.locate) == 0 {
		return nil, "", false
	}
	return slices.Clone(c.locate), c.sub, true
}
