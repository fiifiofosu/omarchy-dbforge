// Package cli implements dbctl, the scriptable frontend. It talks to dbforged
// over the Unix socket rather than to Podman directly, so that all mutations
// funnel through the single writer (spec 5).
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"

	"github.com/fiifiofosu/dbforge/internal/daemon"
	"github.com/fiifiofosu/dbforge/internal/model"
	"github.com/fiifiofosu/dbforge/internal/runtime"
)

// Client speaks HTTP over the daemon's Unix socket.
type Client struct {
	http   *http.Client
	socket string
}

// ErrDaemonUnreachable is distinct from "no instances" on purpose: the widget
// and the CLI must render those differently (spec 7, phase 4).
type ErrDaemonUnreachable struct {
	Socket string
	Err    error
}

func (e *ErrDaemonUnreachable) Error() string {
	return fmt.Sprintf("cannot reach dbforged at %s: %v\n"+
		"hint: start it with `systemctl --user start dbforged` (or run `dbforged` in a terminal)",
		e.Socket, e.Err)
}

func (e *ErrDaemonUnreachable) Unwrap() error { return e.Err }

func NewClient(socket string) *Client {
	if socket == "" {
		socket = daemon.SocketPath()
	}
	return &Client{
		socket: socket,
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socket)
				},
			},
		},
	}
}

type apiError struct {
	Error string `json:"error"`
	Kind  string `json:"kind"`
}

func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://dbforge"+path, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &ErrDaemonUnreachable{Socket: c.socket, Err: err}
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		var ae apiError
		json.NewDecoder(resp.Body).Decode(&ae)
		if ae.Error == "" {
			ae.Error = resp.Status
		}
		return nil, fmt.Errorf("%s", ae.Error)
	}
	return resp, nil
}

func (c *Client) List(ctx context.Context) ([]model.Instance, error) {
	resp, err := c.do(ctx, http.MethodGet, "/instances", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out []model.Instance
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

func (c *Client) Create(ctx context.Context, opt daemon.CreateOptions) (model.Instance, error) {
	resp, err := c.do(ctx, http.MethodPost, "/instances", opt)
	if err != nil {
		return model.Instance{}, err
	}
	defer resp.Body.Close()
	var out model.Instance
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

// CreateStream creates an instance, reporting image-pull progress as it goes.
//
// Create blocks with nothing to show for however long a first pull takes,
// which on a slow link is minutes of apparent hang. This reports each phase as
// the daemon reaches it, so the caller can say what is happening.
//
// The response is 200 as soon as the work starts, so failures arrive in the
// stream rather than the status line -- see the server side for why.
func (c *Client) CreateStream(ctx context.Context, opt daemon.CreateOptions, onProgress func(runtime.PullEvent)) (model.Instance, error) {
	resp, err := c.do(ctx, http.MethodPost, "/instances?stream=true", opt)
	if err != nil {
		return model.Instance{}, err
	}
	defer resp.Body.Close()

	dec := json.NewDecoder(resp.Body)
	for {
		var ev daemon.CreateEvent
		if err := dec.Decode(&ev); err != nil {
			if errors.Is(err, io.EOF) {
				// The daemon closed without a verdict: it died, or was
				// restarted mid-pull. Saying so beats returning a zero
				// instance and no error.
				return model.Instance{}, errors.New("daemon closed the connection before the instance was created")
			}
			return model.Instance{}, err
		}
		switch {
		case ev.Error != "":
			return model.Instance{}, errors.New(ev.Error)
		case ev.Instance != nil:
			return *ev.Instance, nil
		case ev.Progress != nil && onProgress != nil:
			onProgress(*ev.Progress)
		}
	}
}

// Version reports the daemon's build version.
func (c *Client) Version(ctx context.Context) (string, error) {
	resp, err := c.do(ctx, http.MethodGet, "/version", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Version, nil
}

func (c *Client) Start(ctx context.Context, id string) error {
	resp, err := c.do(ctx, http.MethodPost, "/instances/"+url.PathEscape(id)+"/start", nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (c *Client) Stop(ctx context.Context, id string) error {
	resp, err := c.do(ctx, http.MethodPost, "/instances/"+url.PathEscape(id)+"/stop", nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (c *Client) RestartInstance(ctx context.Context, id string) error {
	resp, err := c.do(ctx, http.MethodPost, "/instances/"+url.PathEscape(id)+"/restart", nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (c *Client) Remove(ctx context.Context, id string, wipe, force bool) error {
	q := fmt.Sprintf("?wipe_data=%t&force=%t", wipe, force)
	resp, err := c.do(ctx, http.MethodDelete, "/instances/"+url.PathEscape(id)+q, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (c *Client) SetRestartPolicy(ctx context.Context, id, policy string) error {
	body := map[string]string{"policy": policy}
	resp, err := c.do(ctx, http.MethodPut, "/instances/"+url.PathEscape(id)+"/restart-policy", body)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// SuspendReport mirrors daemon.SuspendReport.
type SuspendReport struct {
	Stopped []string          `json:"Stopped"`
	Failed  map[string]string `json:"Failed"`
}

// Shutdown stops every running instance and then the daemon.
//
// The daemon replies before it exits, so a normal response is expected. A
// connection dropped without one still means the shutdown happened -- the
// daemon does not get that far and then change its mind -- so callers treat
// an EOF here as success rather than telling the user it failed.
func (c *Client) Shutdown(ctx context.Context) (SuspendReport, error) {
	resp, err := c.do(ctx, http.MethodPost, "/shutdown", nil)
	if err != nil {
		return SuspendReport{}, err
	}
	defer resp.Body.Close()
	var out SuspendReport
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return SuspendReport{}, err
	}
	return out, nil
}

// RestoreReport mirrors daemon.RestoreReport.
type RestoreReport struct {
	Started []string          `json:"Started"`
	Failed  map[string]string `json:"Failed"`
	Skipped map[string]string `json:"Skipped"`
}

func (c *Client) Restore(ctx context.Context) (RestoreReport, error) {
	resp, err := c.do(ctx, http.MethodPost, "/restore", nil)
	if err != nil {
		return RestoreReport{}, err
	}
	defer resp.Body.Close()
	var out RestoreReport
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

func (c *Client) Logs(ctx context.Context, id string, follow bool, tail int, w io.Writer) error {
	q := fmt.Sprintf("?follow=%t&tail=%d", follow, tail)
	resp, err := c.do(ctx, http.MethodGet, "/instances/"+url.PathEscape(id)+"/logs"+q, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(w, resp.Body)
	return err
}

func (c *Client) ConnString(ctx context.Context, id string) (string, error) {
	conn, _, err := c.Credentials(ctx, id)
	return conn, err
}

// Credentials returns an instance's connection string and password. The
// password is per-instance on purpose: it is not in the list response, so
// nothing that dumps every instance dumps every password with it.
func (c *Client) Credentials(ctx context.Context, id string) (conn, password string, err error) {
	resp, err := c.do(ctx, http.MethodGet, "/instances/"+url.PathEscape(id)+"/connstring", nil)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	var out struct {
		ConnString string `json:"conn_string"`
		Password   string `json:"password"`
	}
	err = json.NewDecoder(resp.Body).Decode(&out)
	return out.ConnString, out.Password, err
}
