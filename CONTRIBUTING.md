# Contributing

Thank you for your interest in the Privasys renderer.

## Layout

| Path | Purpose |
| --- | --- |
| [`cmd/browser`](cmd/browser) | The service. |
| [`internal/render`](internal/render) | The browser driver: journeys, steps, captures, text recognition. |
| [`internal/api`](internal/api) | The HTTP surface: probes, configure, one action. |
| [`testdata`](testdata) | A page for the smoke job to render, including text drawn into a canvas so recognition is exercised against something the document cannot answer. |

## Building and testing

```bash
go build ./...
go test ./...
docker build -t container-app-browser .
```

The unit tests need no browser. The smoke job in CI renders a real page
in the real image, which is the only test that proves a renderer
renders.

## The rule this service is built around

It holds nothing and decides nothing. A change that gives it a volume, a
cache outliving a call, a verdict about availability, or a second caller
is a change that undoes the reason it is a separate enclave. Thresholds,
baselines and what a screenshot means belong to the caller, which has to
prove its arithmetic afterwards.

Two specifics worth stating:

- **A `fill` value may be a credential.** It is typed into the page and
  must never appear in a result, a log line or an error.
- **The navigation policy is applied before the browser starts.** A
  refused journey should cost nothing; refusing after spawning Chromium
  would make the refusal itself a way to make this service do work.

## Licence

By contributing you agree that your contributions are licensed under the
AGPL-3.0.
