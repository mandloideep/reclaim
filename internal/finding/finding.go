// Package finding defines the data model shared by scanners, reports and plans.
//
// The JSON encoding of these types is a stable, versioned interface: scripts
// consume it and plan files are built from it. Fields may be added, but
// existing fields keep their names and meaning within a report version.
package finding

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"
)

// ReportVersion is the version of the report JSON schema written by this build.
const ReportVersion = 1

// Tier says how expensive it is to get a finding back after removing it.
type Tier string

// Tiers, from cheapest to most expensive to restore.
const (
	// TierA regenerates itself the next time the project builds or installs.
	TierA Tier = "A"
	// TierB can be downloaded or rebuilt again but costs real time or bandwidth.
	TierB Tier = "B"
	// TierC is data. It is never preselected and must be picked individually.
	TierC Tier = "C"
)

// Tiers lists every valid tier in order.
func Tiers() []Tier { return []Tier{TierA, TierB, TierC} }

// Valid reports whether t is one of the known tiers.
func (t Tier) Valid() bool { return slices.Contains(Tiers(), t) }

// ParseTier parses a tier letter, case-insensitively.
func ParseTier(s string) (Tier, error) {
	t := Tier(strings.ToUpper(strings.TrimSpace(s)))
	if !t.Valid() {
		return "", fmt.Errorf("unknown tier %q, want A, B or C", s)
	}
	return t, nil
}

// Category groups findings in reports.
type Category string

// Known categories.
const (
	// CategoryProject covers build artifacts and dependency folders inside projects.
	CategoryProject Category = "project"
	// CategoryPackageCache covers package manager stores and caches.
	CategoryPackageCache Category = "package-cache"
	// CategoryDocker covers Docker containers, images, volumes and build cache.
	CategoryDocker Category = "docker"
	// CategoryAppCache covers application caches, downloaded browsers and
	// models, Xcode build data and the leftovers of agent tools.
	CategoryAppCache Category = "app-cache"
	// CategoryDownloads covers installers and archives in the Downloads folder,
	// and large old files listed for attention only.
	CategoryDownloads Category = "downloads"
)

// Categories lists every known category in report order.
func Categories() []Category {
	return []Category{CategoryProject, CategoryPackageCache, CategoryDocker, CategoryAppCache, CategoryDownloads}
}

// Title is the human readable section heading for the category.
func (c Category) Title() string {
	switch c {
	case CategoryProject:
		return "Project artifacts"
	case CategoryPackageCache:
		return "Package caches"
	case CategoryDocker:
		return "Docker"
	case CategoryAppCache:
		return "App caches"
	case CategoryDownloads:
		return "Downloads"
	default:
		return string(c)
	}
}

// Action is what apply does to remove a finding.
type Action string

// Known actions. Plans containing any other action are refused.
const (
	// ActionRemovePath removes a file or directory with os.RemoveAll.
	ActionRemovePath Action = "RemovePath"
	// ActionRunCommand runs the tool's own clean command, such as "brew cleanup -s".
	ActionRunCommand Action = "RunCommand"
	// ActionDockerRemoveContainer removes a stopped container.
	ActionDockerRemoveContainer Action = "DockerRemoveContainer"
	// ActionDockerRemoveImage removes an image that no container uses.
	ActionDockerRemoveImage Action = "DockerRemoveImage"
	// ActionDockerRemoveVolume removes a volume that no running container uses.
	ActionDockerRemoveVolume Action = "DockerRemoveVolume"
	// ActionDockerPruneBuildCache prunes the unused Docker build cache.
	ActionDockerPruneBuildCache Action = "DockerPruneBuildCache"
	// ActionNone marks a finding that is shown for attention only, such as a
	// large old file in Downloads. Select never puts it in a plan and apply
	// refuses a plan that contains it.
	ActionNone Action = "None"
)

// Actions lists every known action.
func Actions() []Action {
	return []Action{
		ActionRemovePath,
		ActionRunCommand,
		ActionDockerRemoveContainer,
		ActionDockerRemoveImage,
		ActionDockerRemoveVolume,
		ActionDockerPruneBuildCache,
		ActionNone,
	}
}

// Valid reports whether a is one of the known actions.
func (a Action) Valid() bool { return slices.Contains(Actions(), a) }

// IsDocker reports whether the action talks to the Docker daemon.
func (a Action) IsDocker() bool {
	switch a {
	case ActionDockerRemoveContainer, ActionDockerRemoveImage, ActionDockerRemoveVolume, ActionDockerPruneBuildCache:
		return true
	default:
		return false
	}
}

// Applicable reports whether apply can act on the action. Every known action
// is applicable except ActionNone.
func (a Action) Applicable() bool { return a.Valid() && a != ActionNone }

// Kind records what kind of filesystem object a path finding is, so apply can
// check that the object did not change kind between scan and removal.
type Kind string

// Filesystem kinds.
const (
	// KindDir is a directory.
	KindDir Kind = "dir"
	// KindFile is a regular file.
	KindFile Kind = "file"
)

// Valid reports whether k is one of the known kinds.
func (k Kind) Valid() bool { return k == KindDir || k == KindFile }

// Finding is one thing that can be reclaimed.
type Finding struct {
	// ID is a stable hash of the scanner name and the target.
	ID string `json:"id"`
	// Scanner is the name of the scanner that produced the finding.
	Scanner string `json:"scanner"`
	// Category groups the finding in reports.
	Category Category `json:"category"`
	// Tier says how expensive the finding is to restore.
	Tier Tier `json:"tier"`
	// Path is the filesystem path, empty for targets that are not files.
	Path string `json:"path,omitempty"`
	// Kind is the kind of filesystem object at Path, empty for other targets.
	Kind Kind `json:"kind,omitempty"`
	// Target is a Docker object id, a volume name, or the same as Path.
	Target string `json:"target"`
	// Name is a short human readable label, such as an image tag.
	Name string `json:"name,omitempty"`
	// Tags are the tags of an image at scan time. Apply refuses an image
	// whose tags changed since, so a re-tagged image is never removed.
	Tags []string `json:"tags,omitempty"`
	// Size is the number of bytes expected to be freed, apparent size on disk.
	Size int64 `json:"size"`
	// Project is the owning project root when known.
	Project string `json:"project,omitempty"`
	// LastUsed is a best effort time of last use.
	LastUsed time.Time `json:"last_used,omitzero"`
	// Restore explains how to get the finding back.
	Restore string `json:"restore,omitempty"`
	// Action is what apply does to remove the finding.
	Action Action `json:"action"`
	// Command is the exact argv for RunCommand actions.
	Command []string `json:"command,omitempty"`
	// NeedsSudo marks findings that apply prints as a command and never executes.
	NeedsSudo bool `json:"needs_sudo,omitempty"`
	// Warning is shown next to the finding, always set for tier C.
	Warning string `json:"warning,omitempty"`
}

// Actionable reports whether the finding can be selected and applied. Findings
// with ActionNone are listed for attention only.
func (f *Finding) Actionable() bool { return f.Action.Applicable() }

// DockerKind names the kind of Docker object a Docker finding is, for
// grouping: "build cache", "stopped containers", "dangling images", "unused
// images" or "volumes". It returns an empty string for other findings.
func (f *Finding) DockerKind() string {
	switch f.Action {
	case ActionDockerPruneBuildCache:
		return DockerKindBuildCache
	case ActionDockerRemoveContainer:
		return DockerKindContainers
	case ActionDockerRemoveImage:
		if len(f.Tags) == 0 {
			return DockerKindDangling
		}
		return DockerKindImages
	case ActionDockerRemoveVolume:
		return DockerKindVolumes
	default:
		return ""
	}
}

// Docker finding kinds, in the order reports list them.
const (
	// DockerKindBuildCache is the unused build cache.
	DockerKindBuildCache = "build cache"
	// DockerKindContainers are stopped containers.
	DockerKindContainers = "stopped containers"
	// DockerKindDangling are images without a tag.
	DockerKindDangling = "dangling images"
	// DockerKindImages are tagged images that no container uses.
	DockerKindImages = "unused images"
	// DockerKindVolumes are volumes that no running container uses.
	DockerKindVolumes = "volumes"
)

// DockerKinds lists the Docker finding kinds in report order.
func DockerKinds() []string {
	return []string{DockerKindBuildCache, DockerKindContainers, DockerKindDangling, DockerKindImages, DockerKindVolumes}
}

// MakeID returns the stable id for a scanner and target pair.
func MakeID(scanner, target string) string {
	sum := sha256.Sum256([]byte(scanner + "\x00" + target))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// DisplayName is the label used in tables: the name when set, else the path,
// else the target.
func (f *Finding) DisplayName() string {
	switch {
	case f.Name != "":
		return f.Name
	case f.Path != "":
		return f.Path
	default:
		return f.Target
	}
}

// Warning is a non fatal problem met during a scan, such as an unreadable
// directory or a scanner that could not run.
type Warning struct {
	// Scanner is the scanner that raised the warning, empty for the runner.
	Scanner string `json:"scanner,omitempty"`
	// Path is the path the warning is about, when there is one.
	Path string `json:"path,omitempty"`
	// Message describes the problem.
	Message string `json:"message"`
}

// String formats the warning for terminal output.
func (w Warning) String() string {
	var b strings.Builder
	if w.Scanner != "" {
		b.WriteString(w.Scanner)
		b.WriteString(": ")
	}
	if w.Path != "" {
		b.WriteString(w.Path)
		b.WriteString(": ")
	}
	b.WriteString(w.Message)
	return b.String()
}

// Scope records what produced a report, so select can say where its findings
// came from.
type Scope struct {
	// Command is the command that wrote the report: "scan" or "here".
	Command string `json:"command"`
	// Args are the command line arguments after the command name, with the
	// flags that were set written as --name=value.
	Args []string `json:"args,omitempty"`
	// Paths are the resolved path arguments. Empty for a scan of the
	// configured roots.
	Paths []string `json:"paths,omitempty"`
	// Roots are the directories the project walk covered.
	Roots []string `json:"roots,omitempty"`
}

// Partial reports whether the report covers only some folders rather than
// the whole machine: it came from here, or from a scan given paths.
func (s *Scope) Partial() bool {
	return s != nil && (s.Command == "here" || len(s.Paths) > 0)
}

// Report is the result of a scan. Its JSON form is the input of select.
type Report struct {
	// Version is the report schema version, ReportVersion when written by this build.
	Version int `json:"version"`
	// Created is when the scan finished.
	Created time.Time `json:"created"`
	// Host is the machine the scan ran on.
	Host string `json:"host"`
	// OS is the operating system the scan ran on, as in runtime.GOOS.
	OS string `json:"os"`
	// Scope records the command that produced the report. Reports written
	// before scopes existed have none.
	Scope *Scope `json:"scope,omitempty"`
	// Roots are the directories filesystem findings may live under. Apply
	// refuses to remove paths outside them.
	Roots []string `json:"roots"`
	// Findings are sorted by category, then size descending.
	Findings []Finding `json:"findings"`
	// Warnings are non fatal problems met during the scan.
	Warnings []Warning `json:"warnings,omitempty"`
	// Notes are informational messages from scanners.
	Notes []string `json:"notes,omitempty"`
}

// TotalSize sums the size of every finding that can be applied. Findings
// listed for attention only are not reclaimable and are not counted.
func (r *Report) TotalSize() int64 {
	return ReclaimableSize(r.Findings)
}

// ReclaimableSize sums the size of the findings that can be applied.
func ReclaimableSize(fs []Finding) int64 {
	var total int64
	for i := range fs {
		if fs[i].Actionable() {
			total += fs[i].Size
		}
	}
	return total
}

// Validate checks that a decoded report has a supported version and that its
// findings are well formed.
func (r *Report) Validate() error {
	if r.Version != ReportVersion {
		return fmt.Errorf("unsupported report version %d, this build reads version %d", r.Version, ReportVersion)
	}
	for i := range r.Findings {
		f := &r.Findings[i]
		if !f.Tier.Valid() {
			return fmt.Errorf("finding %d (%s): unknown tier %q", i+1, f.DisplayName(), f.Tier)
		}
		if !f.Action.Valid() {
			return fmt.Errorf("finding %d (%s): unknown action %q", i+1, f.DisplayName(), f.Action)
		}
		if f.Scanner == "" || f.Target == "" {
			return fmt.Errorf("finding %d: scanner and target are required", i+1)
		}
		if f.ID != MakeID(f.Scanner, f.Target) {
			return fmt.Errorf("finding %d (%s): id does not match scanner and target", i+1, f.DisplayName())
		}
	}
	return nil
}

// Sort orders findings by category, then by size descending, then by id so the
// order is deterministic.
func Sort(fs []Finding) {
	rank := func(c Category) int {
		if i := slices.Index(Categories(), c); i >= 0 {
			return i
		}
		return len(Categories())
	}
	slices.SortStableFunc(fs, func(a, b Finding) int {
		if d := rank(a.Category) - rank(b.Category); d != 0 {
			return d
		}
		if a.Category != b.Category {
			return strings.Compare(string(a.Category), string(b.Category))
		}
		if a.Size != b.Size {
			if a.Size > b.Size {
				return -1
			}
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
}
