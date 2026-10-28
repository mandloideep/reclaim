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

// TestScreensCannotRemoveAnything proves from the source that the screens
// have no way to remove anything themselves: no file of the package imports
// os or os/exec, the only names they use from apply are the types of the
// protected list, the only names they use from plan are the plan types and
// the conversion of a finding into an action for the details pane, and the
// Backend interface has no method that removes anything. The tests import
// neither os nor os/exec either, so no test can create on disk what it then
// claims a screen removed.
func TestScreensCannotRemoveAnything(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	allowed := map[string][]string{
		"apply": {"ProtectedEntry", "ProtectedSource", "ProtectedSystem", "ProtectedHome", "ProtectedRule", "ProtectedRoot", "ProtectedExclude"},
		"plan":  {"Plan", "FromFinding"},
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
			require.NotContains(t, []string{"os", "os/exec", "syscall", "io/fs", "github.com/mandloideep/reclaim/internal/execx",
				"github.com/mandloideep/reclaim/internal/fsx", "github.com/mandloideep/reclaim/internal/dockerx"}, path,
				"%s imports %s", name, path)
			local := filepath.Base(path)
			if imp.Name != nil {
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

	// The backend scans, renders the protected list and saves a plan;
	// none of that removes anything.
	backend := reflect.TypeFor[Backend]()
	methods := make([]string, 0, backend.NumMethod())
	for i := range backend.NumMethod() {
		methods = append(methods, backend.Method(i).Name)
	}
	require.Equal(t, []string{"Protected", "SavePlan", "Scan"}, methods)
}
