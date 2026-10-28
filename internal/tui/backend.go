// Package tui is the interactive mode of reclaim: one screen that walks
// through scan, review, confirm, removing and done, built as one bubbletea
// model per screen and a root model that switches between them.
//
// The screens remove nothing themselves. They import neither os nor
// os/exec, keep no global state, and reach the machine only through the
// Backend interface, which cmd/reclaim implements over the same code the
// scan, select and apply commands use. The only method of Backend that
// removes anything is Apply, which takes the path of the plan SavePlan
// wrote, and the screens call it only after yes is typed on the Confirm
// screen, or with dryRun from its d key.
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
	// ModeFolder scans one folder with the single measuring walk of
	// reclaim here, which also gives the folder tab its breakdown, or the
	// folders given on the command line, as reclaim scan with paths does.
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
	// For one folder it is the depth of the folder breakdown instead, as
	// for reclaim here, and the project walk has no limit.
	Depth int
	// All looks outside the folders of ModeFolder too, as --all does.
	All bool
	// DockerLabels limit Docker findings to objects with these labels,
	// written key=value or key.
	DockerLabels []string
	// GitHub lets the worktree scanners ask gh for pull request state.
	GitHub bool
}

// ScanResult is what a scan found.
type ScanResult struct {
	// Report is the report, as reclaim scan, here or worktrees writes it.
	Report *finding.Report
	// Folder is the breakdown of the folder "This folder" measured, nil
	// for the other modes and for several folders.
	Folder *Folder
}

// Folder is the breakdown of one folder by size, from the measuring walk of
// reclaim here, for the folder tab of the Review screen.
type Folder struct {
	// Path is the folder.
	Path string
	// Size is its size, everything below it included.
	Size int64
	// Entries are its entries, largest first.
	Entries []FolderEntry
}

// FolderEntry is one entry of a folder breakdown.
type FolderEntry struct {
	// Name is the base name and Path the full path.
	Name, Path string
	// Dir reports whether the entry is a folder.
	Dir bool
	// Size is the size of the entry, everything below it included.
	Size int64
	// Worktree is the state in words of a linked worktree, empty for any
	// other entry.
	Worktree string
	// Entries are the entries of a folder, largest first, down to the
	// depth of the breakdown.
	Entries []FolderEntry
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
// a fake. The screens never remove anything themselves; the only method
// here that removes anything is Apply, and it takes a plan path. Ignore
// writes the ignore list and nothing else.
type Backend interface {
	// Scan runs the scanners the request names and reports progress
	// through events. Canceling ctx stops the scan and returns what was
	// found so far, with a warning that the scan was interrupted. A
	// finished scan is saved as the last report, as reclaim scan does.
	Scan(ctx context.Context, req ScanRequest, progress func(scan.Event)) (ScanResult, error)
	// Protected returns the protected list in words, built by apply from
	// the tables its checks use.
	Protected(roots []string) []apply.ProtectedEntry
	// SavePlan builds a plan from a report and the selected findings with
	// the code select uses, proves the selected worktrees as select does,
	// and saves the plan under the reclaim folder.
	SavePlan(ctx context.Context, r *finding.Report, selected []finding.Finding) (SavedPlan, error)
	// Apply runs the plan file at path with apply.Run, as reclaim apply
	// does with --keep-going, and reports progress through the callbacks.
	// With dryRun it runs every check and removes nothing. This is the only
	// method that removes anything, and it takes nothing but the path of a
	// plan SavePlan wrote.
	Apply(ctx context.Context, path string, dryRun bool, progress apply.Progress) (apply.Summary, error)
	// FreeSpace reports the free bytes of the volume holding path.
	FreeSpace(path string) (int64, error)
	// Ignore adds entries to the ignore list and drops entries from it,
	// each a clean absolute path or a target entry such as docker:<id>,
	// and returns the list as written. It writes the ignore list and
	// nothing else.
	Ignore(add, drop []string) ([]string, error)
}
