// Package notify tells the status bar that instance state changed.
//
// The plan prefers signal-driven updates over polling, to avoid waking the CPU
// on a laptop for a value that changes a few times a day (spec 7, phase 4).
// Waybar refreshes a custom module when it receives SIGRTMIN+N, so the daemon
// sends that signal whenever it changes something.
package notify

import (
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// DefaultSignal is the SIGRTMIN offset the waybar module listens on. Omarchy
// already uses 7 for its update indicator, so 8 is the first free one.
const DefaultSignal = 8

// sigrtmin is glibc's SIGRTMIN, which is 34 rather than the kernel's 32: glibc
// reserves the first two real-time signals for its threading implementation.
// Go's syscall package does not export it, and the value must match what
// waybar computes, since waybar is glibc-linked.
const sigrtmin = 34

// debounce collapses a burst of changes into one signal. Creating an instance
// touches state several times in a second, and the bar only needs telling once.
const debounce = 250 * time.Millisecond

// Notifier signals waybar when state changes.
type Notifier struct {
	log    *slog.Logger
	signal int
	// enabled is false when signalling is switched off entirely.
	enabled bool

	mu      sync.Mutex
	pending bool
	timer   *time.Timer
}

// New builds a Notifier. A signal number of 0 disables it.
func New(signal int, log *slog.Logger) *Notifier {
	return &Notifier{log: log, signal: signal, enabled: signal > 0}
}

// FromEnv reads DBFORGE_WAYBAR_SIGNAL, defaulting to DefaultSignal. Setting it
// to 0 disables signalling for users who would rather poll.
func FromEnv(log *slog.Logger) *Notifier {
	sig := DefaultSignal
	if v := os.Getenv("DBFORGE_WAYBAR_SIGNAL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			sig = n
		}
	}
	return New(sig, log)
}

// Changed reports that instance state changed. Safe to call often: signals are
// debounced, and delivery failures are never fatal -- a bar that is not running
// is the normal case on a headless machine.
func (n *Notifier) Changed() {
	if n == nil || !n.enabled {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.pending {
		return
	}
	n.pending = true
	n.timer = time.AfterFunc(debounce, func() {
		n.mu.Lock()
		n.pending = false
		n.mu.Unlock()
		n.send()
	})
}

// Stop cancels any pending signal, for a clean shutdown.
func (n *Notifier) Stop() {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.timer != nil {
		n.timer.Stop()
	}
}

func (n *Notifier) send() {
	pids, err := waybarPIDs()
	if err != nil || len(pids) == 0 {
		return // no bar running; nothing to tell
	}
	sig := syscall.Signal(sigrtmin + n.signal)
	for _, pid := range pids {
		if err := syscall.Kill(pid, sig); err != nil {
			n.log.Debug("could not signal waybar", "pid", pid, "error", err)
		}
	}
}

// waybarPIDs finds running waybar processes by reading /proc.
//
// Deliberately not `pidof waybar`: shelling out from a daemon for something
// this small is wasteful, and /proc is the same source with no dependency.
func waybarPIDs() ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		comm, err := os.ReadFile(filepath.Join("/proc", e.Name(), "comm"))
		if err != nil {
			continue // the process exited between listing and reading
		}
		if strings.TrimSpace(string(comm)) == "waybar" {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}
