// Package ports allocates host ports for instances.
//
// The central rule (spec 7, phase 1) is that "not in our config" is not the
// same as "free". Another dev tool, a stray container, or a natively installed
// database can hold a port we know nothing about, so every candidate is proven
// free by actually binding it.
package ports

import (
	"fmt"
	"net"
)

// Range is the inclusive span searched for a free port.
type Range struct {
	Low, High int
}

// DefaultRange is used when an engine has no opinion.
var DefaultRange = Range{Low: 15000, High: 15999}

// Prober reports whether a port can be bound. It exists so tests can simulate
// contention without racing on real sockets.
type Prober func(port int) error

// BindProbe is the production Prober: it proves a port is free by binding it.
//
// It binds 127.0.0.1 specifically, because that is where instances are
// published. A port busy on another interface is irrelevant to us.
func BindProbe(port int) error {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	return l.Close()
}

// Allocator hands out ports, refusing to double-assign within a single run.
type Allocator struct {
	probe    Prober
	reserved map[int]string // port -> instance id holding it
}

// New builds an Allocator. taken maps already-assigned ports to the instance
// that owns them, so a restarted daemon does not reissue a live port.
func New(probe Prober, taken map[int]string) *Allocator {
	if probe == nil {
		probe = BindProbe
	}
	res := make(map[int]string, len(taken))
	for p, id := range taken {
		res[p] = id
	}
	return &Allocator{probe: probe, reserved: res}
}

// ErrPortTaken reports a port held by a known DBForge instance.
type ErrPortTaken struct {
	Port  int
	Owner string
}

func (e *ErrPortTaken) Error() string {
	return fmt.Sprintf("port %d is already assigned to instance %q", e.Port, e.Owner)
}

// ErrPortBusy reports a port held by something outside DBForge's knowledge.
type ErrPortBusy struct {
	Port int
	Err  error
}

func (e *ErrPortBusy) Error() string {
	return fmt.Sprintf("port %d is in use by another process: %v", e.Port, e.Err)
}

func (e *ErrPortBusy) Unwrap() error { return e.Err }

// Pin reserves one specific port for id, failing if it is unavailable. This
// backs `--port`, letting a user claim a conventional port such as 5432.
func (a *Allocator) Pin(port int, id string) error {
	if owner, ok := a.reserved[port]; ok {
		return &ErrPortTaken{Port: port, Owner: owner}
	}
	if err := a.probe(port); err != nil {
		return &ErrPortBusy{Port: port, Err: err}
	}
	a.reserved[port] = id
	return nil
}

// Allocate finds a free port for id, starting at base and staying inside r.
// Ports below base are also searched, so a busy base does not shrink the range.
func (a *Allocator) Allocate(base int, r Range, id string) (int, error) {
	if base < r.Low || base > r.High {
		base = r.Low
	}
	var lastErr error
	for _, p := range order(base, r) {
		if _, ok := a.reserved[p]; ok {
			continue
		}
		if err := a.probe(p); err != nil {
			lastErr = err
			continue
		}
		a.reserved[p] = id
		return p, nil
	}
	return 0, fmt.Errorf("no free port in range %d-%d (last error: %v)", r.Low, r.High, lastErr)
}

// Release gives a port back, e.g. when a create fails partway through.
func (a *Allocator) Release(port int) { delete(a.reserved, port) }

// order walks base..high then low..base-1.
func order(base int, r Range) []int {
	out := make([]int, 0, r.High-r.Low+1)
	for p := base; p <= r.High; p++ {
		out = append(out, p)
	}
	for p := r.Low; p < base; p++ {
		out = append(out, p)
	}
	return out
}
