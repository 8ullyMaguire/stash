package subjects

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// R082 IS A REFUSAL GATE, SO IT MUST NOT BE ABLE TO WRITE. Enforced structurally
// rather than by convention, because a gate that can write is no longer a gate.
//
// `internal/api`'s write-path guard does not apply: it walks internal/api, and this
// package is not there. A judge is trusted with a verdict and nothing else, so the
// check is that the package's entire source declares no mutating operation at all.
//
// NOT A SUBSTRING SCAN FOR "write" -- a comment or a field name would trip it. This
// parses the AST and looks at CALLS, which is the only shape that actually mutates.
func TestThisPackageCannotWrite(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	require.NoError(t, err)
	require.NotEmpty(t, pkgs)

	// Mutating operations, by name. Everything here changes state somewhere.
	mutators := map[string]bool{
		"Write": true, "WriteString": true, "WriteFile": true, "Create": true,
		"CreateTemp": true, "Remove": true, "RemoveAll": true, "Rename": true,
		"Mkdir": true, "MkdirAll": true, "Truncate": true, "Chmod": true,
		"Chown": true, "Setenv": true, "Unsetenv": true, "Exec": true,
	}

	found := 0
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			rel, _ := filepath.Rel(".", path)
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					switch fun := call.Fun.(type) {
					case *ast.SelectorExpr:
						if mutators[fun.Sel.Name] {
							t.Errorf("%s:%d calls %s -- a consent gate may refuse, "+
								"and mutating anything means it is no longer a gate",
								rel, fset.Position(call.Pos()).Line, fun.Sel.Name)
							found++
						}
					case *ast.Ident:
						if mutators[fun.Name] {
							t.Errorf("%s:%d calls %s", rel,
								fset.Position(call.Pos()).Line, fun.Name)
							found++
						}
					}
					return true
				})
			}
		}
	}
	assert.Zero(t, found, "the package declares no mutating operation")

	// AND no database or filesystem package is imported, which is the same claim
	// stated about dependencies rather than about syntax.
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	for _, e := range entries {
		// NON-TEST FILES ONLY. The guard's own source contains the banned strings as
		// data, so including test files makes the guard fail on itself -- the first
		// version did exactly that. A gate that cannot test itself is not a gate.
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(e.Name())
		require.NoError(t, err)
		for _, banned := range []string{"database/sql", "gorm.io", "os.WriteFile",
			"io/ioutil", "net/http"} {
			assert.NotContains(t, string(src), `"`+banned+`"`,
				"%s must not import %s: a gate that can reach storage or the network "+
					"is a gate that can be made to lie", e.Name(), banned)
		}
	}
}
