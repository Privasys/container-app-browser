// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0. See LICENSE.

// Package api is the renderer's HTTP surface.
//
// It is small on purpose. This service renders a page and reports what
// happened; it stores nothing, decides nothing, and has exactly one
// caller. The surface reflects that: a probe, a manifest, a configure
// call, and one action.
package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Privasys/container-app-browser/internal/render"
)

// Server is the HTTP surface.
type Server struct {
	log      *slog.Logger
	renderer *render.Renderer

	mu sync.RWMutex
	// callerToken is the shared secret the one caller presents. It
	// arrives through the attested configure call and is held in memory
	// only: this service has no volume and keeps nothing across a
	// restart but its own measurement.
	callerToken string
	allowed     []string
	configured  bool

	Version  string
	Manifest []byte
	// OCRVersion names the recogniser in this image, so a caller can
	// record what produced a piece of text alongside the text.
	OCRVersion string
}

// NewServer builds the surface.
func NewServer(log *slog.Logger, renderer *render.Renderer) *Server {
	s := &Server{log: log, renderer: renderer}
	renderer.Allowed = s.allow
	return s
}

// Configure applies the configure document.
type Configure struct {
	// CallerToken is the secret the monitor presents on every call.
	// Attestation tells the monitor which build it is talking to; this
	// tells the renderer which caller is talking to it.
	CallerToken string `json:"caller_token"`
	// AllowedDomains restricts where a journey may navigate. The caller
	// keeps its own allowlist; this one exists so a renderer cannot be
	// pointed anywhere by whoever holds the token.
	AllowedDomains string `json:"allowed_domains,omitempty"`
}

// Apply installs a configuration.
func (s *Server) Apply(c Configure) error {
	if strings.TrimSpace(c.CallerToken) == "" {
		return fmt.Errorf("configure: a caller token is required")
	}
	if len(c.CallerToken) < 32 {
		return fmt.Errorf("configure: the caller token is too short to be a secret")
	}
	var allowed []string
	for _, d := range strings.Split(c.AllowedDomains, ",") {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			allowed = append(allowed, d)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.callerToken, s.allowed, s.configured = c.CallerToken, allowed, true
	return nil
}

// Configured reports whether a configuration has been applied.
func (s *Server) Configured() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.configured
}

// allow implements the renderer's navigation policy.
func (s *Server) allow(host string) error {
	s.mu.RLock()
	allowed := s.allowed
	s.mu.RUnlock()
	if len(allowed) == 0 {
		return nil
	}
	host = strings.ToLower(host)
	for _, d := range allowed {
		if d == "*" || host == d || strings.HasSuffix(host, "."+d) {
			return nil
		}
	}
	return fmt.Errorf("render: %s is not an allowed domain for this renderer", host)
}

// Handler builds the router.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /readiness", s.readiness)
	mux.HandleFunc("GET /version", s.version)
	mux.HandleFunc("GET /privasys.json", s.manifest)
	mux.HandleFunc("GET /.well-known/privasys-manifest", s.manifest)
	mux.HandleFunc("POST /configure", s.configure)
	mux.HandleFunc("POST /render", s.render)
	mux.HandleFunc("POST /tools/render", s.render)
	return logging(s.log, mux)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "version": s.Version, "configured": s.Configured(),
	})
}

func (s *Server) readiness(w http.ResponseWriter, _ *http.Request) {
	if !s.Configured() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "awaiting configuration"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": "ready"})
}

func (s *Server) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version": s.Version, "ocr": s.OCRVersion,
	})
}

func (s *Server) manifest(w http.ResponseWriter, _ *http.Request) {
	if len(s.Manifest) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no manifest in this image"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(s.Manifest)
}

func (s *Server) configure(w http.ResponseWriter, r *http.Request) {
	var c Configure
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&c); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "configuration refused",
			"detail": err.Error()})
		return
	}
	if err := s.Apply(c); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "configuration refused",
			"detail": err.Error()})
		return
	}
	s.mu.RLock()
	allowed := s.allowed
	s.mu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"configured": true, "allowed_domains": allowed, "ocr": s.OCRVersion,
	})
}

func (s *Server) render(w http.ResponseWriter, r *http.Request) {
	if !s.Configured() {
		writeJSON(w, http.StatusServiceUnavailable,
			map[string]any{"error": "this renderer has not been configured yet"})
		return
	}
	if !s.authorised(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "not authorised"})
		return
	}
	var journey render.Journey
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&journey); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad journey", "detail": err.Error()})
		return
	}
	result := s.renderer.Run(r.Context(), journey)
	writeJSON(w, http.StatusOK, result)
}

// authorised compares the presented token in constant time. The token
// is the only thing standing between this renderer and anyone who can
// reach it, so a comparison that leaks its length by timing is not good
// enough.
func (s *Server) authorised(r *http.Request) bool {
	s.mu.RLock()
	want := s.callerToken
	s.mu.RUnlock()
	if want == "" {
		return false
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return subtleCompare(got, want)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(body)
}

// logging records one line per request. Bodies are never logged: a
// journey body carries the credential the caller is trusting this
// service with for the length of one call.
func logging(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if r.URL.Path == "/health" && rec.status == http.StatusOK {
			return
		}
		log.Info("request", "method", r.Method, "path", r.URL.Path,
			"status", rec.status, "duration_ms", time.Since(started).Milliseconds())
	})
}

type recorder struct {
	http.ResponseWriter
	status int
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
