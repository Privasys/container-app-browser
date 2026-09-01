// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0. See LICENSE.

// Command browser renders scripted journeys in a headless browser,
// inside a confidential VM, for a caller that has verified its
// measurement.
//
// It exists so that the service holding the credentials and the
// availability record does not also have to hold a browser. A journey
// renders whatever the watched service returns, which on a bad day is
// whatever an attacker put there; doing that here, with no vault, no
// volume and no state between calls, keeps that page away from
// everything worth stealing.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Privasys/container-app-browser/internal/api"
	"github.com/Privasys/container-app-browser/internal/render"
)

var version = "dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(log); err != nil {
		log.Error("the renderer stopped", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	port, err := strconv.Atoi(os.Getenv("PORT"))
	if err != nil || port <= 0 || port > 65535 {
		return errors.New("PORT must be set to the port the platform assigned")
	}

	chromium := env("BROWSER_CHROMIUM", "/usr/bin/chromium-browser")
	if _, err := os.Stat(chromium); err != nil {
		if alt, lookErr := lookChromium(); lookErr == nil {
			chromium = alt
		} else {
			return fmt.Errorf("no browser executable at %s", chromium)
		}
	}

	renderer := &render.Renderer{
		Chromium:  chromium,
		UserAgent: env("BROWSER_USER_AGENT", "Privasys-Service-Monitoring/1.0 (+https://privasys.org)"),
		OCR:       render.Tesseract(env("BROWSER_TESSERACT", "")),
	}

	server := api.NewServer(log, renderer)
	server.Version = version
	server.Manifest = readManifest()
	server.OCRVersion = render.TesseractVersion(env("BROWSER_TESSERACT", ""))

	// A development instance may be configured from the environment.
	// The platform's container credentials switch it off, so it cannot
	// be reached inside an enclave.
	if token := os.Getenv("BROWSER_CALLER_TOKEN"); token != "" && !onPlatform() {
		if err := server.Apply(api.Configure{
			CallerToken:    token,
			AllowedDomains: os.Getenv("BROWSER_ALLOWED_DOMAINS"),
		}); err != nil {
			return err
		}
		log.Warn("configured from the environment; this is a development mode and is refused on the platform")
	}

	srv := &http.Server{
		Addr:              net.JoinHostPort("", strconv.Itoa(port)),
		Handler:           server.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		// A journey may legitimately take a couple of minutes.
		WriteTimeout: 5 * time.Minute,
	}
	log.Info("listening", "port", port, "version", version,
		"chromium", chromium, "ocr", server.OCRVersion)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func onPlatform() bool {
	return os.Getenv("PRIVASYS_MANAGER_URL") != "" && os.Getenv("PRIVASYS_CONTAINER_TOKEN") != ""
}

func lookChromium() (string, error) {
	for _, candidate := range []string{
		"/usr/bin/chromium-browser", "/usr/bin/chromium", "/usr/bin/google-chrome",
	} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", errors.New("no browser executable found")
}

func readManifest() []byte {
	for _, path := range []string{"/privasys.json", "privasys.json"} {
		if raw, err := os.ReadFile(path); err == nil {
			var probe any
			if json.Unmarshal(raw, &probe) == nil {
				return raw
			}
		}
	}
	return nil
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
