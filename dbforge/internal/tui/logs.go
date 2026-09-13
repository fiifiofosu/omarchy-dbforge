package tui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fiifiofosu/dbforge/internal/cli"
)

// maxLogLines caps what we keep in memory. A chatty database would otherwise
// grow the buffer without bound for as long as the tail is open.
const maxLogLines = 2000

type logLineMsg string

type logClosedMsg struct{ err error }

// logsModel streams one instance's logs.
//
// The two requirements from spec 7 phase 3 are that a long-running tail must
// not block the rest of the TUI, and must not leak file descriptors when the
// user backs out mid-tail. Both are handled the same way: the stream runs in
// its own goroutine feeding a channel, and stop() cancels the context that
// owns the HTTP response body.
type logsModel struct {
	client *cli.Client
	id     string

	width, height int

	lines  []string
	offset int // how many lines scrolled up from the bottom
	closed bool
	err    error

	// No mutex: append and view both run on the Bubble Tea goroutine, and the
	// reader goroutine only ever sends on ch. Holding a lock here would also
	// make the model uncopyable, which Bubble Tea requires.
	ch     chan tea.Msg
	cancel context.CancelFunc
	// once makes stop() idempotent: it is called both on explicit exit and on
	// quit, and cancelling twice must be harmless.
	once *sync.Once
}

func newLogsModel(c *cli.Client, id string, w, h int) logsModel {
	return logsModel{
		client: c, id: id, width: w, height: h,
		ch: make(chan tea.Msg, 256), once: &sync.Once{},
	}
}

func (l *logsModel) setSize(w, h int) { l.width, l.height = w, h }

// start opens the stream and returns a command that waits for its first line.
func (l *logsModel) start() tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	l.cancel = cancel

	client, id, ch := l.client, l.id, l.ch

	go func() {
		defer close(ch)

		pr, pw := io.Pipe()

		// The HTTP call writes into the pipe; cancelling ctx unblocks it and
		// closes the response body, which is what prevents the leak.
		go func() {
			err := client.Logs(ctx, id, true, 300, pw)
			pw.CloseWithError(err)
		}()

		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			select {
			case ch <- logLineMsg(sc.Text()):
			case <-ctx.Done():
				pr.CloseWithError(ctx.Err())
				return
			}
		}

		err := sc.Err()
		if ctx.Err() != nil {
			err = nil // a deliberate exit is not an error
		}
		select {
		case ch <- logClosedMsg{err: err}:
		case <-ctx.Done():
		}
	}()

	return l.waitForNext()
}

// waitForNext blocks on the channel inside a command, so the Bubble Tea event
// loop keeps running while the tail is idle.
func (l *logsModel) waitForNext() tea.Cmd {
	ch := l.ch
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return logClosedMsg{}
		}
		return msg
	}
}

// stop tears the stream down. Safe to call more than once.
func (l *logsModel) stop() {
	if l.once == nil || l.cancel == nil {
		return
	}
	l.once.Do(func() { l.cancel() })
}

func (l *logsModel) append(line logLineMsg) {
	l.lines = append(l.lines, string(line))
	if len(l.lines) > maxLogLines {
		l.lines = l.lines[len(l.lines)-maxLogLines:]
	}
}

func (l *logsModel) markClosed(err error) {
	l.closed = true
	l.err = err
}

func (m Model) updateLogs(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		// Backing out must cancel the stream, not orphan it.
		m.logs.stop()
		m.view = viewList
		return m, m.fetchInstances()
	case "up", "k":
		m.logs.offset++
	case "down", "j":
		if m.logs.offset > 0 {
			m.logs.offset--
		}
	case "pgup":
		m.logs.offset += m.logs.pageSize()
	case "pgdown":
		m.logs.offset = max(0, m.logs.offset-m.logs.pageSize())
	case "G", "end":
		m.logs.offset = 0
	}
	return m, nil
}

func (l *logsModel) pageSize() int {
	n := l.height - 6
	if n < 1 {
		return 10
	}
	return n
}

func (l *logsModel) view() string {
	var b strings.Builder

	b.WriteString(styleTitle.Render("Logs: " + l.id))
	if l.closed {
		b.WriteString(styleDim.Render("  (stream ended)"))
	} else {
		b.WriteString(styleDim.Render("  (following)"))
	}
	b.WriteString("\n\n")

	lines := l.lines

	if len(lines) == 0 {
		b.WriteString(styleDim.Render("waiting for output..."))
	} else {
		size := l.pageSize()
		end := len(lines) - l.offset
		end = min(max(end, 0), len(lines))
		start := max(0, end-size)

		width := max(20, l.width-2)
		for _, line := range lines[start:end] {
			b.WriteString(truncate(line, width))
			b.WriteString("\n")
		}
		if l.offset > 0 {
			b.WriteString(styleWarn.Render(fmt.Sprintf("\n-- scrolled up %d lines --", l.offset)))
			b.WriteString("\n")
		}
	}

	if l.err != nil {
		b.WriteString("\n")
		b.WriteString(styleErr.Render(l.err.Error()))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(styleHelp.Render("j/k scroll   pgup/pgdn page   G bottom   esc back"))
	return b.String()
}
