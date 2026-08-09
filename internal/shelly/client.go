package shelly

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// Client talks to a single Shelly Gen2+ device over HTTP.
//
// Authentication: Gen2+ devices protect the HTTP RPC endpoint with digest
// auth (RFC 7616, algorithm SHA-256; user is always "admin"). The client
// retries a 401 once with a computed Authorization header.
type Client struct {
	addr     string
	password string
	http     *http.Client
	nextID   atomic.Int64
}

// Option configures a Client.
type Option func(*Client)

// WithPassword sets the device password used for digest authentication.
func WithPassword(pw string) Option { return func(c *Client) { c.password = pw } }

// WithHTTPClient replaces the underlying HTTP client (used in tests and
// to tune timeouts).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// NewClient creates a client for the device reachable at addr (IP,
// host, or host:port — no scheme).
func NewClient(addr string, opts ...Option) *Client {
	c := &Client{
		addr: addr,
		http: &http.Client{Timeout: 10 * time.Second},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Addr returns the address the client was created with.
func (c *Client) Addr() string { return c.addr }

// Call invokes a Shelly RPC method and unmarshals the result into out
// when out is non-nil.
func (c *Client) Call(ctx context.Context, method string, params any, out any) error {
	req := RPCRequest{
		ID:     int(c.nextID.Add(1)),
		Src:    "shellyctl",
		Method: method,
		Params: params,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal rpc request: %w", err)
	}

	url := "http://" + c.addr + "/rpc"
	resp, err := c.post(ctx, url, body, "")
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized && c.password != "" {
		challenge := resp.Header.Get("WWW-Authenticate")
		_ = resp.Body.Close()
		authHeader, err := digestAuthHeader(challenge, "admin", c.password, http.MethodPost, "/rpc")
		if err != nil {
			return fmt.Errorf("device requires auth: %w", err)
		}
		resp, err = c.post(ctx, url, body, authHeader)
		if err != nil {
			return err
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("authentication failed for %s (check password)", c.addr)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read rpc response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("device %s returned HTTP %d: %s", c.addr, resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var rpcResp RPCResponse
	if err := json.Unmarshal(raw, &rpcResp); err != nil {
		return fmt.Errorf("decode rpc response: %w", err)
	}
	if rpcResp.Error != nil {
		return rpcResp.Error
	}
	if out != nil {
		if err := json.Unmarshal(rpcResp.Result, out); err != nil {
			return fmt.Errorf("decode rpc result for %s: %w", method, err)
		}
	}
	return nil
}

func (c *Client) post(ctx context.Context, url string, body []byte, auth string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call %s: %w", c.addr, err)
	}
	return resp, nil
}

// GetDeviceInfo fetches Shelly.GetDeviceInfo.
func (c *Client) GetDeviceInfo(ctx context.Context) (DeviceInfo, error) {
	var info DeviceInfo
	err := c.Call(ctx, "Shelly.GetDeviceInfo", nil, &info)
	return info, err
}

// digestAuthHeader computes an RFC 7616 digest Authorization header from
// a WWW-Authenticate challenge. Shelly Gen2+ uses algorithm SHA-256; MD5
// is supported as a fallback for completeness.
func digestAuthHeader(challenge, username, password, method, uri string) (string, error) {
	if !strings.HasPrefix(challenge, "Digest ") {
		return "", fmt.Errorf("unsupported auth challenge %q", challenge)
	}
	params := parseChallenge(strings.TrimPrefix(challenge, "Digest "))
	realm, nonce := params["realm"], params["nonce"]
	if realm == "" || nonce == "" {
		return "", fmt.Errorf("malformed digest challenge %q", challenge)
	}
	algo := params["algorithm"]
	if algo == "" {
		algo = "SHA-256"
	}
	var newHash func() hash.Hash
	switch strings.ToUpper(algo) {
	case "SHA-256":
		newHash = sha256.New
	case "MD5":
		newHash = md5.New
	default:
		return "", fmt.Errorf("unsupported digest algorithm %q", algo)
	}
	h := func(s string) string {
		hh := newHash()
		hh.Write([]byte(s))
		return fmt.Sprintf("%x", hh.Sum(nil))
	}

	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	cnonce := fmt.Sprintf("%x", binary.BigEndian.Uint64(buf[:]))
	const nc = "00000001"

	ha1 := h(username + ":" + realm + ":" + password)
	ha2 := h(method + ":" + uri)
	response := h(strings.Join([]string{ha1, nonce, nc, cnonce, "auth", ha2}, ":"))

	return fmt.Sprintf(
		`Digest username="%s", realm="%s", nonce="%s", uri="%s", qop=auth, nc=%s, cnonce="%s", response="%s", algorithm=%s`,
		username, realm, nonce, uri, nc, cnonce, response, algo,
	), nil
}

func parseChallenge(s string) map[string]string {
	out := map[string]string{}
	for _, part := range splitChallenge(s) {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(k))] = strings.Trim(strings.TrimSpace(v), `"`)
	}
	return out
}

// splitChallenge splits a digest challenge on commas that are not inside
// quoted strings.
func splitChallenge(s string) []string {
	var parts []string
	var b strings.Builder
	inQuotes := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuotes = !inQuotes
			b.WriteRune(r)
		case r == ',' && !inQuotes:
			parts = append(parts, b.String())
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() > 0 {
		parts = append(parts, b.String())
	}
	return parts
}
