// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0. See LICENSE.

package api_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Privasys/container-app-browser/internal/api"
	"github.com/Privasys/container-app-browser/internal/render"
)

func newServer(t *testing.T) (*api.Server, http.Handler) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := api.NewServer(log, &render.Renderer{Chromium: "/nonexistent"})
	return s, s.Handler()
}

func TestEverythingIsRefusedBeforeConfiguration(t *testing.T) {
	_, handler := newServer(t)

	for _, path := range []string{"/render", "/tools/render"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"steps":[]}`))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s before configuration = %d, want 503", path, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readiness", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness before configuration = %d, want 503", rec.Code)
	}
}

func TestAWeakCallerTokenIsRefused(t *testing.T) {
	s, _ := newServer(t)
	if err := s.Apply(api.Configure{CallerToken: "short"}); err == nil {
		t.Fatal("a five character token was accepted as a secret")
	}
	if err := s.Apply(api.Configure{}); err == nil {
		t.Fatal("an empty token was accepted")
	}
}

func TestRenderNeedsTheCallerToken(t *testing.T) {
	s, handler := newServer(t)
	token := strings.Repeat("k", 48)
	if err := s.Apply(api.Configure{CallerToken: token}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/render", strings.NewReader(`{"steps":[]}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("an unauthenticated render = %d, want 401", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/render", strings.NewReader(`{"steps":[]}`))
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("j", 48))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong token = %d, want 401", rec.Code)
	}

	// With the right token the request is accepted and fails on its own
	// merits, which here is an empty journey.
	req = httptest.NewRequest(http.MethodPost, "/render", strings.NewReader(`{"steps":[]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("an authorised render = %d, want 200", rec.Code)
	}
	var result render.Result
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.OK || !strings.Contains(result.Error, "at least one step") {
		t.Fatalf("result = %+v", result)
	}
}

// A renderer shared by more than one caller must not be pointable
// anywhere by any of them.
func TestTheDomainAllowlistIsEnforced(t *testing.T) {
	s, handler := newServer(t)
	token := strings.Repeat("k", 48)
	if err := s.Apply(api.Configure{
		CallerToken: token, AllowedDomains: "example.com, api.other.test",
	}); err != nil {
		t.Fatal(err)
	}

	body := `{"steps":[{"name":"go","kind":"goto","url":"https://attacker.test/collect"}]}`
	req := httptest.NewRequest(http.MethodPost, "/render", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var result render.Result
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.OK {
		t.Fatal("a journey navigated to a domain outside the allowlist")
	}
	if !strings.Contains(result.Error, "not an allowed domain") {
		t.Fatalf("error = %q", result.Error)
	}
}

func TestConfigurationDoesNotEchoTheToken(t *testing.T) {
	_, handler := newServer(t)
	token := strings.Repeat("k", 48)
	body, _ := json.Marshal(api.Configure{CallerToken: token, AllowedDomains: "example.com"})
	req := httptest.NewRequest(http.MethodPost, "/configure", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("configure = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), token) {
		t.Fatalf("the caller token was echoed back: %s", rec.Body.String())
	}
}
