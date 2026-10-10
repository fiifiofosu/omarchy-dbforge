package tui

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fiifiofosu/dbforge/internal/cli"
)

// logServer streams lines forever until the client disconnects, and reports
// whether the handler ever returned. That is the signal for a leak: if backing
// out of the TUI does not cancel the request, the handler runs on.
func logServer(t *testing.T) (*cli.Client, *atomic.Bool) {
	t.Helper()
	var handlerReturned atomic.Bool

	mux := http.NewServeMux()
	mux.HandleFunc("/instances/{id}/logs", func(w http.ResponseWriter, r *http.Request) {
		defer handlerReturned.Store(true)
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for i := 0; ; i++ {
			select {
			case <-r.Context().Done():
				return
			default:
			}
			if _, err := fmt.Fprintf(w, "line %d\n", i); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(5 * time.Millisecond)
		}
	})

	// Serve over a unix socket, matching how the real client dials.
	sock := t.TempDir() + "/d.sock"
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("cannot listen on a unix socket here: %v", err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	return cli.NewClient(sock), &handlerReturned
}

// Backing out of a tail must cancel the stream rather than orphan it
// (spec 7, phase 3).
func TestBackingOutOfLogsCancelsTheStream(t *testing.T) {
	client, handlerReturned := logServer(t)

	m := New(client)
	m.width, m.height = 80, 24
	m.logs = newLogsModel(client, "app-db", 80, 24)
	m.view = viewLogs

	cmd := m.logs.start()
	// Pump a few lines through, as the event loop would.
	for i := 0; i < 3 && cmd != nil; i++ {
		msg := cmd()
		if line, ok := msg.(logLineMsg); ok {
			m.logs.append(line)
			cmd = m.logs.waitForNext()
			continue
		}
		break
	}
	if len(m.logs.lines) == 0 {
		t.Fatal("no log lines were received")
	}

	// The user presses esc.
	out, _ := m.updateLogs(tea.KeyMsg{Type: tea.KeyEsc})
	if out.(Model).view != viewList {
		t.Fatal("esc did not return to the list")
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if handlerReturned.Load() {
			return // the server saw the disconnect
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the log handler is still running after the user backed out: the stream leaked")
}

func TestQuitFromLogsCancelsTheStream(t *testing.T) {
	client, handlerReturned := logServer(t)

	m := New(client)
	m.logs = newLogsModel(client, "app-db", 80, 24)
	m.view = viewLogs

	// Wait for the stream to actually establish. Cancelling before the request
	// reaches the server would prove nothing: the handler would never run.
	cmd := m.logs.start()
	if _, ok := cmd().(logLineMsg); !ok {
		t.Fatal("the log stream never produced a line")
	}

	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if handlerReturned.Load() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("ctrl+c left the log stream running")
}

// stop() runs on both the esc path and the quit path, so it must be safe to
// call twice.
func TestStopIsIdempotent(t *testing.T) {
	client, _ := logServer(t)
	l := newLogsModel(client, "app-db", 80, 24)
	l.start()
	l.stop()
	l.stop() // must not panic on a second cancel
}

func TestStopBeforeStartIsSafe(t *testing.T) {
	l := newLogsModel(nil, "app-db", 80, 24)
	l.stop() // no goroutine was ever created
}

// A chatty database must not grow the buffer without bound.
func TestLogBufferIsCapped(t *testing.T) {
	l := newLogsModel(nil, "x", 80, 24)
	for i := 0; i < maxLogLines+500; i++ {
		l.append(logLineMsg(fmt.Sprintf("line %d", i)))
	}
	if len(l.lines) > maxLogLines {
		t.Fatalf("buffer holds %d lines, cap is %d", len(l.lines), maxLogLines)
	}
	// The newest lines are the ones worth keeping.
	if !strings.Contains(l.lines[len(l.lines)-1], fmt.Sprint(maxLogLines+499)) {
		t.Fatal("the most recent line was dropped instead of the oldest")
	}
}

func TestLogScrollingStaysInBounds(t *testing.T) {
	m := New(nil)
	m.logs = newLogsModel(nil, "x", 80, 24)
	m.view = viewLogs
	for i := 0; i < 50; i++ {
		m.logs.append(logLineMsg(fmt.Sprintf("line %d", i)))
	}

	// Scrolling down at the bottom must not go negative.
	out, _ := m.updateLogs(tea.KeyMsg{Type: tea.KeyDown})
	m = out.(Model)
	if m.logs.offset < 0 {
		t.Fatalf("offset went negative: %d", m.logs.offset)
	}

	// Scrolling far up then rendering must not panic or slice out of range.
	for i := 0; i < 100; i++ {
		out, _ = m.updateLogs(tea.KeyMsg{Type: tea.KeyUp})
		m = out.(Model)
	}
	_ = m.logs.view()
}

// A database client controls what its server logs. An OSC 52 sequence in a
// log line would set the user's clipboard in a terminal that allows it, so
// the log view must never pass one through.
func TestLogLinesAreTerminalSafe(t *testing.T) {
	var l logsModel
	l.width, l.height = 120, 40
	l.append(logLineMsg("ERROR:  relation \"x\x1b]52;c;cHduZWQ=\x07\" does not exist"))
	l.append(logLineMsg("\x1b[2J\x1b]0;owned\x1b\\\tcleared"))

	for _, line := range l.lines {
		if strings.ContainsAny(line, "\x1b\x07\u009b\u009d") {
			t.Fatalf("control sequence survived in stored line %q", line)
		}
	}
	if got, want := l.lines[0], `ERROR:  relation "x" does not exist`; got != want {
		t.Errorf("line 0 = %q, want %q", got, want)
	}
	if got, want := l.lines[1], "    cleared"; got != want {
		t.Errorf("line 1 = %q, want %q", got, want)
	}
	if strings.Contains(l.view(), "]52;") {
		t.Fatal("OSC 52 payload reached the rendered view")
	}
}
