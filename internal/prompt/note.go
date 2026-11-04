package prompt

import (
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"unicode/utf8"
)

// NormalizeNote preserves valid notes and bounds malformed optional note data.
func NormalizeNote(note string) string { return normalizeNote(note, false) }

func normalizeNote(note string, clipped bool) string {
	note = strings.ToValidUTF8(note, "�")
	note = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(note)
	if len(note) <= 280 && !clipped {
		return note
	}
	const marker = " [truncated]"
	end := min(len(note), 280-len(marker))
	for !utf8.ValidString(note[:end]) {
		end--
	}
	return note[:end] + marker
}

// ReadNote tolerates optional-file errors without weakening receipt/result reads.
func ReadNote(path string) string {
	const unreadable = "[unreadable note]"
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ENOENT) {
		// A dangling symlink is an unreadable note, rather than an absent note.
		if _, e := os.Lstat(path); os.IsNotExist(e) {
			return ""
		}
	}
	if err != nil {
		return unreadable
	}
	f := os.NewFile(uintptr(fd), path)
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return unreadable
	}
	b, err := io.ReadAll(io.LimitReader(f, 283))
	if err != nil {
		return unreadable
	}
	clipped := len(b) == 283
	note := string(b)
	if !clipped {
		note = strings.TrimSuffix(strings.TrimSuffix(note, "\n"), "\r")
	}
	return normalizeNote(note, clipped)
}
