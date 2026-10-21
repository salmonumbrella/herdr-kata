package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// The shared palette is the product boundary for production color choices.
func TestThemeColorLiterals(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || filepath.Base(path) == "theme.go" {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		aliases := map[string]bool{}
		for _, imp := range file.Imports {
			if imp.Path.Value == `"charm.land/lipgloss/v2"` {
				name := "lipgloss"
				if imp.Name != nil {
					name = imp.Name.Name
				}
				aliases[name] = true
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Color" {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if ok && aliases[id.Name] {
				t.Errorf("%s: production color outside theme", path)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
