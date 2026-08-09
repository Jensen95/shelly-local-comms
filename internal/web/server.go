// Package web serves the embedded browser UI and the JSON REST API. It
// depends only on the app.Manager facade, mirroring the TUI.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/Jensen95/shelly-local-comms/internal/app"
	"github.com/Jensen95/shelly-local-comms/internal/shelly"
)

//go:embed static
var staticFiles embed.FS

// defaultDiscoverSeconds is used when POST /api/discover omits a timeout.
const defaultDiscoverSeconds = 5

// shutdownTimeout bounds the graceful drain in Serve.
const shutdownTimeout = 5 * time.Second

// NewServer returns an http.Handler exposing the JSON API under /api/ and
// the embedded single-page UI at /.
func NewServer(m app.Manager) http.Handler {
	s := &server{m: m}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/devices", s.listDevices)
	mux.HandleFunc("POST /api/devices", s.addDevice)
	mux.HandleFunc("DELETE /api/devices/{key}", s.removeDevice)
	mux.HandleFunc("POST /api/discover", s.discover)

	mux.HandleFunc("GET /api/links", s.listLinks)
	mux.HandleFunc("POST /api/links", s.saveLink)
	mux.HandleFunc("DELETE /api/links/{id}", s.deleteLink)
	mux.HandleFunc("POST /api/links/{id}/deploy", s.deployLink)
	mux.HandleFunc("GET /api/links/{id}/script", s.linkScript)

	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("POST /api/health/probe", s.probeNow)

	mux.HandleFunc("POST /api/settings/mqtt", s.applyMQTT)
	mux.HandleFunc("POST /api/settings/ble", s.applyBLE)
	mux.HandleFunc("POST /api/settings/extender", s.rangeExtender)
	mux.HandleFunc("POST /api/settings/extender/join", s.joinExtender)

	// Unknown /api paths get a JSON 404 instead of the SPA fallback.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})

	static, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic("web: embedded static dir missing: " + err.Error())
	}
	mux.Handle("/", http.FileServerFS(static))

	return mux
}

// Serve runs an http.Server on addr until ctx is cancelled, then shuts it
// down gracefully. It returns nil after a clean shutdown.
func Serve(ctx context.Context, addr string, m app.Manager) error {
	srv := &http.Server{Addr: addr, Handler: NewServer(m)}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

type server struct {
	m app.Manager
}

// --- Devices ---

func (s *server) listDevices(w http.ResponseWriter, r *http.Request) {
	devices := s.m.Devices()
	if devices == nil {
		devices = []shelly.Device{}
	}
	writeJSON(w, http.StatusOK, devices)
}

type addDeviceRequest struct {
	Addr     string `json:"addr"`
	Password string `json:"password"`
}

func (s *server) addDevice(w http.ResponseWriter, r *http.Request) {
	var req addDeviceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Addr) == "" {
		writeError(w, http.StatusBadRequest, "addr is required")
		return
	}
	dev, err := s.m.AddDevice(r.Context(), req.Addr, req.Password)
	if err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dev)
}

func (s *server) removeDevice(w http.ResponseWriter, r *http.Request) {
	if err := s.m.RemoveDevice(r.PathValue("key")); err != nil {
		writeManagerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type discoverRequest struct {
	TimeoutSeconds int `json:"timeout_seconds"`
}

func (s *server) discover(w http.ResponseWriter, r *http.Request) {
	req := discoverRequest{}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if req.TimeoutSeconds < 0 {
		writeError(w, http.StatusBadRequest, "timeout_seconds must not be negative")
		return
	}
	if req.TimeoutSeconds == 0 {
		req.TimeoutSeconds = defaultDiscoverSeconds
	}
	found, err := s.m.Discover(r.Context(), req.TimeoutSeconds)
	if err != nil {
		writeManagerError(w, err)
		return
	}
	if found == nil {
		found = []shelly.Device{}
	}
	writeJSON(w, http.StatusOK, found)
}

// --- Links ---

func (s *server) listLinks(w http.ResponseWriter, r *http.Request) {
	links := s.m.Links()
	if links == nil {
		links = []app.Link{}
	}
	writeJSON(w, http.StatusOK, links)
}

func (s *server) saveLink(w http.ResponseWriter, r *http.Request) {
	var l app.Link
	if !decodeJSON(w, r, &l) {
		return
	}
	switch {
	case strings.TrimSpace(l.SourceDevice) == "":
		writeError(w, http.StatusBadRequest, "source_device is required")
		return
	case strings.TrimSpace(l.TargetDevice) == "":
		writeError(w, http.StatusBadRequest, "target_device is required")
		return
	case strings.TrimSpace(l.TargetMethod) == "":
		writeError(w, http.StatusBadRequest, "target_method is required")
		return
	}
	if l.Fallback == (app.FallbackConfig{}) {
		l.Fallback = app.DefaultFallback()
	}
	saved, err := s.m.SaveLink(l)
	if err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *server) deleteLink(w http.ResponseWriter, r *http.Request) {
	if err := s.m.DeleteLink(r.Context(), r.PathValue("id")); err != nil {
		writeManagerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) deployLink(w http.ResponseWriter, r *http.Request) {
	if err := s.m.DeployLink(r.Context(), r.PathValue("id")); err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deployed"})
}

func (s *server) linkScript(w http.ResponseWriter, r *http.Request) {
	script, err := s.m.RenderLinkScript(r.PathValue("id"))
	if err != nil {
		writeManagerError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, script)
}

// --- Health ---

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	stats := s.m.LatencyStats()
	if stats == nil {
		stats = []app.LatencyStats{}
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *server) probeNow(w http.ResponseWriter, r *http.Request) {
	stats := s.m.ProbeNow(r.Context())
	if stats == nil {
		stats = []app.LatencyStats{}
	}
	writeJSON(w, http.StatusOK, stats)
}

// --- Settings ---

type mqttRequest struct {
	Settings app.MQTTSettings `json:"settings"`
	Devices  []string         `json:"devices"`
}

func (s *server) applyMQTT(w http.ResponseWriter, r *http.Request) {
	var req mqttRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Settings.Enable && strings.TrimSpace(req.Settings.Server) == "" {
		writeError(w, http.StatusBadRequest, "settings.server is required when enabling MQTT")
		return
	}
	if err := s.m.ApplyMQTT(r.Context(), req.Settings, req.Devices); err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "applied"})
}

type bleRequest struct {
	Settings app.BLESettings `json:"settings"`
	Devices  []string        `json:"devices"`
}

func (s *server) applyBLE(w http.ResponseWriter, r *http.Request) {
	var req bleRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.m.ApplyBLE(r.Context(), req.Settings, req.Devices); err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "applied"})
}

type extenderRequest struct {
	Device string `json:"device"`
	Enable bool   `json:"enable"`
}

func (s *server) rangeExtender(w http.ResponseWriter, r *http.Request) {
	var req extenderRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Device) == "" {
		writeError(w, http.StatusBadRequest, "device is required")
		return
	}
	if err := s.m.EnableRangeExtender(r.Context(), req.Device, req.Enable); err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "applied"})
}

type joinRequest struct {
	Edge     string `json:"edge"`
	Extender string `json:"extender"`
}

func (s *server) joinExtender(w http.ResponseWriter, r *http.Request) {
	var req joinRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	switch {
	case strings.TrimSpace(req.Edge) == "":
		writeError(w, http.StatusBadRequest, "edge is required")
		return
	case strings.TrimSpace(req.Extender) == "":
		writeError(w, http.StatusBadRequest, "extender is required")
		return
	case req.Edge == req.Extender:
		writeError(w, http.StatusBadRequest, "edge and extender must be different devices")
		return
	}
	if err := s.m.JoinExtender(r.Context(), req.Edge, req.Extender); err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "joined"})
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeManagerError maps a Manager error onto an HTTP status: lookup
// failures ("not found", "unknown", "no such") become 404, everything else
// is a 500.
func writeManagerError(w http.ResponseWriter, err error) {
	msg := strings.ToLower(err.Error())
	status := http.StatusInternalServerError
	if strings.Contains(msg, "not found") ||
		strings.Contains(msg, "unknown") ||
		strings.Contains(msg, "no such") {
		status = http.StatusNotFound
	}
	writeError(w, status, err.Error())
}

// decodeJSON decodes the request body into dst, writing a 400 JSON error
// and returning false when the body is missing or malformed.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}
