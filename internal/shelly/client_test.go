package shelly

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func rpcHandler(t *testing.T, requireAuth bool) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if requireAuth && r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate",
				`Digest qop="auth", realm="shelly1-test", nonce="1652184000", algorithm=SHA-256`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req RPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		switch req.Method {
		case "Shelly.GetDeviceInfo":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":     req.ID,
				"src":    "shelly1-test",
				"result": DeviceInfo{ID: "shelly1-test", Model: "SNSW-001X16EU", Gen: 2},
			})
		case "Fail.Always":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":    req.ID,
				"src":   "shelly1-test",
				"error": RPCError{Code: -103, Message: "Invalid argument"},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": req.ID, "src": "shelly1-test", "result": map[string]any{},
			})
		}
	}
}

func testClient(t *testing.T, requireAuth bool, opts ...Option) *Client {
	t.Helper()
	srv := httptest.NewServer(rpcHandler(t, requireAuth))
	t.Cleanup(srv.Close)
	addr := strings.TrimPrefix(srv.URL, "http://")
	return NewClient(addr, opts...)
}

func TestCallSuccess(t *testing.T) {
	c := testClient(t, false)
	info, err := c.GetDeviceInfo(context.Background())
	if err != nil {
		t.Fatalf("GetDeviceInfo: %v", err)
	}
	if info.ID != "shelly1-test" || info.Gen != 2 {
		t.Fatalf("unexpected device info: %+v", info)
	}
}

func TestCallRPCError(t *testing.T) {
	c := testClient(t, false)
	err := c.Call(context.Background(), "Fail.Always", nil, nil)
	var rpcErr *RPCError
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.As(err, &rpcErr) || rpcErr.Code != -103 {
		t.Fatalf("expected RPCError -103, got %v", err)
	}
}

func TestDigestAuthRetry(t *testing.T) {
	c := testClient(t, true, WithPassword("secret"))
	if _, err := c.GetDeviceInfo(context.Background()); err != nil {
		t.Fatalf("GetDeviceInfo with auth: %v", err)
	}
}

func TestAuthFailsWithoutPassword(t *testing.T) {
	c := testClient(t, true)
	if _, err := c.GetDeviceInfo(context.Background()); err == nil {
		t.Fatal("expected auth failure")
	}
}

func TestDigestAuthHeader(t *testing.T) {
	challenge := `Digest qop="auth", realm="shelly1-abc", nonce="12345", algorithm=SHA-256`
	header, err := digestAuthHeader(challenge, "admin", "pw", http.MethodPost, "/rpc")
	if err != nil {
		t.Fatalf("digestAuthHeader: %v", err)
	}
	for _, want := range []string{`username="admin"`, `realm="shelly1-abc"`, `nonce="12345"`, "algorithm=SHA-256", `uri="/rpc"`} {
		if !strings.Contains(header, want) {
			t.Errorf("header missing %s: %s", want, header)
		}
	}
}
