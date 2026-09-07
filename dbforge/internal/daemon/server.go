package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"sync"

	"github.com/fiifiofosu/dbforge/internal/model"
	"github.com/fiifiofosu/dbforge/internal/runtime"
)

// SocketPath is where dbforged listens. Frontends (CLI, TUI, waybar widget)
// all speak to this one socket, which is what keeps the daemon the single
// writer (spec 5).
func SocketPath() string {
	// DBFORGE_SOCKET is documented as overriding the socket path, and the CLI
	// has always honoured it. The daemon ignoring it meant the two ends of the
	// same override pointed at different files: `DBFORGE_SOCKET=x dbctl ls`
	// would talk to x while `DBFORGE_SOCKET=x dbforged` listened on the
	// default, and the only symptom was a connection refused.
	if s := os.Getenv("DBFORGE_SOCKET"); s != "" {
		return s
	}
	run := os.Getenv("XDG_RUNTIME_DIR")
	if run == "" {
		run = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	return filepath.Join(run, "dbforge", "dbforged.sock")
}

// Server exposes the Manager over HTTP on a Unix socket. HTTP rather than a
// bespoke protocol so the phase-4 waybar module can simply curl it.
type Server struct {
	mgr *Manager
	log *slog.Logger
}

func NewServer(m *Manager, log *slog.Logger) *Server { return &Server{mgr: m, log: log} }

type errorResponse struct {
	Error string `json:"error"`
	Kind  string `json:"kind,omitempty"`
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /instances", s.handleList)
	mux.HandleFunc("POST /instances", s.handleCreate)
	mux.HandleFunc("POST /instances/{id}/start", s.handleStart)
	mux.HandleFunc("POST /instances/{id}/stop", s.handleStop)
	mux.HandleFunc("POST /instances/{id}/restart", s.handleRestart)
	mux.HandleFunc("DELETE /instances/{id}", s.handleRemove)
	mux.HandleFunc("GET /instances/{id}/logs", s.handleLogs)
	mux.HandleFunc("GET /instances/{id}/connstring", s.handleConnString)
	mux.HandleFunc("PUT /instances/{id}/restart-policy", s.handleRestartPolicy)
	mux.HandleFunc("POST /restore", s.handleRestore)
	return mux
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	list, err := s.mgr.List(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	if list == nil {
		list = []model.Instance{}
	}
	writeJSON(w, http.StatusOK, list)
}

// CreateEvent is one line of a streaming create. Exactly one field is set:
// progress while the image is being fetched, then either the instance or an
// error. Streaming is opt-in, so a client that just wants the result is
// unaffected.
type CreateEvent struct {
	Progress *runtime.PullEvent `json:"progress,omitempty"`
	Instance *model.Instance    `json:"instance,omitempty"`
	Error    string             `json:"error,omitempty"`
	Kind     string             `json:"kind,omitempty"`
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	var opt CreateOptions
	if err := json.NewDecoder(r.Body).Decode(&opt); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	if r.URL.Query().Get("stream") == "true" {
		s.createStreaming(w, r, opt)
		return
	}
	inst, err := s.mgr.Create(r.Context(), opt)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, inst)
}

// createStreaming reports progress as newline-delimited JSON.
//
// The status code has to be sent before the work starts, so it is always 200:
// a failure is carried in the final event rather than the status line. That is
// the usual trade for a streamed response, and the client reads the outcome
// from the stream instead of from the header.
func (s *Server) createStreaming(w http.ResponseWriter, r *http.Request, opt CreateOptions) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	fw := &flushWriter{w: w, f: flusher}
	enc := json.NewEncoder(fw)

	// Serialised because Manager may call the callback from its own goroutine
	// and an Encoder is not safe for concurrent use.
	var mu sync.Mutex
	send := func(ev CreateEvent) {
		mu.Lock()
		defer mu.Unlock()
		_ = enc.Encode(ev)
	}

	opt.OnProgress = func(ev runtime.PullEvent) {
		e := ev
		send(CreateEvent{Progress: &e})
	}

	inst, err := s.mgr.Create(r.Context(), opt)
	if err != nil {
		send(CreateEvent{Error: err.Error(), Kind: errKind(err)})
		return
	}
	send(CreateEvent{Instance: &inst})
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.Start(r.Context(), r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	// 0 means the engine decides.
	timeout := uint(0)
	if v := r.URL.Query().Get("timeout"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			timeout = uint(n)
		}
	}
	if err := s.mgr.Stop(r.Context(), r.PathValue("id"), timeout); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	timeout := uint(0)
	if v := r.URL.Query().Get("timeout"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			timeout = uint(n)
		}
	}
	if err := s.mgr.RestartInstance(r.Context(), r.PathValue("id"), timeout); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "restarted"})
}

func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request) {
	opt := RemoveOptions{
		WipeData: r.URL.Query().Get("wipe_data") == "true",
		Force:    r.URL.Query().Get("force") == "true",
	}
	if err := s.mgr.Remove(r.Context(), r.PathValue("id"), opt); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	follow := r.URL.Query().Get("follow") == "true"
	tail, _ := strconv.Atoi(r.URL.Query().Get("tail"))

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	fw := &flushWriter{w: w, f: flusher}

	// r.Context() is cancelled when the client disconnects, which is what
	// stops a followed tail from leaking (spec 7, phase 3).
	if err := s.mgr.Logs(r.Context(), r.PathValue("id"), follow, tail, fw); err != nil &&
		!errors.Is(err, context.Canceled) {
		fmt.Fprintf(fw, "\nerror: %v\n", err)
	}
}

func (s *Server) handleConnString(w http.ResponseWriter, r *http.Request) {
	cs, err := s.mgr.ConnString(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"conn_string": cs})
}

func (s *Server) handleRestartPolicy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Policy string `json:"policy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	if err := s.mgr.SetRestartPolicy(r.Context(), r.PathValue("id"), model.RestartPolicy(body.Policy)); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"restart": body.Policy})
}

func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	rep, err := s.mgr.Restore(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

type flushWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func (fw *flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if fw.f != nil {
		fw.f.Flush()
	}
	return n, err
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// writeErr maps domain errors onto status codes so the CLI can distinguish
// "you asked for something impossible" from "the daemon broke".
func writeErr(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrInstanceNotFound):
		code = http.StatusNotFound
	case errors.Is(err, ErrInstanceExists):
		code = http.StatusConflict
	case errors.Is(err, ErrContainerMissing):
		code = http.StatusConflict
	}
	writeJSON(w, code, errorResponse{Error: err.Error(), Kind: errKind(err)})
}

// errKind names the error class for a client. A streamed response cannot use
// the status line, so this is shared with writeErr rather than duplicated.
func errKind(err error) string {
	switch {
	case errors.Is(err, ErrInstanceNotFound):
		return "not_found"
	case errors.Is(err, ErrInstanceExists):
		return "conflict"
	case errors.Is(err, ErrContainerMissing):
		return "missing_container"
	}
	return ""
}

// Serve listens on the Unix socket until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, socketPath string) error {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return err
	}
	// A socket left behind by a killed daemon would block the bind. Removing
	// a *live* daemon's socket would be worse, so probe it first.
	if _, err := os.Stat(socketPath); err == nil {
		if c, derr := net.DialTimeout("unix", socketPath, 300*time.Millisecond); derr == nil {
			c.Close()
			return fmt.Errorf("another dbforged is already listening on %s", socketPath)
		}
		os.Remove(socketPath)
	}

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", socketPath, err)
	}
	// The socket grants full control over the user's databases: owner only.
	if err := os.Chmod(socketPath, 0o600); err != nil {
		ln.Close()
		return err
	}

	srv := &http.Server{Handler: s.Handler()}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutCtx)
	}()

	s.log.Info("dbforged listening", "socket", socketPath)
	err = srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
