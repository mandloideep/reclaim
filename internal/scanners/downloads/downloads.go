// Package downloads holds the Downloads folder scanners: installers whose
// application is already installed, archives that were extracted next to
// themselves, and large old files listed for attention only.
//
// The scanners look at the files directly inside ~/Downloads. They never
// descend into folders there, which hold whatever the user put in them.
package downloads

import (
	"archive/zip"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/mandloideep/reclaim/internal/finding"
	"github.com/mandloideep/reclaim/internal/fsx"
	"github.com/mandloideep/reclaim/internal/scan"
	"github.com/mandloideep/reclaim/internal/units"
)

const (
	// attentionSize is the smallest file the attention list shows.
	attentionSize = 250_000_000
	// attentionAge is how long a file must have gone untouched, neither
	// modified nor read, to be listed for attention.
	attentionAge = 90 * 24 * time.Hour
)

// kind is what the classification decided about one file.
type kind int

const (
	kindNone kind = iota
	kindInstaller
	kindArchive
	kindAttention
)

// spec describes one Downloads scanner.
type spec struct {
	name        string
	kind        kind
	description string
}

// Scanner reports one kind of Downloads finding.
type Scanner struct {
	s spec
}

var _ scan.Scanner = (*Scanner)(nil)

// Name implements scan.Scanner.
func (s *Scanner) Name() string { return s.s.name }

// Category implements scan.Scanner.
func (s *Scanner) Category() finding.Category { return finding.CategoryDownloads }

// Ecosystem implements scan.Scanner.
func (s *Scanner) Ecosystem() string { return "downloads" }

// Description implements scan.Scanner.
func (s *Scanner) Description() string { return s.s.description }

// Scanners returns every Downloads scanner.
func Scanners() []*Scanner {
	specs := []spec{
		{name: "installers", kind: kindInstaller,
			description: "installers (.dmg, .pkg, .iso, .zip, .app.tar.*) in ~/Downloads whose app is installed"},
		{name: "extracted-archives", kind: kindArchive,
			description: "archives in ~/Downloads with an extracted folder of the same name next to them"},
		{name: "old-downloads", kind: kindAttention,
			description: "files in ~/Downloads over 250 MB untouched for 90 days, listed for attention and never removed"},
	}
	out := make([]*Scanner, len(specs))
	for i, s := range specs {
		out[i] = &Scanner{s: s}
	}
	return out
}

// Scan implements scan.Scanner.
func (s *Scanner) Scan(ctx context.Context, env scan.Env) ([]finding.Finding, error) {
	dir := filepath.Join(env.Home, "Downloads")
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	now := env.Now
	if now.IsZero() {
		now = time.Now()
	}
	// The archive rule wins over the others and needs no app list; the
	// attention list needs it to leave out installers already reported.
	var apps []installedApp
	if s.s.kind != kindArchive {
		apps = installedApps(&env)
	}
	dirs := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			dirs[e.Name()] = true
		}
	}
	var out []finding.Finding
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f := finding.Finding{
			Path:     path,
			Kind:     finding.KindFile,
			Target:   path,
			Size:     info.Size(),
			LastUsed: lastTouched(info),
			Action:   finding.ActionRemovePath,
		}
		switch classify(dir, e.Name(), dirs, apps, f.Size, now.Sub(f.LastUsed)) {
		case kindArchive:
			if s.s.kind != kindArchive {
				continue
			}
			f.Tier = finding.TierC
			f.Warning = "the folder " + archiveStem(e.Name()) + " next to it looks like its extracted copy; keep the archive if you need the original"
			f.Restore = "download it again"
		case kindInstaller:
			if s.s.kind != kindInstaller {
				continue
			}
			app, _ := installerApp(dir, e.Name(), apps)
			f.Tier = finding.TierB
			f.Restore = "download it again from the vendor; " + app.name + " is installed"
		case kindAttention:
			if s.s.kind != kindAttention {
				continue
			}
			f.Tier = finding.TierC
			f.Action = finding.ActionNone
			f.Warning = "not touched for " + units.FormatAge(now.Sub(f.LastUsed)) + "; listed for you to review, reclaim never removes it"
		default:
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

// classify decides what one file in Downloads is. An archive with an
// extracted sibling wins over an installer, because the archive may be the
// only untouched copy of what was extracted.
func classify(dir, name string, dirs map[string]bool, apps []installedApp, size int64, age time.Duration) kind {
	if stem := archiveStem(name); stem != "" && dirs[stem] {
		return kindArchive
	}
	if _, ok := installerApp(dir, name, apps); ok {
		return kindInstaller
	}
	if size >= attentionSize && age >= attentionAge {
		return kindAttention
	}
	return kindNone
}

// lastTouched is the later of the modification and access times.
func lastTouched(info fs.FileInfo) time.Time {
	t := info.ModTime()
	if at, ok := fsx.AccessTime(info); ok && at.After(t) {
		t = at
	}
	return t
}

// archiveSuffixes are the archive extensions, longest first.
func archiveSuffixes() []string {
	return []string{".tar.gz", ".tar.bz2", ".tar.xz", ".tar.zst", ".tgz", ".tbz2", ".txz", ".tar", ".zip", ".7z", ".rar"}
}

// archiveStem returns the name of an archive without its extension, the name
// an extracted folder would get, or an empty string for other files.
func archiveStem(name string) string {
	lower := strings.ToLower(name)
	for _, ext := range archiveSuffixes() {
		if strings.HasSuffix(lower, ext) && len(name) > len(ext) {
			return name[:len(name)-len(ext)]
		}
	}
	return ""
}

// installerStem returns the name of an installer without its extension, or
// an empty string for other files.
func installerStem(name string) string {
	lower := strings.ToLower(name)
	if i := strings.Index(lower, ".app.tar."); i > 0 {
		return name[:i]
	}
	for _, ext := range []string{".dmg", ".pkg", ".iso", ".zip"} {
		if strings.HasSuffix(lower, ext) && len(name) > len(ext) {
			return strings.TrimSuffix(name[:len(name)-len(ext)], ".app")
		}
	}
	return ""
}

// installedApp is an application bundle found in an applications folder.
type installedApp struct {
	name   string // such as "Google Chrome.app"
	joined string // its name as lower case letters and digits, "googlechrome"
}

// installedApps lists the .app bundles in the applications folders and in
// the folders directly inside them, such as "/Applications/DaVinci Resolve".
func installedApps(env *scan.Env) []installedApp {
	apps := []installedApp{}
	add := func(name string) {
		joined := strings.Join(words(strings.TrimSuffix(name, ".app")), "")
		if len(joined) >= 3 {
			apps = append(apps, installedApp{name: name, joined: joined})
		}
	}
	for _, dir := range env.Applications {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if strings.HasSuffix(e.Name(), ".app") {
				add(e.Name())
				continue
			}
			inner, err := os.ReadDir(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			for _, ie := range inner {
				if ie.IsDir() && strings.HasSuffix(ie.Name(), ".app") {
					add(ie.Name())
				}
			}
		}
	}
	return apps
}

// installerApp reports whether a file in dir is an installer of an installed
// app, and which app. Its name must start with the app's name, and every
// word after it must look like a version or a platform, as in
// "Slack-4.41.105-macOS.dmg"; "Notion Export.zip" or "Signal backup.dmg"
// may be the only copy of data and are not installers. A zip must also hold
// an app, a disk image or a package at its top level, which is read from its
// central directory without extracting anything.
func installerApp(dir, name string, apps []installedApp) (installedApp, bool) {
	stem := installerStem(name)
	if stem == "" || len(apps) == 0 {
		return installedApp{}, false
	}
	app, rest, ok := matchApp(stem, apps)
	if !ok || slices.ContainsFunc(rest, func(w string) bool { return !installerWord(w) }) {
		return installedApp{}, false
	}
	if strings.HasSuffix(strings.ToLower(name), ".zip") && !zipHoldsInstaller(filepath.Join(dir, name)) {
		return installedApp{}, false
	}
	return app, true
}

// installerWord reports whether a word of an installer name after the app
// name is a version number or names a platform or a release channel.
func installerWord(w string) bool {
	if strings.ContainsFunc(w, unicode.IsDigit) {
		return true
	}
	return slices.Contains([]string{
		"mac", "macos", "osx", "darwin", "universal", "arm", "aarch", "x64", "amd64", "intel", "apple", "silicon",
		"installer", "install", "setup", "release", "stable", "latest", "beta", "alpha", "app", "v", "full", "dmg", "pkg",
	}, w)
}

// zipHoldsInstaller reports whether a zip archive has an app bundle, a disk
// image or a package at its top level.
func zipHoldsInstaller(path string) bool {
	r, err := zip.OpenReader(path)
	if err != nil {
		return false
	}
	defer func() { _ = r.Close() }()
	for _, f := range r.File {
		top, _, _ := strings.Cut(strings.TrimPrefix(f.Name, "./"), "/")
		lower := strings.ToLower(top)
		if strings.HasSuffix(lower, ".app") || strings.HasSuffix(lower, ".dmg") || strings.HasSuffix(lower, ".pkg") {
			return true
		}
	}
	return false
}

// matchApp finds the app an installer belongs to. The installer name must
// start with the app name on a word boundary, ignoring case, spaces and
// punctuation: "DaVinci_Resolve_21.1_Mac" belongs to "DaVinci Resolve.app",
// "zen.macos-universal" to "Zen.app", but "Zenith" does not belong to Zen.
// The longest matching app name wins. rest holds the words after the app name.
func matchApp(stem string, apps []installedApp) (app installedApp, rest []string, ok bool) {
	// The stem's word prefixes joined without separators: "davinci",
	// "davinciresolve", "davinciresolve21" and so on.
	ws := words(stem)
	prefixes := make([]string, 0, len(ws))
	var b strings.Builder
	for _, w := range ws {
		b.WriteString(w)
		prefixes = append(prefixes, b.String())
	}
	used := 0
	for _, a := range apps {
		if i := slices.Index(prefixes, a.joined); i >= 0 && (!ok || len(a.joined) > len(app.joined)) {
			app, used, ok = a, i+1, true
		}
	}
	if !ok {
		return installedApp{}, nil, false
	}
	return app, ws[used:], true
}

// words splits s into lower case runs of letters and digits.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}
