// Package plan defines the plan file that select writes and apply executes.
//
// A plan is a JSON file listing exactly what apply will remove. It is meant
// to be readable and editable by hand, so apply validates it strictly: the
// schema version, every action, every id against its scanner and target, and
// every command against a fixed allowlist.
package plan

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mandloideep/reclaim/internal/finding"
)

// Version is the plan schema version this build reads and writes.
const Version = 1

// MaxAge is how old the scan behind a plan may be before apply refuses it
// without --stale-ok.
const MaxAge = 24 * time.Hour

// Plan is the content of a plan file.
type Plan struct {
	// Version is the schema version.
	Version int `json:"version"`
	// Created is when the plan was written.
	Created time.Time `json:"created"`
	// Scanned is when the scan the plan was built from finished.
	Scanned time.Time `json:"scanned,omitzero"`
	// Host is the machine the scan ran on.
	Host string `json:"host"`
	// Roots are the directories RemovePath actions must stay inside.
	Roots []string `json:"roots"`
	// Actions are executed in order.
	Actions []Action `json:"actions"`
}

// Action is one removal.
type Action struct {
	// ID is the finding id, a hash of Scanner and Target.
	ID string `json:"id"`
	// Action is what to do.
	Action finding.Action `json:"action"`
	// Path is the filesystem path for RemovePath and RunCommand actions.
	Path string `json:"path,omitempty"`
	// Kind is the kind of object at Path, checked again before removal.
	Kind finding.Kind `json:"kind,omitempty"`
	// Target is the Docker object or, for filesystem actions, the path.
	Target string `json:"target"`
	// Name is a human readable label.
	Name string `json:"name,omitempty"`
	// Tags are the image tags at scan time, for DockerRemoveImage.
	Tags []string `json:"tags,omitempty"`
	// Size is the size measured at scan time.
	Size int64 `json:"size"`
	// Tier is the finding tier.
	Tier finding.Tier `json:"tier"`
	// Scanner is the scanner that produced the finding.
	Scanner string `json:"scanner"`
	// Category is the finding category.
	Category finding.Category `json:"category,omitempty"`
	// Project is the owning project, which apply never removes.
	Project string `json:"project,omitempty"`
	// Restore explains how to get it back.
	Restore string `json:"restore,omitempty"`
	// Command is the argv of a RunCommand action.
	Command []string `json:"command,omitempty"`
	// NeedsSudo actions are printed for the user and never executed.
	NeedsSudo bool `json:"needs_sudo,omitempty"`
	// Warning is shown before the action runs.
	Warning string `json:"warning,omitempty"`
}

// Label is a short description of the action for terminal output.
func (a *Action) Label() string {
	switch {
	case a.Name != "" && a.Path != "":
		return a.Name + " " + a.Path
	case a.Name != "":
		return a.Name
	case a.Path != "":
		return a.Path
	default:
		return a.Target
	}
}

// FromFinding converts a finding into a plan action.
func FromFinding(f *finding.Finding) Action {
	return Action{
		ID:        f.ID,
		Action:    f.Action,
		Path:      f.Path,
		Kind:      f.Kind,
		Target:    f.Target,
		Name:      f.Name,
		Tags:      slices.Clone(f.Tags),
		Size:      f.Size,
		Tier:      f.Tier,
		Scanner:   f.Scanner,
		Category:  f.Category,
		Project:   f.Project,
		Restore:   f.Restore,
		Command:   slices.Clone(f.Command),
		NeedsSudo: f.NeedsSudo,
		Warning:   f.Warning,
	}
}

// New builds a plan from selected findings of a report. Actions are ordered
// so containers go before the images and volumes they may hold, then by size.
// Findings listed for attention only cannot be applied and are refused.
func New(r *finding.Report, selected []finding.Finding, host string, now time.Time) (*Plan, error) {
	for i := range selected {
		if !selected[i].Actionable() {
			return nil, fmt.Errorf("%s is listed for attention only and cannot be put in a plan", selected[i].DisplayName())
		}
	}
	p := &Plan{
		Version: Version,
		Created: now.UTC(),
		Scanned: r.Created.UTC(),
		Host:    host,
		Roots:   slices.Clone(r.Roots),
		Actions: make([]Action, 0, len(selected)),
	}
	for i := range selected {
		p.Actions = append(p.Actions, FromFinding(&selected[i]))
	}
	order := map[finding.Action]int{
		finding.ActionDockerRemoveContainer: 0,
		finding.ActionDockerRemoveImage:     1,
		finding.ActionDockerRemoveVolume:    2,
		finding.ActionDockerPruneBuildCache: 3,
		finding.ActionRemovePath:            4,
		finding.ActionRunCommand:            5,
	}
	slices.SortStableFunc(p.Actions, func(a, b Action) int {
		if d := cmp.Compare(order[a.Action], order[b.Action]); d != 0 {
			return d
		}
		return cmp.Compare(b.Size, a.Size)
	})
	return p, nil
}

// Preset returns the tiers a named preset selects: "safe" selects tier A and
// "aggressive" selects tiers A and B. No preset ever selects tier C.
func Preset(name string) ([]finding.Tier, error) {
	switch name {
	case "safe":
		return []finding.Tier{finding.TierA}, nil
	case "aggressive":
		return []finding.Tier{finding.TierA, finding.TierB}, nil
	default:
		return nil, fmt.Errorf("unknown preset %q, want safe or aggressive", name)
	}
}

// SelectTiers returns the findings in the given tiers, never tier C and never
// a finding listed for attention only.
func SelectTiers(fs []finding.Finding, tiers []finding.Tier) []finding.Finding {
	var out []finding.Finding
	for _, f := range fs {
		if f.Tier != finding.TierC && f.Actionable() && slices.Contains(tiers, f.Tier) {
			out = append(out, f)
		}
	}
	return out
}

// TotalSize sums the sizes of the actions.
func (p *Plan) TotalSize() int64 {
	var n int64
	for i := range p.Actions {
		n += p.Actions[i].Size
	}
	return n
}

// Load reads and validates a plan file. Unknown fields are rejected so that a
// typo in a hand edited plan is an error instead of a silently ignored field.
func Load(path string) (*Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read plan %s: %w", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var p Plan
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("parse plan %s: %w", path, err)
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("plan %s: %w", path, err)
	}
	return &p, nil
}

// Save writes the plan as indented JSON, replacing path atomically.
func Save(path string, p *Plan) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("encode plan: %w", err)
	}
	return WriteFileAtomic(path, append(data, '\n'))
}

// WriteFileAtomic writes data to a temporary file next to path and renames it
// over path, so readers never see a partial file.
func WriteFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// Validate checks the plan's schema and every action.
func (p *Plan) Validate() error {
	if p.Version != Version {
		return fmt.Errorf("unsupported plan version %d, this build reads version %d", p.Version, Version)
	}
	if p.Created.IsZero() {
		return errors.New("plan has no creation time")
	}
	for _, r := range p.Roots {
		if !filepath.IsAbs(r) || filepath.Clean(r) != r {
			return fmt.Errorf("root %q is not a clean absolute path", r)
		}
	}
	seen := map[string]bool{}
	for i := range p.Actions {
		a := &p.Actions[i]
		if err := a.validate(); err != nil {
			return fmt.Errorf("action %d (%s): %w", i+1, a.Label(), err)
		}
		if seen[a.ID] {
			return fmt.Errorf("action %d (%s): duplicate id", i+1, a.Label())
		}
		seen[a.ID] = true
	}
	return nil
}

func (a *Action) validate() error {
	if a.Action == finding.ActionNone {
		return errors.New("the finding is listed for attention only and cannot be applied")
	}
	if !a.Action.Valid() {
		return fmt.Errorf("unknown action %q", a.Action)
	}
	if !a.Tier.Valid() {
		return fmt.Errorf("unknown tier %q", a.Tier)
	}
	if a.Scanner == "" || a.Target == "" {
		return errors.New("scanner and target are required")
	}
	if a.ID != finding.MakeID(a.Scanner, a.Target) {
		return errors.New("id does not match scanner and target, the plan was edited inconsistently")
	}
	if a.Path != "" && (!filepath.IsAbs(a.Path) || filepath.Clean(a.Path) != a.Path) {
		return fmt.Errorf("path %q is not a clean absolute path", a.Path)
	}
	switch a.Action {
	case finding.ActionRemovePath:
		if a.Path == "" || a.Target != a.Path {
			return errors.New("RemovePath needs a path equal to its target")
		}
		if !a.Kind.Valid() {
			return fmt.Errorf("RemovePath needs kind dir or file, got %q", a.Kind)
		}
		if len(a.Command) > 0 {
			return errors.New("RemovePath must not carry a command")
		}
	case finding.ActionRunCommand:
		c, ok := lookupCommand(a.Command)
		if !ok {
			return fmt.Errorf("command %q is not one of the clean commands reclaim knows", strings.Join(a.Command, " "))
		}
		if c.target != nil && !c.target(a.Command[len(a.Command)-1], a.Path, a.Target) {
			return fmt.Errorf("command %q does not act on the target %s, the plan was edited inconsistently", strings.Join(a.Command, " "), a.Target)
		}
	default:
		if a.Path != "" || len(a.Command) > 0 {
			return fmt.Errorf("%s must not carry a path or command", a.Action)
		}
	}
	return nil
}

// CheckAge refuses a plan whose scan is older than MaxAge unless staleOK is
// set. The age is measured from the scan when known, else from the plan.
func (p *Plan) CheckAge(now time.Time, staleOK bool) error {
	ref := p.Created
	if !p.Scanned.IsZero() && p.Scanned.Before(ref) {
		ref = p.Scanned
	}
	age := now.Sub(ref)
	if age > MaxAge && !staleOK {
		return fmt.Errorf("the scan behind this plan is %s old, more than %s; scan again or pass --stale-ok", age.Round(time.Minute), MaxAge)
	}
	return nil
}
