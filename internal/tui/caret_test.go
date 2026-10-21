package tui

import (
	"reflect"
	"strings"
	"testing"
)

func TestCaretVisualLines(t *testing.T) {
	for _, tc := range []struct {
		text             string
		cols, from, want int
	}{
		{"abcdefghi", 4, 2, 6}, {"ab\ncd\nef", 8, 1, 4}, {"界ab\n界cd", 8, 1, 5}, {"e\u0301ab\ncd", 8, 2, 6},
	} {
		if got := verticalCaret(tc.text, tc.cols, tc.from, 1, -1); got != tc.want {
			t.Fatalf("%q from %d got %d want %d", tc.text, tc.from, got, tc.want)
		}
	}
	m := &jobForm{buffer: "abc\ndef\nghi", caret: 5, width: 20}
	m.moveCaret("home")
	if m.caret != 4 {
		t.Fatal("home", m.caret)
	}
	m.moveCaret("end")
	if m.caret != 7 {
		t.Fatal("end", m.caret)
	}
	m.moveCaret("ctrl+home")
	if m.caret != 0 {
		t.Fatal("whole home")
	}
	m.moveCaret("ctrl+end")
	if m.caret != 11 {
		t.Fatal("whole end")
	}
}

func TestCaretPreferredColumn(t *testing.T) {
	m := &jobForm{buffer: "abcdef\nx\nabcdef", caret: 5, width: 20, preferredCol: -1}
	m.moveCaret("down")
	if m.caret != 8 {
		t.Fatal(m.caret)
	}
	m.moveCaret("down")
	if m.caret != 14 {
		t.Fatal("preferred column lost", m.caret)
	}
}

func TestCaretRenderAgreement(t *testing.T) {
	for _, text := range []string{"abcd\nxy", "界ábcd", "abcd́x"} {
		layout := layoutText(text, 4)
		for caret, position := range layout.positions {
			_, row := layout.withCaret(caret, 4)
			if row != position.row {
				t.Fatalf("%q caret %d draws row %d but motion uses row %d", text, caret, row, position.row)
			}
		}
	}
}

func TestCaretFullWidthHardLine(t *testing.T) {
	layout := layoutText("abcd\nxy", 4)
	if !reflect.DeepEqual(layout.lines, []string{"abcd", "xy"}) {
		t.Fatal("hard newline added a phantom display line", layout.lines)
	}
	m := &jobForm{width: 80, buffer: strings.Repeat("x", 74) + "\nyz", caret: 76, preferredCol: -1}
	m.moveCaret("up")
	if m.caret != 1 {
		t.Fatal("up stopped on a phantom row", m.caret)
	}
	m.moveCaret("down")
	if m.caret != 76 {
		t.Fatal("down lost the visual column", m.caret)
	}
}
