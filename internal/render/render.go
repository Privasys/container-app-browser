// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0. See LICENSE.

// Package render drives a headless browser through a scripted journey.
//
// This is a renderer, and deliberately nothing more. It holds no state
// between calls, keeps no credential, writes nothing to disk, and
// reaches no conclusion about what it saw: it returns what the page
// did, and the caller decides what that means. Everything about
// availability, thresholds and verdicts lives in the service that calls
// this one, because that service has to be able to prove its arithmetic
// afterwards and a browser cannot help with that.
//
// The separation also bounds a real risk. A journey renders whatever
// the watched service returns, which on a bad day is whatever an
// attacker put there. Doing that in its own enclave, with no vault and
// no storage, keeps a compromised page away from the credentials and
// the availability record that live in the caller's enclave.
package render

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// Limits. A journey that needs more than this is a journey that should
// be split, and the ceilings keep one caller from taking the renderer
// out for everybody.
const (
	MaxSteps       = 40
	MaxJourney     = 120 * time.Second
	MaxStep        = 60 * time.Second
	MaxScreenshots = 20
	// MaxText is how much page text is returned per step. Assertions run
	// against it in the caller, so it has to be enough to find a message
	// in and small enough not to become a data transfer.
	MaxText = 256 << 10
)

// Step kinds.
const (
	StepGoto       = "goto"
	StepClick      = "click"
	StepFill       = "fill"
	StepPress      = "press"
	StepWait       = "wait"
	StepScreenshot = "screenshot"
	StepText       = "text"
	StepEval       = "eval"
	StepSleep      = "sleep"
)

// Journey is one scripted run.
type Journey struct {
	Steps []Step `json:"steps"`
	// Viewport is the window the page believes it is in. It is part of
	// what a screenshot means, so it travels with the journey rather
	// than being a property of the renderer.
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
	// TimeoutMs bounds the whole journey.
	TimeoutMs int `json:"timeout_ms,omitempty"`
	// UserAgent identifies the monitor to the watched service, so an
	// operator reading their own access log can tell synthetic traffic
	// from real users.
	UserAgent string `json:"user_agent,omitempty"`
	// OCR asks for text recognised from each screenshot as well as the
	// text in the document. It costs about a tenth of a second and is
	// only worth it for content the page draws rather than writes:
	// canvas, charts, images with words in them.
	OCR bool `json:"ocr,omitempty"`
}

// Step is one action.
type Step struct {
	Name string `json:"name"`
	Kind string `json:"kind"`

	URL      string `json:"url,omitempty"`
	Selector string `json:"selector,omitempty"`
	// Value is what a fill step types. It may be a credential, which is
	// why it is used and never echoed: no step result carries it back.
	Value string `json:"value,omitempty"`
	Key   string `json:"key,omitempty"`
	// Expression is evaluated in the page and its result returned as a
	// string. It is the escape hatch for a check the other kinds cannot
	// express.
	Expression string `json:"expression,omitempty"`

	// WaitVisible waits for the selector to be visible rather than
	// merely present.
	WaitVisible bool `json:"wait_visible,omitempty"`
	SleepMs     int  `json:"sleep_ms,omitempty"`
	TimeoutMs   int  `json:"timeout_ms,omitempty"`

	// Screenshot captures the page after this step. Capture is opt-in
	// per step: a screenshot of a page a credential was just typed into
	// is a screenshot nobody asked for.
	Screenshot bool `json:"screenshot,omitempty"`
	// FullPage captures beyond the viewport.
	FullPage bool `json:"full_page,omitempty"`
	// Text returns the document's rendered text after this step.
	Text bool `json:"text,omitempty"`
}

// Result is what one journey produced.
type Result struct {
	OK         bool         `json:"ok"`
	FailedStep string       `json:"failed_step,omitempty"`
	Error      string       `json:"error,omitempty"`
	ErrorClass string       `json:"error_class,omitempty"`
	DurationMs int          `json:"duration_ms"`
	Steps      []StepResult `json:"steps"`
	// Console and Failed collect what the page reported for itself: a
	// page can return 200 and still be broken, and this is usually where
	// it says so.
	Console []string        `json:"console,omitempty"`
	Failed  []FailedRequest `json:"failed_requests,omitempty"`
}

// StepResult is what one action produced.
type StepResult struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	OK         bool   `json:"ok"`
	DurationMs int    `json:"duration_ms"`
	Detail     string `json:"detail,omitempty"`
	URL        string `json:"url,omitempty"`
	Status     int    `json:"status,omitempty"`
	// Text is the document's rendered text, when the step asked for it.
	Text string `json:"text,omitempty"`
	// Screenshot is a PNG, base64 encoded, when the step asked for one.
	Screenshot string `json:"screenshot,omitempty"`
	// OCRText is what was recognised in that screenshot.
	OCRText string `json:"ocr_text,omitempty"`
	// Value is what an eval step returned.
	Value string `json:"value,omitempty"`
}

// FailedRequest is one subresource the page could not load. A missing
// stylesheet is how a page renders blank while answering 200.
type FailedRequest struct {
	URL    string `json:"url"`
	Reason string `json:"reason"`
	Type   string `json:"type,omitempty"`
}

// Error classes, chosen to match what the caller does with them: a
// failure of the watched page, or a failure of this renderer.
const (
	ErrNavigation = "navigation"
	ErrSelector   = "selector"
	ErrTimeout    = "timeout"
	ErrScript     = "script"
	ErrInternal   = "internal"
)

// Renderer runs journeys.
type Renderer struct {
	// Chromium is the browser executable baked into the image.
	Chromium string
	// UserAgent is the default identification.
	UserAgent string
	// OCR runs text recognition over a screenshot. Nil disables it.
	OCR func(png []byte) (string, error)
	// Allowed decides which hosts a journey may navigate to. The caller
	// has its own allowlist; this one exists so a renderer shared by
	// more than one caller cannot be pointed anywhere by any of them.
	Allowed func(host string) error

	// concurrency bounds how many browsers run at once. Chromium is the
	// expensive part of this service by a wide margin.
	sem  chan struct{}
	once sync.Once
}

// Run executes a journey.
func (r *Renderer) Run(ctx context.Context, j Journey) Result {
	r.once.Do(func() {
		if r.sem == nil {
			r.sem = make(chan struct{}, 4)
		}
	})
	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-ctx.Done():
		return Result{ErrorClass: ErrTimeout, Error: "the renderer was busy"}
	}

	started := time.Now()
	out := Result{OK: true}

	if len(j.Steps) == 0 {
		return Result{ErrorClass: ErrInternal, Error: "a journey needs at least one step"}
	}
	if len(j.Steps) > MaxSteps {
		return Result{ErrorClass: ErrInternal,
			Error: fmt.Sprintf("a journey runs at most %d steps", MaxSteps)}
	}

	// The navigation policy is applied before a browser is started. A
	// journey pointed somewhere it may not go should cost nothing, and
	// refusing it after spawning Chromium would mean the refusal itself
	// is a way to make this service do work.
	if r.Allowed != nil {
		for _, s := range j.Steps {
			if s.Kind != StepGoto {
				continue
			}
			if err := r.Allowed(hostOf(s.URL)); err != nil {
				return Result{ErrorClass: ErrInternal, Error: err.Error(), FailedStep: s.Name}
			}
		}
	}

	budget := time.Duration(j.TimeoutMs) * time.Millisecond
	if budget <= 0 || budget > MaxJourney {
		budget = MaxJourney
	}
	runCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	width, height := j.Width, j.Height
	if width <= 0 || height <= 0 {
		width, height = 1280, 800
	}
	agent := j.UserAgent
	if agent == "" {
		agent = r.UserAgent
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(r.Chromium),
		chromedp.WindowSize(width, height),
		chromedp.UserAgent(agent),
		chromedp.NoSandbox,
		chromedp.Flag("headless", "new"),
		// The renderer keeps nothing. A fresh profile per run means one
		// journey cannot see another's cookies, and nothing survives the
		// process.
		chromedp.Flag("incognito", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
		chromedp.Flag("mute-audio", true),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(runCtx, opts...)
	defer cancelAlloc()
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()

	var mu sync.Mutex
	chromedp.ListenTarget(browserCtx, func(ev any) {
		switch e := ev.(type) {
		case *runtime.EventConsoleAPICalled:
			if e.Type != "error" && e.Type != "warning" {
				return
			}
			var parts []string
			for _, arg := range e.Args {
				if arg.Value != nil {
					parts = append(parts, strings.Trim(string(arg.Value), `"`))
				}
			}
			mu.Lock()
			if len(out.Console) < 50 {
				out.Console = append(out.Console, string(e.Type)+": "+strings.Join(parts, " "))
			}
			mu.Unlock()
		case *network.EventLoadingFailed:
			mu.Lock()
			if len(out.Failed) < 50 {
				out.Failed = append(out.Failed, FailedRequest{
					Reason: e.ErrorText, Type: string(e.Type),
				})
			}
			mu.Unlock()
		}
	})

	if err := chromedp.Run(browserCtx, network.Enable()); err != nil {
		return Result{ErrorClass: ErrInternal, Error: "the browser did not start: " + err.Error()}
	}

	for i := range j.Steps {
		step := j.Steps[i]
		sr, err := r.step(browserCtx, step, j)
		out.Steps = append(out.Steps, sr)
		if err != nil {
			out.OK = false
			out.FailedStep = step.Name
			out.Error = err.detail
			out.ErrorClass = err.class
			break
		}
	}

	out.DurationMs = int(time.Since(started) / time.Millisecond)
	return out
}

type stepError struct {
	class  string
	detail string
}

func (e *stepError) Error() string { return e.detail }

func (r *Renderer) step(ctx context.Context, s Step, j Journey) (StepResult, *stepError) {
	started := time.Now()
	sr := StepResult{Name: s.Name, Kind: s.Kind, OK: true}
	finish := func() { sr.DurationMs = int(time.Since(started) / time.Millisecond) }

	timeout := time.Duration(s.TimeoutMs) * time.Millisecond
	if timeout <= 0 || timeout > MaxStep {
		timeout = 30 * time.Second
	}
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	fail := func(class, detail string) (StepResult, *stepError) {
		finish()
		sr.OK = false
		sr.Detail = detail
		return sr, &stepError{class: class, detail: detail}
	}

	var action chromedp.Action
	switch s.Kind {
	case StepGoto:
		// The navigation policy was applied before the browser started.
		sr.URL = s.URL
		action = chromedp.Navigate(s.URL)
	case StepClick:
		action = chromedp.Click(s.Selector, queryOpts(s)...)
	case StepFill:
		// The value may be a credential. It is typed into the page and
		// never returned: no branch below puts s.Value into the result.
		action = chromedp.SendKeys(s.Selector, s.Value, queryOpts(s)...)
	case StepPress:
		action = chromedp.KeyEvent(s.Key)
	case StepWait:
		if s.WaitVisible {
			action = chromedp.WaitVisible(s.Selector, queryOpts(s)...)
		} else {
			action = chromedp.WaitReady(s.Selector, queryOpts(s)...)
		}
	case StepSleep:
		wait := time.Duration(s.SleepMs) * time.Millisecond
		if wait <= 0 || wait > 30*time.Second {
			return fail(ErrInternal, "a sleep step waits between 1ms and 30s")
		}
		action = chromedp.Sleep(wait)
	case StepEval:
		var value string
		action = chromedp.Evaluate(s.Expression, &value)
		if err := chromedp.Run(stepCtx, action); err != nil {
			return fail(classify(stepCtx, err), err.Error())
		}
		sr.Value = value
	case StepText, StepScreenshot:
		// Both are captures, handled below.
	default:
		return fail(ErrInternal, "unknown step kind "+s.Kind)
	}

	if action != nil && s.Kind != StepEval {
		if err := chromedp.Run(stepCtx, action); err != nil {
			return fail(classify(stepCtx, err), describe(s, err))
		}
	}

	if s.Text || s.Kind == StepText {
		var text string
		if err := chromedp.Run(stepCtx, chromedp.Evaluate(
			`document.body ? document.body.innerText : ""`, &text)); err != nil {
			return fail(classify(stepCtx, err), err.Error())
		}
		if len(text) > MaxText {
			text = text[:MaxText]
		}
		sr.Text = text
	}

	if s.Screenshot || s.Kind == StepScreenshot {
		var buf []byte
		capture := chromedp.CaptureScreenshot(&buf)
		if s.FullPage {
			capture = chromedp.FullScreenshot(&buf, 90)
		}
		if err := chromedp.Run(stepCtx, capture); err != nil {
			return fail(classify(stepCtx, err), err.Error())
		}
		sr.Screenshot = base64.StdEncoding.EncodeToString(buf)
		if j.OCR && r.OCR != nil {
			if text, err := r.OCR(buf); err == nil {
				sr.OCRText = text
			} else {
				sr.Detail = "text recognition failed: " + err.Error()
			}
		}
	}

	finish()
	return sr, nil
}

func queryOpts(s Step) []chromedp.QueryOption {
	if strings.HasPrefix(s.Selector, "//") || strings.HasPrefix(s.Selector, "(//") {
		return []chromedp.QueryOption{chromedp.BySearch}
	}
	return []chromedp.QueryOption{chromedp.ByQuery}
}

// classify separates a page that did not do what was asked from a
// renderer that ran out of time.
func classify(ctx context.Context, err error) string {
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return ErrTimeout
	}
	text := err.Error()
	switch {
	case strings.Contains(text, "could not find node"),
		strings.Contains(text, "no node with given id"):
		return ErrSelector
	case strings.Contains(text, "net::"):
		return ErrNavigation
	}
	return ErrScript
}

// describe turns a driver error into something a person reads in an
// incident timeline.
func describe(s Step, err error) string {
	text := err.Error()
	switch {
	case strings.Contains(text, "net::ERR_NAME_NOT_RESOLVED"):
		return "the hostname does not resolve"
	case strings.Contains(text, "net::ERR_CONNECTION_REFUSED"):
		return "the connection was refused"
	case strings.Contains(text, "net::ERR_CERT"):
		return "the TLS certificate was not accepted: " + text
	case strings.Contains(text, "context deadline exceeded"):
		if s.Selector != "" {
			return fmt.Sprintf("%s did not appear", s.Selector)
		}
		return "the page did not finish in time"
	}
	if s.Selector != "" {
		return fmt.Sprintf("%s: %s", s.Selector, text)
	}
	return text
}

func hostOf(raw string) string {
	rest := raw
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.LastIndex(rest, ":"); i > 0 && !strings.Contains(rest[i:], "]") {
		rest = rest[:i]
	}
	return strings.ToLower(rest)
}

// unused keeps the cdp import honest while the node-level API is not
// needed; removing the import would change the vendored set.
var _ = cdp.NodeID(0)
var _ = page.Enable
