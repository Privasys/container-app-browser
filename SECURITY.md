# Security Policy

## Reporting a vulnerability

Please report suspected vulnerabilities privately to
**security@privasys.org**. Do not open public issues for security
reports. Include the affected commit or image digest, the journey that
reproduces it, and an impact assessment.

## Scope

This service makes few claims, and they are the ones to attack.

- **It keeps nothing.** A path by which one journey observes another's
  cookies, storage, page state or credential; anything written to disk;
  anything that survives the process.
- **It echoes no credential.** A step result, log line, error message or
  manifest response that carries a `fill` value or the caller token.
- **It renders only where it was allowed.** A journey that navigates
  outside the configured domains, whether by redirect, by an `eval` that
  navigates, or by a URL whose host parsing differs from the browser's.
- **It serves only its caller.** A render accepted without the token, or
  a token comparison that leaks its contents by timing.

## Out of scope

- The content of pages the caller asks it to render.
- Resource exhaustion by the authorised caller.
- Chromium vulnerabilities themselves; report those upstream. That this
  service is a separate enclave with no vault and no volume is the
  mitigation, and reports that the isolation is weaker than claimed are
  very much in scope.

## Known limits

- Attestation is deterministic rather than challenge-response: the quote
  binds a certificate renewed every 24 hours, not the connection.
- A screenshot cannot be redacted after the fact. Capture is opt-in per
  step so a caller can avoid photographing a page it has just typed a
  credential into.
