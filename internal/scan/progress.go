package scan

import (
	"slices"
	"strings"

	"github.com/mandloideep/reclaim/internal/finding"
)

// EventKind says what a scan event reports.
type EventKind int

// Event kinds, in the order a run sends them.
const (
	// Planned is sent once, before any scanner starts, with every scanner
	// the run will start, so a screen can show what is still waiting.
	Planned EventKind = iota + 1
	// ScannerStarted is sent when a scanner begins.
	ScannerStarted
	// Step is sent by a scanner, or for the shared project walk, during a
	// long phase, with a short text such as "asking GitHub about 4 branches".
	Step
	// ScannerFinished is sent when a scanner returns, with what it found and
	// the error it returned.
	ScannerFinished
)

// Event reports the progress of a scan. Events are sent from the goroutines
// that run the scanners, so a receiver must be safe for concurrent use.
type Event struct {
	// Kind says what happened.
	Kind EventKind
	// Scanner is the scanner the event is about, empty for Planned and for
	// the steps of the shared project walk.
	Scanner string
	// Category is the category of the scanner, or the category whose row
	// shows a step of the shared project walk.
	Category finding.Category
	// Text says what the scanner is doing, for Step.
	Text string
	// Folders and Projects count what the shared project walk has visited
	// and found, for its steps.
	Folders, Projects int
	// Findings counts the findings that can be applied and Size is their
	// reclaimable size, for ScannerFinished.
	Findings int
	Size     int64
	// Err is the error the scanner returned or the panic it raised, for
	// ScannerFinished. The same text lands in the report's warnings.
	Err error
	// Scanners lists every scanner the run will start, for Planned.
	Scanners []PlannedScanner
}

// PlannedScanner names one scanner of a run and its category.
type PlannedScanner struct {
	// Name is the scanner's name.
	Name string
	// Category is the scanner's category.
	Category finding.Category
}

// Step reports that the scanner is in a long phase, such as asking GitHub
// about branches or listing Docker images. The runner fills in the scanner
// and its category.
func (e *Env) Step(text string) {
	if e.Progress != nil {
		e.Progress(Event{Kind: Step, Text: text})
	}
}

// boundProgress returns a Progress function that marks every event with the
// scanner and its category, or nil when there is nowhere to send them.
func boundProgress(progress func(Event), s Scanner) func(Event) {
	if progress == nil {
		return nil
	}
	name, category := s.Name(), s.Category()
	return func(ev Event) {
		if ev.Scanner == "" {
			ev.Scanner = name
		}
		if ev.Category == "" {
			ev.Category = category
		}
		progress(ev)
	}
}

// interruptedPrefix starts the warning a scan whose context ended adds.
const interruptedPrefix = "scan interrupted: "

// Interrupted reports whether the warnings of a run say the scan was
// interrupted before every scanner finished, which is what decides that
// its report is partial: it is shown but not saved as the last report.
func Interrupted(ws []finding.Warning) bool {
	return slices.ContainsFunc(ws, func(w finding.Warning) bool {
		return w.Scanner == "" && strings.HasPrefix(w.Message, interruptedPrefix)
	})
}
