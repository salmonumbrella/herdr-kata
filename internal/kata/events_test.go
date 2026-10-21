package kata

import (
	"context"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func tailCLI(t *testing.T, output string, hold bool, stderr string) (*CLI, <-chan struct{}) {
	t.Helper()
	reaped := make(chan struct{})
	c := fakeCLI(t, "", nil)
	c.starter = func(ctx context.Context, argv, _ []string, dir string) (Proc, error) {
		want := []string{"events", "--tail", "--last-event-id", "7"}
		if !reflect.DeepEqual(argv[len(argv)-4:], want) || dir != "/state" {
			t.Fatal(argv, dir)
		}
		if _, ok := ctx.Deadline(); ok {
			t.Fatal("stream has finite deadline")
		}
		outR, outW := io.Pipe()
		errR, errW := io.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer outW.Close()
			defer errW.Close()
			_, _ = io.WriteString(errW, stderr)
			_, _ = io.WriteString(outW, output)
			if hold {
				<-ctx.Done()
			}
		}()
		go func() {
			select {
			case <-ctx.Done():
				_ = outW.CloseWithError(ctx.Err())
				_ = errW.CloseWithError(ctx.Err())
			case <-done:
			}
		}()
		return Proc{Stdout: outR, Stderr: errR, Wait: func() error { <-done; close(reaped); return nil }}, nil
	}
	return c, reaped
}

func TestTailDeliversBeforeExit(t *testing.T) {
	c, reaped := tailCLI(t, `{"event_id":8,"type":"cron.job.created","issue_uid":null,"payload":{"uid":"j"},"future":true}`+"\n", true, "")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	items, errs := c.Tail(ctx, 7)
	select {
	case item := <-items:
		e := item.Event
		if e.EventID != 8 || e.Type != "cron.job.created" || string(e.Payload) != `{"uid":"j"}` {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("buffered until exit")
	}
	select {
	case <-reaped:
		t.Fatal("child exited early")
	default:
	}
	cancel()
	for range errs {
	}
	<-reaped
}

func TestTailParsesReset(t *testing.T) {
	c, _ := tailCLI(t, "{\"reset_required\":true,\"reset_after_id\":42}\n", false, "")
	items, errs := c.Tail(t.Context(), 7)
	item := <-items
	if item.Reset == nil || item.Reset.AfterID != 42 {
		t.Fatal(item)
	}
	for err := range errs {
		t.Fatal(err)
	}
}

func TestTailCancelReaps(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	c := fakeCLI(t, "", nil)
	reaped := make(chan struct{})
	c.starter = func(ctx context.Context, _, _ []string, _ string) (Proc, error) {
		p, err := execStarter(ctx, []string{os.Args[0], "-test.run=TestKataTailChild"}, append(os.Environ(), "KATA_TAIL_CHILD=1"), t.TempDir())
		if err != nil {
			return p, err
		}
		wait := p.Wait
		p.Wait = func() error { err := wait(); close(reaped); return err }
		return p, nil
	}
	items, errs := c.Tail(ctx, 7)
	select {
	case <-items:
	case <-time.After(3 * time.Second):
		t.Fatal("child did not start")
	}
	cancel()
	select {
	case <-reaped:
	case <-time.After(3 * time.Second):
		t.Fatal("child not reaped")
	}
	for range errs {
	}
}

func TestKataTailChild(t *testing.T) {
	if os.Getenv("KATA_TAIL_CHILD") != "1" {
		return
	}
	fmt.Println(`{"event_id":8,"type":"issue.created"}`)
	select {}
}

func TestTailOversizedLine(t *testing.T) {
	for _, extra := range []int{0, 1} {
		prefix, suffix := `{"event_id":8,"type":"issue.updated","payload":{"text":"`, `"}}`
		line := prefix + strings.Repeat("x", maxEventLine-len(prefix)-len(suffix)+extra) + suffix
		c, reaped := tailCLI(t, line+"\n", false, "")
		items, errs := c.Tail(t.Context(), 7)
		err := <-errs
		for range errs {
		}
		<-reaped
		item, ok := <-items
		e := item.Event
		if extra == 0 && (err != nil || !ok || e.EventID != 8) {
			t.Fatal("1 MiB line rejected", err)
		}
		if extra == 1 && (err == nil || ok) {
			t.Fatal("oversized line accepted")
		}
	}
}

func TestTailStderrSaturation(t *testing.T) {
	c, _ := tailCLI(t, "malformed\n", false, strings.Repeat("x", 10<<20)+"stderr tail marker")
	_, errs := c.Tail(t.Context(), 7)
	select {
	case err := <-errs:
		if err == nil || !strings.Contains(err.Error(), "stderr tail marker") || len(err.Error()) > (64<<10)+1024 {
			t.Fatal("stderr not bounded or drained")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stderr deadlock")
	}
}

// Definition snapshots must not race ahead of creating the tail process.
func TestTailStartsBeforeReturn(t *testing.T) {
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	c := New(config.Kata{Bin: "/bin/kata", Project: "test", Actor: "worker"}, t.TempDir(), nil, func(ctx context.Context, _, _ []string, _ string) (Proc, error) {
		close(entered)
		<-release
		return Proc{Stdout: strings.NewReader(""), Stderr: strings.NewReader(""), Wait: func() error { return nil }}, nil
	})
	go func() { c.Tail(t.Context(), 0); close(returned) }()
	<-entered
	select {
	case <-returned:
		close(release)
		t.Fatal("Tail returned before process start completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("Tail did not return after start")
	}
}
