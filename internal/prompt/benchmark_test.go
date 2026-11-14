package prompt

import "testing"

func BenchmarkNormalizeNote(b *testing.B) {
	for _, note := range []string{"Completed the task", "Completed\r\nthe task"} {
		b.Run(note, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if got := NormalizeNote(note); got != "Completed the task" {
					b.Fatal(got)
				}
			}
		})
	}
}
