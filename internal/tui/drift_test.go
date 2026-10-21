package tui

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestDriftParser(t *testing.T) {
	for _, s := range []string{`package x; func applyColorMode(){ titleStyle = base.Bold(true) }`, `package x; func applyColorMode(){ titleStyle = mysterious() }`} {
		if _, err := parsePalette(s); err == nil {
			t.Fatal("incomplete/unsupported palette accepted")
		}
	}
}
func TestKataPaletteDrift(t *testing.T) {
	preserveTheme(t)
	root := os.Getenv("HERDR_KATA_TEST_KATA_SRC")
	if root == "" {
		t.Skip("HERDR_KATA_TEST_KATA_SRC unset")
	}
	b, err := os.ReadFile(filepath.Join(root, "internal/tui/theme.go"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := parsePalette(string(b))
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []colorMode{colorLight, colorDark, colorNone} {
		applyColorMode(mode, false)
		for _, r := range paletteRoles() {
			name := r.name
			for _, override := range kataOverrides {
				if override.name == name {
					name = override.source
				}
			}
			expected := got[name][mode]
			// Apply the same documented product overrides to the source palette.
			style := lipgloss.NewStyle()
			if expected.FG != "" {
				style = style.Foreground(lipgloss.Color(expected.FG))
			}
			if expected.BG != "" {
				style = style.Background(lipgloss.Color(expected.BG))
			}
			style = style.Bold(expected.Bold).Faint(expected.Faint).Underline(expected.Underline).Reverse(expected.Reverse)
			for _, override := range kataOverrides {
				if override.name == r.name {
					style = override.apply(style, mode)
				}
			}
			s := styleValues(style)
			if s != styleValues(*r.style) {
				t.Errorf("%s mode %d: source %v copy %v", r.name, mode, s, styleValues(*r.style))
			}
		}
	}
}

type styleSpec struct {
	FG, BG                          string
	Bold, Faint, Underline, Reverse bool
}

func styleValues(s lipgloss.Style) styleSpec {
	return styleSpec{colorName(s.GetForeground()), colorName(s.GetBackground()), s.GetBold(), s.GetFaint(), s.GetUnderline(), s.GetReverse()}
}
func parsePalette(src string) (map[string]map[colorMode]styleSpec, error) {
	file, err := parser.ParseFile(token.NewFileSet(), "theme.go", src, 0)
	if err != nil {
		return nil, err
	}
	names := map[string]string{"titleStyle": "Title", "subtleStyle": "Subtle", "statusStyle": "Status", "chipStyle": "Chip", "selectedStyle": "Selected", "cursorRowStyle": "CursorRow", "openStyle": "Open", "closedStyle": "Closed", "deletedStyle": "Deleted", "helpKeyStyle": "HelpKey", "helpDescStyle": "HelpDesc", "errorStyle": "Error", "toastStyle": "Toast", "chipActive": "ChipActive", "tabActive": "TabActive", "tabInactive": "TabInactive", "detailMetaStyle": "DetailMeta", "detailSectionHeaderStyle": "DetailSectionHeader", "tableHeaderStyle": "TableHeader", "separatorRuleStyle": "SeparatorRule", "altRowStyle": "AltRow"}
	out := map[string]map[colorMode]styleSpec{}
	var parseErr error
	var scan func([]ast.Stmt, bool)
	scan = func(stmts []ast.Stmt, none bool) {
		for _, stmt := range stmts {
			if branch, ok := stmt.(*ast.IfStmt); ok {
				if cond, ok := branch.Cond.(*ast.BinaryExpr); ok {
					if id, ok := cond.Y.(*ast.Ident); ok && id.Name == "colorNone" {
						scan(branch.Body.List, true)
					}
				}
				continue
			}
			assignment, ok := stmt.(*ast.AssignStmt)
			if !ok || len(assignment.Lhs) != 1 || len(assignment.Rhs) != 1 {
				continue
			}
			id, ok := assignment.Lhs[0].(*ast.Ident)
			if !ok {
				continue
			}
			_, ok = names[id.Name]
			name := id.Name
			if !ok {
				continue
			}
			if out[name] == nil {
				out[name] = map[colorMode]styleSpec{}
			}
			modes := []colorMode{colorLight, colorDark}
			if none {
				modes = []colorMode{colorNone}
			}
			for _, mode := range modes {
				value, err := parseStyle(assignment.Rhs[0], mode)
				if err != nil {
					parseErr = fmt.Errorf("%s: %w", name, err)
					return
				}
				out[name][mode] = value
			}
		}
	}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "applyColorMode" {
			scan(fn.Body.List, false)
		}
	}
	if parseErr != nil {
		return nil, parseErr
	}
	for name := range names {
		if len(out[name]) != 3 {
			return nil, fmt.Errorf("missing palette assignment for %s", name)
		}
	}
	return out, nil
}
func parseStyle(expr ast.Expr, mode colorMode) (styleSpec, error) {
	if id, ok := expr.(*ast.Ident); ok && id.Name == "base" {
		return styleSpec{}, nil
	}
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return styleSpec{}, fmt.Errorf("unsupported style %T", expr)
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return styleSpec{}, fmt.Errorf("unsupported style call")
	}
	if id, ok := sel.X.(*ast.Ident); ok && id.Name == "lipgloss" && sel.Sel.Name == "NewStyle" && len(call.Args) == 0 {
		return styleSpec{}, nil
	}
	value, err := parseStyle(sel.X, mode)
	if err != nil {
		return value, err
	}
	switch sel.Sel.Name {
	case "Bold", "Faint", "Underline", "Reverse":
		if len(call.Args) != 1 {
			return value, fmt.Errorf("bad attribute")
		}
		id, ok := call.Args[0].(*ast.Ident)
		if !ok || id.Name != "true" {
			return value, fmt.Errorf("unknown attribute")
		}
		switch sel.Sel.Name {
		case "Bold":
			value.Bold = true
		case "Faint":
			value.Faint = true
		case "Underline":
			value.Underline = true
		case "Reverse":
			value.Reverse = true
		}
	case "Foreground", "Background":
		if len(call.Args) != 1 {
			return value, fmt.Errorf("bad color")
		}
		pick, ok := call.Args[0].(*ast.CallExpr)
		if !ok {
			return value, fmt.Errorf("unknown color")
		}
		id, ok := pick.Fun.(*ast.Ident)
		if !ok || id.Name != "pick" || len(pick.Args) != 2 {
			return value, fmt.Errorf("unknown color call")
		}
		index := 0
		if mode == colorDark {
			index = 1
		}
		literal, ok := pick.Args[index].(*ast.BasicLit)
		if !ok {
			return value, fmt.Errorf("nonliteral color")
		}
		color, err := strconv.Unquote(literal.Value)
		if err != nil {
			return value, err
		}
		if sel.Sel.Name == "Foreground" {
			value.FG = color
		} else {
			value.BG = color
		}
	default:
		return value, fmt.Errorf("unsupported method %s", sel.Sel.Name)
	}
	return value, nil
}
