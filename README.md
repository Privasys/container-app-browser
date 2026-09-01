# container-app-browser

A headless browser as an attested service, running as a confidential
container on Privasys
[`enclave-os-virtual`](https://docs.privasys.org/solutions/enclave-os/).

It renders a scripted journey and reports what the page did. It reaches
no conclusion about whether anything is up, keeps nothing between calls,
and has exactly one caller: the service that verified its measurement
before sending it a credential.

## Why it is a separate enclave

It exists because of
[container-app-service-monitoring](https://github.com/Privasys/container-app-service-monitoring),
which holds a customer's credentials and an availability record that has
to stay provable. That service should not also be parsing whatever a
watched page returns, because on a bad day the watched page is whatever
an attacker put there.

So the browser lives here instead: its own measurement, no vault, no
volume, no state that survives a call. The caller pins this image's
digest at OID `1.3.6.1.4.1.65230.3.2` and hands over a credential for
the length of one render, knowing which build it is talking to.

## What a journey looks like

```json
{
  "width": 1280, "height": 800, "timeout_ms": 60000,
  "steps": [
    { "name": "open",      "kind": "goto",  "url": "https://app.example.com/login" },
    { "name": "wait",      "kind": "wait",  "selector": "#email", "wait_visible": true },
    { "name": "email",     "kind": "fill",  "selector": "#email",    "value": "monitor@example.com" },
    { "name": "password",  "kind": "fill",  "selector": "#password", "value": "the credential" },
    { "name": "sign in",   "kind": "click", "selector": "button[type=submit]" },
    { "name": "dashboard", "kind": "wait",  "selector": ".dashboard", "wait_visible": true },
    { "name": "read it",   "kind": "text",  "screenshot": true }
  ]
}
```

Step kinds are `goto`, `click`, `fill`, `press`, `wait`, `sleep`,
`text`, `screenshot` and `eval`. Any step can also set `text` or
`screenshot` to capture after it runs.

The result carries, per step: whether it worked, how long it took, a
readable reason if it did not, the document's rendered text where asked,
a PNG where asked, and what an `eval` returned. Alongside them the
journey reports the page's console errors and the subresources that
failed to load, because a page that answers 200 with no stylesheet is a
broken page and that is where it says so.

## Screenshots, text, and which to use

Assert on the document's text wherever you can: it is exact, free, and
tells you what the page actually says. A screenshot is for the questions
text cannot answer, such as whether anything rendered at all and whether
it still looks like it did last week, and those are best answered by the
caller with a perceptual hash against an approved baseline rather than
by a person or a model.

Text recognition (`"ocr": true`) is for words a page draws rather than
writes: a canvas, a chart, an error baked into an image. It costs about
a tenth of a second. The recogniser is a fixed version inside this
measured image, so the same screenshot yields the same text, and a
caller that records both can have the result re-derived by anyone
holding the image.

## Configuration

The platform holds every endpoint at HTTP 503 until the configure call
succeeds:

```bash
privasys apps configure <app-id> \
  --set caller_token="$(head -c 32 /dev/urandom | base64)" \
  --set allowed_domains=app.example.com
```

`caller_token` is the shared secret its one caller presents. Attestation
tells the caller which build it is talking to; the token tells this
service which caller is talking to it. It is held in memory only, and
never echoed by any endpoint.

`allowed_domains` bounds where a journey may navigate. The caller keeps
its own allowlist; this one exists so a renderer cannot be pointed
anywhere by whoever holds the token.

## Running it locally

```bash
docker build -t container-app-browser .
docker run --rm --network host \
  -e PORT=8081 -e BROWSER_CALLER_TOKEN="$(head -c 32 /dev/urandom | base64)" \
  container-app-browser
```

Configuring from the environment is a development mode: the platform's
container credentials switch it off, so it cannot be reached inside an
enclave.

## Limits

- **One caller, one token.** There is no per-caller identity here. A
  deployment shared between tenants would be one where a tenant's token
  renders another tenant's pages; run one per tenant.
- **A screenshot cannot be redacted.** The caller decides which steps
  capture one, and should not capture the step that types a credential.
  Password fields are masked by the browser; a token displayed in the
  page is not.
- **Attestation is deterministic, not challenge-response.** A caller
  verifies a quote bound to a certificate renewed every 24 hours.
  Freshness at the connection level needs the forked TLS stack and is a
  later upgrade.
- Chromium is a large attack surface. That is the reason this service
  exists as its own enclave rather than as a library inside the caller.

## Licence

AGPL-3.0. See [LICENSE](LICENSE).
