package apply

import (
	"strings"

	"github.com/mandloideep/reclaim/internal/plan"
)

// The checks of the actions other than worktree removal, by name, in the
// order they run. Every outcome carries the checks that ran, and the log
// lists them.
const (
	// CheckAction refuses an action that cannot be applied, such as one
	// listed for attention only.
	CheckAction = "applicable"
	// CheckSudo marks an action that needs administrator rights, which is
	// printed and never run.
	CheckSudo = "needs sudo"
	// CheckRules are the rules that depend only on the path: clean and
	// absolute, four components, not protected, not inside .git, not the
	// home folder, a root or the project root, inside a recorded root.
	CheckRules = "path rules"
	// CheckExcluded refuses a target that is, lies under or holds an
	// exclude entry of the configuration.
	CheckExcluded = "not excluded"
	// CheckObject checks what is on disk now: it exists, is the kind the
	// scan found, has no symbolic link in its path, is not a mount point
	// and holds no .git entry or project manifest.
	CheckObject = "same kind of object"
	// CheckMounts measures the target and refuses another filesystem
	// mounted inside it.
	CheckMounts = "nothing mounted inside"
	// CheckLastLook looks once more, right before removing, through an
	// os.Root at the recorded root, that the object is the one checked.
	CheckLastLook = "unchanged since checked"
	// CheckAllowed refuses a command that is not on the fixed list.
	CheckAllowed = "allowed command"
	// CheckTool finds the tool that runs the command.
	CheckTool = "tool installed"
	// CheckLocation asks the tool again where its cache is.
	CheckLocation = "cache location"
	// CheckTarget checks the target a removal command names, such as an
	// Ollama model or a simulator.
	CheckTarget = "command target"
	// CheckDaemon reaches the Docker daemon.
	CheckDaemon = "Docker daemon"
	// CheckDockerUse checks that nothing uses the Docker object in a way
	// that forbids removing it.
	CheckDockerUse = "not in use"
	// CheckTags checks that an image's tags did not change since the scan.
	CheckTags = "tags unchanged"
	// CheckRepo checks the repository whose registrations are pruned.
	CheckRepo = "repository"
	// CheckRegistrations asks git what a prune would remove.
	CheckRegistrations = "registrations"
)

// checks accumulates the checks of one action and builds its outcome.
type checks struct {
	list []Check
}

func (c *checks) pass(name, detail string) {
	c.list = append(c.list, Check{Name: name, OK: true, Detail: detail})
}

// fail records a failed check and returns the outcome that carries it.
func (c *checks) fail(name string, err error) Outcome {
	c.list = append(c.list, Check{Name: name, Detail: err.Error()})
	return Outcome{Checks: c.list, Err: err}
}

// gone records that the target no longer exists, which is not a failure.
func (c *checks) gone(name, detail string) Outcome {
	c.list = append(c.list, Check{Name: name, OK: true, Detail: detail})
	return Outcome{Checks: c.list, Err: errGone}
}

// done returns o carrying the checks.
func (c *checks) done(o Outcome) Outcome {
	o.Checks = append(c.list, o.Checks...)
	return o
}

// argumentWords says what the check of a command's target found, for the
// commands that name their target.
func argumentWords(a *plan.Action) string {
	switch argv := a.Command; {
	case len(argv) == 3 && argv[0] == "ollama" && argv[1] == "rm":
		return "the manifest of " + argv[2] + " is still in " + a.Path + ", and Ollama runs on this machine"
	case len(argv) == 4 && strings.Join(argv[:3], " ") == "xcrun simctl delete":
		return "simulator " + argv[3] + " is still unavailable"
	}
	return ""
}
