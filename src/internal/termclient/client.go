package termclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Client is one signed-in rendezvous session. The session cookie lives in
// the jar and is sent on every API call and WebSocket dial.
type Client struct {
	server string
	http   *http.Client
}

// SignIn trades the app token for a session cookie.
func SignIn(ctx context.Context, creds Credentials) (*Client, error) {
	jar, _ := cookiejar.New(nil)
	c := &Client{server: creds.Server, http: &http.Client{Jar: jar, Timeout: httpTimeout}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.server+"/api/app/session", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+creds.Token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach %s: %w", c.server, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		return c, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, &AuthError{Server: c.server, Status: resp.Status}
	default:
		return nil, fmt.Errorf("%s answered %s to sign-in", c.server, resp.Status)
	}
}

// Server is the rendezvous base URL.
func (c *Client) Server() string { return c.server }

// Machine is one connected agent.
type Machine struct {
	ID          string    `json:"id"`
	Label       string    `json:"label"`
	Port        int       `json:"port"`
	Tunnels     int       `json:"tunnels"`
	ConnectedAt time.Time `json:"connected_at"`
	// URL is the path-mode base under which the machine's termulaa is
	// proxied, e.g. "/rc/t/<agent>/". Empty when the rendezvous serves
	// terminals from a dedicated view host, which this client cannot use.
	URL string `json:"url"`
}

// Machines lists the machines connected right now, sorted by label.
func (c *Client) Machines(ctx context.Context) ([]Machine, error) {
	var out struct {
		Agents []Machine `json:"agents"`
	}
	if err := c.api(ctx, http.MethodGet, "/rc/api/agents", nil, &out); err != nil {
		return nil, err
	}
	sort.Slice(out.Agents, func(i, j int) bool { return out.Agents[i].Label < out.Agents[j].Label })
	return out.Agents, nil
}

// Find picks the connected machine whose label or id is name.
func (c *Client) Find(ctx context.Context, name string) (Machine, error) {
	list, err := c.Machines(ctx)
	if err != nil {
		return Machine{}, err
	}
	var hits []Machine
	labels := make([]string, 0, len(list))
	for _, m := range list {
		labels = append(labels, m.Label)
		if m.Label == name || m.ID == name {
			hits = append(hits, m)
		}
	}
	switch {
	case len(hits) == 0:
		return Machine{}, &OfflineError{Machine: name, Connected: labels}
	case len(hits) > 1:
		return Machine{}, fmt.Errorf("%q names %d connected machines; use the id shown by `termulaa machines`", name, len(hits))
	case hits[0].URL == "":
		return Machine{}, fmt.Errorf("%s serves terminals from a dedicated view host; this client needs path mode (/rc/t/...)", c.server)
	}
	return hits[0], nil
}

// api performs one JSON call against the rendezvous (path is absolute, or
// relative to a machine's URL when it starts with that).
func (c *Client) api(ctx context.Context, method, path string, in, out interface{}) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.server+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return &AuthError{Server: c.server, Status: resp.Status}
	case resp.StatusCode == http.StatusNotFound:
		return &notFoundError{path: path}
	case resp.StatusCode/100 != 2:
		return fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}
	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := decodeJSON(resp, out); err != nil {
		return fmt.Errorf("%s %s: %w (the machine may be offline or remote access disabled)", method, path, err)
	}
	return nil
}

type notFoundError struct{ path string }

func (e *notFoundError) Error() string { return e.path + ": not found" }

// wsURL turns the rendezvous base URL plus a path into a WebSocket URL.
func (c *Client) wsURL(path string, q url.Values) (string, error) {
	u, err := url.Parse(c.server + path)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", errors.New("rendezvous URL must be http(s)")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// isTransient reports whether an error is worth retrying: anything but a
// definite "no" from the rendezvous or the machine.
func isTransient(err error) bool {
	var ae *AuthError
	var tg *TabGoneError
	var om *OldMachineError
	return err != nil && !errors.As(err, &ae) && !errors.As(err, &tg) && !errors.As(err, &om) && !strings.Contains(err.Error(), "view host")
}
