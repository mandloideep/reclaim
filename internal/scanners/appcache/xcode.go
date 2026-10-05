package appcache

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mandloideep/reclaim/internal/execx"
	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/plan"
	"github.com/mandloideep/reclaim/internal/project"
	"github.com/mandloideep/reclaim/internal/scan"
)

func scanDerivedData(ctx context.Context, env *scan.Env) ([]finding.Finding, error) {
	if env.GOOS != "darwin" {
		return nil, nil
	}
	def := resolve(filepath.Join(env.Home, "Library", "Developer", "Xcode", "DerivedData"))
	var entries []entry
	for _, e := range subdirs(env, def) {
		entries = append(entries, derivedDataFolder(def, e.Name()))
	}
	// Once a custom location is set Xcode builds there, but folders it
	// left in the default location stay, so both are scanned.
	if custom, ok := customDerivedData(ctx, env); ok && !project.IsWithin(custom, def) {
		for _, e := range subdirs(env, custom) {
			// A folder the user chose may hold other things than Xcode's
			// build folders, so only what Xcode made is offered there.
			if xcodeMade(filepath.Join(custom, e.Name())) {
				entries = append(entries, derivedDataFolder(custom, e.Name()))
			}
		}
	}
	return collect(ctx, env, entries)
}

func derivedDataFolder(dir, name string) entry {
	return entry{
		path:    filepath.Join(dir, name),
		tier:    finding.TierA,
		restore: "rebuilt by the next Xcode build of the project",
	}
}

// derivedDataKey is the Xcode setting that holds a custom DerivedData
// location, set in Xcode > Settings > Locations.
const derivedDataKey = "IDECustomDerivedDataLocation"

// customDerivedData returns the custom DerivedData location from Xcode's
// defaults. ok is false when none is set, and when the location does not lie
// strictly inside the home directory, because apply removes nothing outside
// the recorded roots; the report then says why.
func customDerivedData(ctx context.Context, env *scan.Env) (dir string, ok bool) {
	if env.Exec == nil {
		return "", false
	}
	// defaults fails when the key is not set.
	out, err := env.Exec.Output(ctx, "defaults", "read", "com.apple.dt.Xcode", derivedDataKey)
	if err != nil {
		if !errors.Is(err, execx.ErrNotInstalled) {
			env.Logger().Debug("no custom DerivedData location", "err", err)
		}
		return "", false
	}
	value := strings.TrimSpace(out)
	if value == "" {
		return "", false
	}
	if rest, found := strings.CutPrefix(value, "~/"); found {
		value = filepath.Join(env.Home, rest)
	}
	if !filepath.IsAbs(value) {
		env.Diag.Warn("", "Xcode's custom DerivedData location "+value+" is not an absolute path, so it is not scanned")
		return "", false
	}
	dir = resolve(filepath.Clean(value))
	home := resolve(env.Home)
	if dir == home || !project.IsWithin(dir, home) {
		env.Diag.Note("Xcode keeps DerivedData in " + dir + ", outside the home folder, so reclaim does not offer it; clear it from Xcode or remove its folders yourself.")
		return "", false
	}
	return dir, true
}

// xcodeCaches are the shared caches Xcode keeps in DerivedData.
func xcodeCaches() []string {
	return []string{"ModuleCache.noindex", "SymbolCache.noindex", "SDKStatCaches.noindex", "CompilationCache.noindex"}
}

// xcodeMade reports whether a folder in a DerivedData location is one Xcode
// made: one of its shared caches, or a project folder, which is named after
// the project and a 28 letter hash, such as
// "App-bxrjzktdwfaugpcvhzjmlzkhwqyi", and holds an info.plist that records
// the workspace it belongs to.
func xcodeMade(path string) bool {
	name := filepath.Base(path)
	if slices.Contains(xcodeCaches(), name) {
		return true
	}
	if !hashedName(name) {
		return false
	}
	plist := filepath.Join(path, "info.plist")
	info, err := os.Lstat(plist)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxManifestSize {
		return false
	}
	data, err := os.ReadFile(plist)
	return err == nil && bytes.Contains(data, []byte("WorkspacePath"))
}

// hashedName reports whether name ends in a dash and 28 lowercase letters.
func hashedName(name string) bool {
	i := strings.LastIndexByte(name, '-')
	if i <= 0 || len(name)-i-1 != 28 {
		return false
	}
	for _, r := range name[i+1:] {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

// simctl asks xcrun simctl for JSON. A missing Xcode is not an error: the
// command line tools alone have no simctl, and then there is nothing to find.
func simctl(ctx context.Context, env *scan.Env, v any, args ...string) (bool, error) {
	if env.Exec == nil {
		return false, nil
	}
	out, err := env.Exec.Output(ctx, "xcrun", append([]string{"simctl"}, args...)...)
	if err != nil {
		if !errors.Is(err, execx.ErrNotInstalled) {
			env.Logger().Debug("simctl query failed", "args", args, "err", err)
		}
		return false, nil
	}
	if err := json.Unmarshal([]byte(out), v); err != nil {
		return false, fmt.Errorf("parse xcrun simctl %s: %w", strings.Join(args, " "), err)
	}
	return true, nil
}

// simDevice is one entry of "xcrun simctl list -j devices".
type simDevice struct {
	UDID              string `json:"udid"`
	Name              string `json:"name"`
	IsAvailable       bool   `json:"isAvailable"`
	AvailabilityError string `json:"availabilityError"`
}

func scanSimulatorDevices(ctx context.Context, env *scan.Env) ([]finding.Finding, error) {
	if env.GOOS != "darwin" {
		return nil, nil
	}
	var list struct {
		Devices map[string][]simDevice `json:"devices"`
	}
	if ok, err := simctl(ctx, env, &list, "list", "-j", "devices"); !ok || err != nil {
		return nil, err
	}
	root := resolve(filepath.Join(env.Home, "Library", "Developer", "CoreSimulator", "Devices"))
	runtimes := make([]string, 0, len(list.Devices))
	for rt := range list.Devices {
		runtimes = append(runtimes, rt)
	}
	slices.Sort(runtimes)
	var out []finding.Finding
	for _, rt := range runtimes {
		for _, d := range list.Devices[rt] {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			command := []string{"xcrun", "simctl", "delete", d.UDID}
			if d.IsAvailable || !plan.AllowedCommand(command) {
				continue
			}
			f, ok, err := dirFinding(ctx, env, entry{
				path:    filepath.Join(root, d.UDID),
				tier:    finding.TierB,
				name:    fmt.Sprintf("simulator %s (%s)", d.Name, runtimeLabel(rt)),
				restore: "create it again in Xcode once its runtime is installed",
				warning: "its runtime is no longer installed, so it cannot boot; apps and data inside it are lost",
			})
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			f.Action = finding.ActionRunCommand
			f.Command = command
			out = append(out, f)
		}
	}
	return out, nil
}

// runtimeLabel turns "com.apple.CoreSimulator.SimRuntime.iOS-17-0" into "iOS 17.0".
func runtimeLabel(id string) string {
	name := id[strings.LastIndex(id, ".")+1:]
	platform, version, ok := strings.Cut(name, "-")
	if !ok {
		return name
	}
	return platform + " " + strings.ReplaceAll(version, "-", ".")
}

// simRuntime is one entry of "xcrun simctl runtime list -j".
type simRuntime struct {
	Identifier         string `json:"identifier"`
	Version            string `json:"version"`
	Build              string `json:"build"`
	PlatformIdentifier string `json:"platformIdentifier"`
	RuntimeIdentifier  string `json:"runtimeIdentifier"`
	Path               string `json:"path"`
	SizeBytes          int64  `json:"sizeBytes"`
	Deletable          bool   `json:"deletable"`
}

func scanSimulatorRuntimes(ctx context.Context, env *scan.Env) ([]finding.Finding, error) {
	if env.GOOS != "darwin" {
		return nil, nil
	}
	var list map[string]simRuntime
	if ok, err := simctl(ctx, env, &list, "runtime", "list", "-j"); !ok || err != nil {
		return nil, err
	}
	var out []finding.Finding
	for _, rt := range list {
		command := []string{"xcrun", "simctl", "runtime", "delete", rt.Identifier}
		if !rt.Deletable || rt.SizeBytes <= 0 || !plan.AllowedCommand(command) {
			continue
		}
		label := runtimeLabel(rt.RuntimeIdentifier)
		if rt.Version != "" && !strings.Contains(label, rt.Version) {
			label += " " + rt.Version
		}
		f := finding.Finding{
			Tier:      finding.TierB,
			Target:    plan.SimulatorRuntimeTarget(rt.Identifier),
			Name:      fmt.Sprintf("%s simulator runtime (%s)", label, rt.Build),
			Size:      rt.SizeBytes,
			Restore:   "downloaded again from Xcode > Settings > Components",
			Action:    finding.ActionRunCommand,
			Command:   command,
			NeedsSudo: true,
			Warning:   "simulators that use this runtime stop working until it is installed again",
		}
		if filepath.IsAbs(rt.Path) && filepath.Clean(rt.Path) == rt.Path {
			f.Path = rt.Path
		}
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b finding.Finding) int { return cmp.Compare(a.Target, b.Target) })
	return out, nil
}
