//go:build !crashpoint

package runner

func journalSnapshot(*Run) []byte      { return nil }
func journalPoint([]byte, *Run) string { return "" }
