package kata

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
)

const maxEventLine = 1 << 20

func (c *CLI) Tail(ctx context.Context, afterID int64) (<-chan TailItem, <-chan error) {
	items, errs := make(chan TailItem, 16), make(chan error, 1)
	started := make(chan struct{})
	go func() {
		var err error
		defer close(errs)
		defer func() {
			close(items)
			if err != nil {
				errs <- err
			}
		}()
		childCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		env, err := c.environment()
		if err != nil {
			close(started)
			return
		}
		proc, err := c.starter(childCtx, c.argv("events", "--tail", "--last-event-id", strconv.FormatInt(afterID, 10)), env, c.stateDir)
		close(started)
		if err != nil {
			return
		}
		stderr := &tailWriter{limit: 64 << 10}
		drained := make(chan error, 1)
		go func() { _, err := io.Copy(stderr, proc.Stderr); drained <- err }()
		scanner := bufio.NewScanner(proc.Stdout)
		scanner.Buffer(make([]byte, 64<<10), maxEventLine+2)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) > maxEventLine {
				err = fmt.Errorf("kata event line exceeds %d bytes", maxEventLine)
				break
			}
			var wire struct {
				Event
				ResetRequired bool  `json:"reset_required"`
				ResetAfterID  int64 `json:"reset_after_id"`
			}
			if err = json.Unmarshal(line, &wire); err != nil {
				break
			}
			item := TailItem{Event: wire.Event}
			if wire.ResetRequired {
				item.Reset = &Reset{AfterID: wire.ResetAfterID}
			}
			select {
			case items <- item:
			case <-childCtx.Done():
			}
			if childCtx.Err() != nil {
				break
			}
		}
		if err == nil {
			err = scanner.Err()
		}
		// Stop and drain before Wait: Wait closes exec pipes, and waiting first
		// would lose the final stderr bytes or deadlock behind a full pipe.
		cancel()
		drainErr := <-drained
		waitErr := proc.Wait()
		if ctx.Err() != nil {
			err = ctx.Err()
			return
		}
		if err == nil {
			err = drainErr
		}
		if err == nil {
			err = waitErr
		}
		if err != nil {
			err = fmt.Errorf("kata events: %w; stderr: %s", err, stderr.data)
		}
	}()
	// Snapshot readers can safely start only after the child has been started.
	<-started
	return items, errs
}

type tailWriter struct {
	data  []byte
	limit int
}

func (w *tailWriter) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) >= w.limit {
		w.data = append(w.data[:0], p[len(p)-w.limit:]...)
	} else {
		if drop := len(w.data) + len(p) - w.limit; drop > 0 {
			copy(w.data, w.data[drop:])
			w.data = w.data[:len(w.data)-drop]
		}
		w.data = append(w.data, p...)
	}
	return n, nil
}
