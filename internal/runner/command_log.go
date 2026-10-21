package runner

import (
	"errors"
	"io"
)

type commandLogFile interface {
	io.WriterAt
	io.Closer
	Sync() error
	Truncate(int64) error
}

const (
	commandHead       = 1 << 20
	commandTail       = 256 << 10
	commandTruncation = "\n[output truncated]\n"
)

// commandLog is used by the single merged-output copier. Even after a disk
// error it keeps draining the pipe, retaining the error for RunCommand.
type commandLog struct {
	file      commandLogFile
	head      int
	tail      []byte
	totalTail int64
	err       error
}

func (l *commandLog) finish(receipt *Receipt) error {
	err := errors.Join(l.err, l.file.Sync(), l.file.Close())
	if err != nil {
		receipt.Uncertain = true
	}
	return err
}

func (l *commandLog) Write(p []byte) (int, error) {
	n := len(p)
	if l.err != nil {
		return n, nil
	}
	if l.head < commandHead {
		head := min(len(p), commandHead-l.head)
		_, l.err = l.file.WriteAt(p[:head], int64(l.head))
		l.head += head
		p = p[head:]
	}
	if len(p) > 0 && l.err == nil {
		l.totalTail += int64(len(p))
		if len(p) >= commandTail {
			l.tail = append(l.tail[:0], p[len(p)-commandTail:]...)
		} else {
			l.tail = append(l.tail, p...)
			if len(l.tail) > commandTail {
				copy(l.tail, l.tail[len(l.tail)-commandTail:])
				l.tail = l.tail[:commandTail]
			}
		}
		offset := int64(l.head)
		if l.totalTail > commandTail {
			_, l.err = l.file.WriteAt([]byte(commandTruncation), offset)
			offset += int64(len(commandTruncation))
		}
		if l.err == nil {
			var wrote int
			wrote, l.err = l.file.WriteAt(l.tail, offset)
			if wrote != len(l.tail) && l.err == nil {
				l.err = io.ErrShortWrite
			}
		}
		if l.err == nil {
			l.err = l.file.Truncate(offset + int64(len(l.tail)))
		}
	}
	return n, nil
}
