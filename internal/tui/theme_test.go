package tui

import (
	"fmt"
	"image/color"
	"reflect"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestResolve(t *testing.T) {
	for _, c := range []struct {
		no, mode string
		want     colorMode
	}{{"", "", colorAuto}, {"", "LIGHT", colorLight}, {"", "dark", colorDark}, {"", "none", colorNone}, {"1", "dark", colorNone}, {"", " dark", colorAuto}, {"", "unexpected", colorAuto}} {
		got := resolveColorMode(func(k string) string {
			if k == "NO_COLOR" {
				return c.no
			}
			if k == "KATA_COLOR_MODE" {
				return c.mode
			}
			t.Fatalf("unexpected key %s", k)
			return ""
		})
		if got != c.want {
			t.Errorf("%q/%q = %v, want %v", c.no, c.mode, got, c.want)
		}
	}
}

type paletteRole struct {
	name                                            string
	style                                           *lipgloss.Style
	light, dark, bgl, bgd                           string
	bold, faint, underline, reverse                 bool
	noneBold, noneFaint, noneUnderline, noneReverse bool
}

func paletteRoles() []paletteRole {
	return []paletteRole{
		{"headerStyle", &headerStyle, "242", "246", "", "", true, false, false, false, true, false, false, false},
		{"titleStyle", &titleStyle, "", "", "", "", true, false, false, false, true, false, false, false},
		{"subtleStyle", &subtleStyle, "242", "246", "", "", false, false, false, false, false, false, false, false},
		{"statusStyle", &statusStyle, "242", "246", "", "", false, false, false, false, false, false, false, false},
		{"chipStyle", &chipStyle, "242", "246", "", "", false, false, false, false, false, false, false, false},
		{"selectedStyle", &selectedStyle, "", "", "153", "24", false, false, false, false, false, false, false, true},
		{"cursorRowStyle", &cursorRowStyle, "", "", "153", "24", false, false, false, false, false, false, false, true},
		{"openStyle", &openStyle, "28", "46", "", "", false, false, false, false, false, false, false, false},
		{"closedStyle", &closedStyle, "240", "245", "", "", false, false, false, false, false, false, false, false},
		{"deletedStyle", &deletedStyle, "124", "196", "", "", false, true, false, false, false, true, false, false},
		{"helpKeyStyle", &helpKeyStyle, "242", "246", "", "", false, false, false, false, true, false, false, false},
		{"helpDescStyle", &helpDescStyle, "248", "240", "", "", false, false, false, false, false, false, false, false},
		{"errorStyle", &errorStyle, "124", "196", "", "", true, false, false, false, true, false, false, false},
		{"toastStyle", &toastStyle, "28", "46", "", "", true, false, false, false, true, false, false, false},
		{"chipActive", &chipActive, "125", "205", "", "", true, false, false, false, true, false, false, false},
		{"tabActive", &tabActive, "125", "205", "", "", true, false, false, false, true, false, false, false},
		{"tabInactive", &tabInactive, "242", "246", "", "", false, false, false, false, false, true, false, false},
		{"dimStyle", &dimStyle, "240", "240", "", "", false, false, false, false, false, false, false, false},
		{"detailMetaStyle", &detailMetaStyle, "", "", "", "", false, false, false, false, false, false, false, false},
		{"detailSectionHeaderStyle", &detailSectionHeaderStyle, "", "", "", "", true, false, false, false, true, false, false, false},
		{"tableHeaderStyle", &tableHeaderStyle, "242", "246", "253", "234", true, false, false, false, true, false, false, false},
		{"separatorRuleStyle", &separatorRuleStyle, "248", "242", "", "", false, true, false, false, false, true, false, false},
		{"altRowStyle", &altRowStyle, "", "", "254", "236", false, false, false, false, false, false, false, false},
	}
}
func colorName(c color.Color) string {
	if _, ok := c.(lipgloss.NoColor); ok {
		return ""
	}
	return fmt.Sprint(c)
}
func TestPalette(t *testing.T) {
	preserveTheme(t)
	for _, mode := range []colorMode{colorLight, colorDark, colorNone, colorAuto} {
		for _, dark := range []bool{false, true} {
			applyColorMode(mode, dark)
			for _, r := range paletteRoles() {
				fg, bg := r.light, r.bgl
				if mode == colorDark || mode == colorAuto && dark {
					fg, bg = r.dark, r.bgd
				}
				b, f, u, v := r.bold, r.faint, r.underline, r.reverse
				if mode == colorNone {
					fg, bg = "", ""
					b, f, u, v = r.noneBold, r.noneFaint, r.noneUnderline, r.noneReverse
				}
				s := *r.style
				if colorName(s.GetForeground()) != fg || colorName(s.GetBackground()) != bg || s.GetBold() != b || s.GetFaint() != f || s.GetUnderline() != u || s.GetReverse() != v {
					t.Errorf("mode %d/%v role %s = fg %v bg %v attributes %v/%v/%v/%v", mode, dark, r.name, s.GetForeground(), s.GetBackground(), s.GetBold(), s.GetFaint(), s.GetUnderline(), s.GetReverse())
				}
			}
			active, inactive := "125", "242"
			if mode == colorDark || mode == colorAuto && dark {
				active, inactive = "205", "246"
			}
			if mode == colorNone {
				active, inactive = "", ""
			}
			if colorName(panelActiveBorder) != active || colorName(panelInactiveBorder) != inactive {
				t.Errorf("borders mode %d", mode)
			}
		}
	}
}
func TestAliases(t *testing.T) {
	preserveTheme(t)
	for _, mode := range []colorMode{colorLight, colorDark, colorNone} {
		applyColorMode(mode, false)
		for _, p := range [][2]lipgloss.Style{{passStyle, openStyle}, {failStyle, errorStyle}, {attentionStyle, chipActive}, {neutralStyle, closedStyle}, {cursorStyle, detailMetaStyle}, {rowSelectedStyle, cursorRowStyle}, {itemSelectedStyle, selectedStyle}, {favoriteStyle, titleStyle}, {headerStyle, tableHeaderStyle.UnsetBackground()}} {
			if !reflect.DeepEqual(p[0], p[1]) {
				t.Errorf("mode %d aliases differ", mode)
			}
		}
	}
}
