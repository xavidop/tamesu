// Package targettest is the conformance suite every conversational target
// must pass. A target's own tests build it against a scripted backend and
// call Run; the suite checks the Session contract the runner relies on.
package targettest

import (
	"context"
	"testing"
	"time"

	"github.com/xavidop/tamesu/target"
)

// Harness tells the suite how to build a target whose agent answers the
// given replies in order and ends the session with the last one.
type Harness struct {
	New func(t *testing.T, replies []string) target.Target
	// SkipDone skips the end-of-session check for transports that cannot
	// signal it.
	SkipDone bool
}

// Run runs the conformance suite.
func Run(t *testing.T, h Harness) {
	t.Helper()
	if h.New == nil {
		t.Fatal("targettest: Harness.New is nil")
	}
	ctx := context.Background()
	runCtx := func(t *testing.T) *target.RunContext {
		return &target.RunContext{TestName: t.Name(), Dir: t.TempDir(), Render: func(s string) (string, error) { return s, nil }}
	}

	t.Run("Name", func(t *testing.T) {
		if h.New(t, []string{"hi"}).Name() == "" {
			t.Error("Name() is empty")
		}
	})

	t.Run("TurnsInOrder", func(t *testing.T) {
		replies := []string{"first reply", "second reply", "third reply"}
		sess, err := h.New(t, replies).Start(ctx, runCtx(t))
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer sess.Close(ctx) //nolint:errcheck // the Close contract has its own case
		for i, want := range replies {
			sentAt := time.Now()
			if err := sess.Send(ctx, target.UserTurn{Text: "turn " + want, At: sentAt}); err != nil {
				t.Fatalf("Send %d: %v", i, err)
			}
			got, err := sess.Receive(ctx)
			if err != nil {
				t.Fatalf("Receive %d: %v", i, err)
			}
			if got.Text != want {
				t.Errorf("turn %d: got %q, want %q", i, got.Text, want)
			}
			if got.StartedAt.IsZero() || got.EndedAt.IsZero() {
				t.Errorf("turn %d: StartedAt and EndedAt must be set (transport timestamps)", i)
			}
			if got.EndedAt.Before(got.StartedAt) {
				t.Errorf("turn %d: EndedAt %v before StartedAt %v", i, got.EndedAt, got.StartedAt)
			}
			if got.EndedAt.Before(sentAt) {
				t.Errorf("turn %d: EndedAt %v before the turn was sent %v", i, got.EndedAt, sentAt)
			}
			if !got.FirstTokenAt.IsZero() && (got.FirstTokenAt.Before(got.StartedAt) || got.FirstTokenAt.After(got.EndedAt)) {
				t.Errorf("turn %d: FirstTokenAt %v outside [StartedAt, EndedAt]", i, got.FirstTokenAt)
			}
			if !h.SkipDone {
				if last := i == len(replies)-1; got.Done != last {
					t.Errorf("turn %d: Done = %v, want %v", i, got.Done, last)
				}
			}
		}
	})

	t.Run("ReceiveBeforeSend", func(t *testing.T) {
		sess, err := h.New(t, []string{"hi"}).Start(ctx, runCtx(t))
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer sess.Close(ctx) //nolint:errcheck // not under test here
		if _, err := sess.Receive(ctx); err == nil {
			t.Error("Receive before any Send must fail")
		}
	})

	t.Run("Cancellation", func(t *testing.T) {
		sess, err := h.New(t, []string{"hi"}).Start(ctx, runCtx(t))
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer sess.Close(ctx) //nolint:errcheck // not under test here
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if err := sess.Send(cctx, target.UserTurn{Text: "x", At: time.Now()}); err == nil {
			if _, err := sess.Receive(cctx); err == nil {
				t.Error("a cancelled context must fail Send or Receive")
			}
		}
	})

	t.Run("CloseIsIdempotent", func(t *testing.T) {
		sess, err := h.New(t, []string{"hi"}).Start(ctx, runCtx(t))
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		if err := sess.Send(ctx, target.UserTurn{Text: "x", At: time.Now()}); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if _, err := sess.Receive(ctx); err != nil {
			t.Fatalf("Receive: %v", err)
		}
		arts, err := sess.Close(ctx)
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
		if arts.Metrics == nil && arts.Files == nil && arts.Events == nil {
			t.Log("Close returned no artifacts; fine for a text transport")
		}
		if _, err := sess.Close(ctx); err != nil {
			t.Errorf("second Close must not fail: %v", err)
		}
	})
}
