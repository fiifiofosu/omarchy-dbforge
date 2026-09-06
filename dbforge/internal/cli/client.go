// Package cli implements dbctl, the scriptable frontend. It talks to dbforged
// over the Unix socket rather than to Podman directly, so that all mutations
// funnel through the single writer (spec 5).
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"

	"github.com/fiifiofosu/dbforge/internal/daemon"
	"github.com/fiifiofosu/dbforge/internal/model"
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

func (c *Client) Remove(ctx context.Context, id string, wipe, force bool) error {
	q := fmt.Sprintf("?wipe_data=%t&force=%t", wipe, force)
	resp, err := c.do(ctx, http.MethodDelete, "/instances/"+url.PathEscape(id)+q, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
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
	resp, err := c.do(ctx, http.MethodGet, "/instances/"+url.PathEscape(id)+"/connstring", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		ConnString string `json:"conn_string"`
	}
	err = json.NewDecoder(resp.Body).Decode(&out)
	return out.ConnString, err
}
