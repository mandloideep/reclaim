// Package tui is the interactive mode of reclaim: one screen that walks
// through scan, review and plan, built as one bubbletea model per screen
// and a root model that switches between them.
//
// The screens remove nothing themselves. They import neither os nor
// os/exec, keep no global state, and reach the machine only through the
// Backend interface, which cmd/reclaim implements over the same code the
// scan, select and apply commands use. No method of Backend removes
// anything: the screens scan, show the protected list and save a plan, and
// the plan is applied by reclaim apply.
package tui

import (
	"context"
	"time"

	"github.com/mandloideep/reclaim/internal/apply"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/scan"
)

// Mode is what a scan looks at.
type Mode int

// Modes, in the order the Start screen lists them.
const (
	// ModeMachine scans the whole machine, as reclaim scan does.
	ModeMachine Mode = iota
	// ModeFolder scans one folder, or the folders given on the command
	// line, as reclaim scan with paths does.
	ModeFolder
	// ModeWorktrees runs the worktree scanners alone, as reclaim worktrees
	// does.
	ModeWorktrees
)

// ScanRequest is what the Start screen asks the backend to scan.
type ScanRequest struct {
	// Mode is the whole machine, folders or worktrees.
	Mode Mode
	// Paths are the folders of ModeFolder, as the user gave them.
	Paths []string
	// Categories are the categories ticked on the Start screen. The backend
	// runs the scanners of these categories that the configuration and the
	// --category flag leave enabled, or every scanner of a category whose
	// scanners are all disabled, because ticking it asks for it.
	Categories []finding.Category
	// MinSize hides findings smaller than this many bytes.
	MinSize int64
	// Stale keeps only project findings with no activity for this long.
	// Zero keeps every finding.
	Stale time.Duration
	// Depth limits the project walk below each root, zero for no limit.
	Depth int
	// All looks outside the folders of ModeFolder too, as --all does.
	All bool
	// DockerLabels limit Docker findings to objects with these labels,
	// written key=value or key.
	DockerLabels []string
	// GitHub lets the worktree scanners ask gh for pull request state.
	GitHub bool
}

// SavedPlan is a plan the backend built and saved.
type SavedPlan struct {
	// Path is the plan file, empty when nothing was left to save.
	Path string
	// Plan is the plan as saved.
	Plan *plan.Plan
	// LeftOut names the selected worktrees that failed checks 1 to 5 of the
	// proof and were left out of the plan, with the check that failed.
	LeftOut []string
}

// Backend is everything the screens need from the machine. cmd/reclaim
// implements it over the scan, plan and apply code; tests give the screens
// a fake. The screens never remove anything themselves, and no method here
// removes anything.
type Backend interface {
	// Scan runs the scanners the request names and reports progress
	// through events. Canceling ctx stops the scan and returns what was
	// found so far, with a warning that the scan was interrupted. A
	// finished scan is saved as the last report, as reclaim scan does.
	Scan(ctx context.Context, req ScanRequest, progress func(scan.Event)) (*finding.Report, error)
	// Protected returns the protected list in words, built by apply from
	// the tables its checks use.
	Protected(roots []string) []apply.ProtectedEntry
	// SavePlan builds a plan from a report and the selected findings with
	// the code select uses, proves the selected worktrees as select does,
	// and saves the plan under the reclaim folder.
	SavePlan(ctx context.Context, r *finding.Report, selected []finding.Finding) (SavedPlan, error)
}
