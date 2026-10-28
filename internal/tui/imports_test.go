package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// screenImports is everything the screens may import: the standard library
// packages that touch nothing outside the process, the terminal libraries,
// and the reclaim packages for findings, plans, scan events, sizes, the
// findings tree and the protected list. os, os/exec, syscall, cgo, the
// Docker client and reclaim's own executors are not on it.
func screenImports() []string {
	return []string{
		"context", "errors", "fmt", "io", "path/filepath", "slices", "strconv", "strings", "time",
		"github.com/charmbracelet/bubbles/spinner", "github.com/charmbracelet/bubbles/textinput", "github.com/charmbracelet/bubbles/viewport",
		"github.com/charmbracelet/bubbletea", "github.com/charmbracelet/lipgloss",
		"github.com/mandloideep/reclaim/internal/apply", "github.com/mandloideep/reclaim/internal/finding",
		"github.com/mandloideep/reclaim/internal/plan", "github.com/mandloideep/reclaim/internal/scan",
		"github.com/mandloideep/reclaim/internal/ui", "github.com/mandloideep/reclaim/internal/units",
	}
}

// testImports are the further packages the tests use, which also leave the
// disk alone.
func testImports() []string {
	return []string{"go/ast", "go/parser", "go/token", "reflect", "sync", "testing", "github.com/stretchr/testify/require"}
}

// TestScreensCannotRemoveAnything proves from the source that the screens
// have no way to remove anything themselves: every file of the package
// imports only what screenImports allows, which leaves out os, os/exec,
// syscall and cgo, the only names they use from apply are the types of the
// protected list, the only names they use from plan are the plan types and
// the conversion of a finding into an action for the details pane, and the
// Backend interface has no method that removes anything. The tests import
// neither os nor os/exec either, so no test can create on disk what it then
// claims a screen removed.
func TestScreensCannotRemoveAnything(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	// No sub-package can hide code the package imports under another name.
	nested, err := filepath.Glob("*/*.go")
	require.NoError(t, err)
	require.Empty(t, nested, "internal/tui has no sub-packages")
	allowed := map[string][]string{
		"apply": {"ProtectedEntry", "Progress", "Summary", "Outcome", "Check", "Status",
			"StatusDone", "StatusGone", "StatusManual", "StatusFailed", "StatusNotRun", "StatusChecked", "StatusRefused"},
		"plan": {"Plan", "Action", "FromFinding"},
	}
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)
		test := strings.HasSuffix(name, "_test.go")
		if !test {
			checked++
		}
		imports := map[string]string{}
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			require.NoError(t, err)
			allowedImports := screenImports()
			if test {
				allowedImports = append(allowedImports, testImports()...)
			}
			require.Contains(t, allowedImports, path, "%s imports %s, which is not on the list of what the screens may use", name, path)
			local := filepath.Base(path)
			if imp.Name != nil {
				require.NotContains(t, []string{".", "_"}, imp.Name.Name, "%s imports %s with %s", name, path, imp.Name.Name)
				local = imp.Name.Name
			}
			imports[local] = filepath.Base(path)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if names, watched := allowed[imports[pkg.Name]]; watched && !test {
				require.True(t, slices.Contains(names, sel.Sel.Name), "%s uses %s.%s, which the screens must not reach",
					fset.Position(sel.Pos()), pkg.Name, sel.Sel.Name)
			}
			return true
		})
	}
	require.Positive(t, checked)

	// The backend scans, renders the protected list, saves a plan, reads
	// the free space and applies a plan. The only method that removes
	// anything is Apply, and the only thing it takes besides the context,
	// the dry run flag and the progress callbacks is the path of a plan.
	backend := reflect.TypeFor[Backend]()
	methods := make([]string, 0, backend.NumMethod())
	for i := range backend.NumMethod() {
		methods = append(methods, backend.Method(i).Name)
	}
	require.Equal(t, []string{"Apply", "FreeSpace", "Protected", "SavePlan", "Scan"}, methods)
	applyMethod, _ := backend.MethodByName("Apply")
	in := make([]string, 0, applyMethod.Type.NumIn())
	for i := range applyMethod.Type.NumIn() {
		in = append(in, applyMethod.Type.In(i).String())
	}
	require.Equal(t, []string{"context.Context", "string", "bool", "apply.Progress"}, in)
}
