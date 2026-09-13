package ports

import (
	"errors"
	"fmt"
	"net"
	"testing"
)

// busy simulates a set of ports held by processes outside DBForge.
func busy(held ...int) Prober {
	set := map[int]bool{}
	for _, p := range held {
		set[p] = true
	}
	return func(p int) error {
		if set[p] {
			return fmt.Errorf("address already in use")
		}
		return nil
	}
}

func TestAllocateStartsAtBase(t *testing.T) {
	a := New(busy(), nil)
	got, err := a.Allocate(5433, Range{5432, 5500}, "pg")
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if got != 5433 {
		t.Fatalf("got port %d, want 5433", got)
	}
}

func TestAllocateSkipsExternallyBusyPort(t *testing.T) {
	// The port is free as far as our config knows, but bound by something
	// else -- the allocator must detect that by probing, not by trusting.
	a := New(busy(5433, 5434), nil)
	got, err := a.Allocate(5433, Range{5432, 5500}, "pg")
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if got != 5435 {
		t.Fatalf("got port %d, want 5435 (5433/5434 externally busy)", got)
	}
}

func TestAllocateNeverDoubleAssigns(t *testing.T) {
	a := New(busy(), map[int]string{5433: "existing"})
	got, err := a.Allocate(5433, Range{5432, 5500}, "new")
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if got == 5433 {
		t.Fatal("allocator reissued a port already held by another instance")
	}
}

func TestAllocateWrapsBelowBase(t *testing.T) {
	// Everything from base to the top of the range is busy; the allocator
	// should still find the free port underneath rather than give up.
	a := New(busy(5440, 5441), nil)
	got, err := a.Allocate(5440, Range{5432, 5441}, "pg")
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if got != 5432 {
		t.Fatalf("got %d, want 5432 (wrap below base)", got)
	}
}

func TestAllocateExhaustedRange(t *testing.T) {
	a := New(busy(5432, 5433), nil)
	if _, err := a.Allocate(5432, Range{5432, 5433}, "pg"); err == nil {
		t.Fatal("expected an error when every port in range is busy")
	}
}

func TestPinRejectsPortHeldByKnownInstance(t *testing.T) {
	a := New(busy(), map[int]string{5432: "pg16-app"})
	err := a.Pin(5432, "other")
	var taken *ErrPortTaken
	if !errors.As(err, &taken) {
		t.Fatalf("got %v, want ErrPortTaken", err)
	}
	if taken.Owner != "pg16-app" {
		t.Fatalf("owner = %q, want pg16-app", taken.Owner)
	}
}

func TestPinRejectsExternallyBusyPort(t *testing.T) {
	a := New(busy(5432), nil)
	var b *ErrPortBusy
	if err := a.Pin(5432, "pg"); !errors.As(err, &b) {
		t.Fatalf("got %v, want ErrPortBusy", err)
	}
}

func TestReleaseFreesPortForReuse(t *testing.T) {
	a := New(busy(), nil)
	p, _ := a.Allocate(5433, Range{5432, 5500}, "doomed")
	a.Release(p)
	got, err := a.Allocate(5433, Range{5432, 5500}, "replacement")
	if err != nil {
		t.Fatalf("Allocate after Release: %v", err)
	}
	if got != p {
		t.Fatalf("got %d, want the released port %d", got, p)
	}
}

// TestBindProbeAgainstRealSocket exercises the production prober, which is the
// part that has to be right for the "port busy outside our knowledge" case.
func TestBindProbeAgainstRealSocket(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot bind loopback in this environment: %v", err)
	}
	defer l.Close()
	held := l.Addr().(*net.TCPAddr).Port

	if err := BindProbe(held); err == nil {
		t.Fatalf("BindProbe said port %d was free while we hold it", held)
	}
	l.Close()
	if err := BindProbe(held); err != nil {
		t.Fatalf("BindProbe said released port %d was busy: %v", held, err)
	}
}
