package notify

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestDisabledNotifierDoesNothing(t *testing.T) {
	n := New(0, quiet())
	if n.enabled {
		t.Fatal("signal 0 should disable notification")
	}
	n.Changed() // must not panic or schedule anything
	n.Stop()
}

func TestNilNotifierIsSafe(t *testing.T) {
	// The daemon may run without one; calling through nil must be harmless.
	var n *Notifier
	n.Changed()
	n.Stop()
}

// A create touches state several times in a second; the bar needs telling once.
func TestChangedIsDebounced(t *testing.T) {
	n := New(8, quiet())
	for i := 0; i < 50; i++ {
		n.Changed()
	}

	n.mu.Lock()
	pending := n.pending
	n.mu.Unlock()
	if !pending {
		t.Fatal("no signal was scheduled")
	}

	// After the window a fresh burst schedules again, rather than the
	// notifier latching permanently.
	time.Sleep(debounce + 150*time.Millisecond)
	n.mu.Lock()
	stillPending := n.pending
	n.mu.Unlock()
	if stillPending {
		t.Fatal("the notifier stayed latched after firing")
	}

	n.Changed()
	n.mu.Lock()
	rescheduled := n.pending
	n.mu.Unlock()
	if !rescheduled {
		t.Fatal("a later change did not schedule a new signal")
	}
	n.Stop()
}

func TestStopCancelsAPendingSignal(t *testing.T) {
	n := New(8, quiet())
	n.Changed()
	n.Stop()
	// Nothing to assert beyond not panicking and not firing after shutdown;
	// the timer is cancelled.
}

// Signalling must never be fatal: no bar running is the normal case on a
// headless machine, and the daemon has to carry on regardless.
func TestSendWithNoWaybarRunningIsHarmless(t *testing.T) {
	n := New(8, quiet())
	n.send()
}

func TestFromEnvHonoursOverride(t *testing.T) {
	t.Setenv("DBFORGE_WAYBAR_SIGNAL", "3")
	if got := FromEnv(quiet()); got.signal != 3 {
		t.Fatalf("signal = %d, want 3", got.signal)
	}

	t.Setenv("DBFORGE_WAYBAR_SIGNAL", "0")
	if got := FromEnv(quiet()); got.enabled {
		t.Fatal("0 should disable signalling")
	}

	t.Setenv("DBFORGE_WAYBAR_SIGNAL", "nonsense")
	if got := FromEnv(quiet()); got.signal != DefaultSignal {
		t.Fatalf("garbage input gave signal %d, want the default %d", got.signal, DefaultSignal)
	}
}

// waybar computes SIGRTMIN+N with glibc's SIGRTMIN, which is 34 -- not the
// kernel's 32, whose first two are reserved by the threading implementation.
// Getting this wrong sends a signal that either does nothing or kills the bar.
func TestSigrtminMatchesGlibc(t *testing.T) {
	if sigrtmin != 34 {
		t.Fatalf("sigrtmin = %d, want glibc's 34", sigrtmin)
	}
}

func TestWaybarPIDsDoesNotError(t *testing.T) {
	// Reading /proc must tolerate processes exiting mid-scan.
	if _, err := waybarPIDs(); err != nil {
		t.Fatalf("waybarPIDs: %v", err)
	}
}
