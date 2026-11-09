package termclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Credentials is what pairing leaves behind: the rendezvous and the app
// token it issued. The token signs in; it is never shown to the user.
type Credentials struct {
	Server string `json:"server"`
	Token  string `json:"token"`
}

// CredentialsPath is ~/.termulaa/client.json, beside the agent's rc.json.
func CredentialsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".termulaa", "client.json")
}

// LoadCredentials reads the stored pairing; a missing file is ErrNotPaired.
func LoadCredentials(path string) (Credentials, error) {
	var c Credentials
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, ErrNotPaired
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	if c.Server == "" || c.Token == "" {
		return c, fmt.Errorf("%s: incomplete; pair again", path)
	}
	return c, nil
}

// Save writes the credentials readable by the owner only.
func (c Credentials) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

const httpTimeout = 30 * time.Second

// Pair redeems a one-time pairing code from the rendezvous dashboard for a
// long-lived app token. label names this client on the dashboard's list of
// paired devices.
func Pair(ctx context.Context, server, code, label string) (Credentials, error) {
	server = normalizeServer(server)
	if server == "" {
		return Credentials{}, errors.New("server must be an http(s) URL")
	}
	body, _ := json.Marshal(map[string]string{"code": strings.TrimSpace(code), "label": label})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server+"/api/app/redeem", bytes.NewReader(body))
	if err != nil {
		return Credentials{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: httpTimeout}).Do(req)
	if err != nil {
		return Credentials{}, fmt.Errorf("cannot reach %s: %w", server, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return Credentials{}, fmt.Errorf("%s did not accept the pairing code: codes are single-use and expire within minutes; get a fresh one from the dashboard", server)
	case http.StatusTooManyRequests:
		return Credentials{}, fmt.Errorf("%s refused: too many pairing attempts, wait a minute", server)
	default:
		return Credentials{}, fmt.Errorf("%s answered %s to the pairing request", server, resp.Status)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(resp, &out); err != nil || out.Token == "" {
		return Credentials{}, fmt.Errorf("%s returned no token: is this a memd server with the phone app enabled?", server)
	}
	return Credentials{Server: server, Token: out.Token}, nil
}

// normalizeServer trims and strips a trailing slash; anything that is not
// http(s) comes back empty.
func normalizeServer(s string) string {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return ""
	}
	return s
}

// decodeJSON refuses to parse a body that is not JSON, so an HTML sign-in
// page or an offline notice never passes for an API answer.
func decodeJSON(resp *http.Response, out interface{}) error {
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		return fmt.Errorf("expected JSON, got %q", ct)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
