# Security notes

Living document. Updated at every security checkpoint (see the log at the end).

## Assets
- Parents' health information: transcripts, reports, medicines, alerts, private notes.
- Phone numbers of parents, family members, local contacts and admins.
- The ability to phone an elderly person or message their family.
- Encryption keys, vendor secrets, admin credentials.

## Trust boundaries and controls

| Boundary | Threat | Control |
| --- | --- | --- |
| Voice platform -> `/v1/webhooks/voice/{provider}`, `/v1/voice/tools/{tool}` | Forged or replayed events, oversized bodies | Provider signature checked before parsing (fake: HMAC-SHA256; fails closed with no secret); 1 MB cap; dedupe on `(provider, event_id)`; unknown calls answered 404 and not recorded; our call id and the provider id must agree |
| Meta -> `/v1/webhooks/whatsapp` | Forged acknowledgements, spam | `X-Hub-Signature-256` HMAC over the raw body, constant-time compare, empty secret rejects; verify token compared in constant time; "I'm on it" only counts from a member of the alert's own family; other inbound text stored encrypted, never acted on |
| Call content -> agent prompt and extraction | Prompt injection ("ignore your rules") | Transcript fenced as data; text cannot forge the closing marker or a speaker line; follow-ups and names sanitised (no newlines, braces, backticks; capped); model output strictly validated in Go; alerts come from rules plus a keyword detector the model cannot switch off (`prompt_injection` scenario) |
| System -> parent's phone | Calling without consent, outside hours, in non-compliant setups | Guards re-checked at dial time: parent and account status, three consents, window, `CALLS_ENABLED`, `DEV_ALLOWLIST` outside prod, `TELECOM_COMPLIANCE_ACK` in prod; attempt marked `dialing` before the vendor call so a retry never re-dials; fake vendors refused in prod |
| System -> family WhatsApp | Leaking private notes or health data to the wrong person | Private-note check at extraction and again at send time (whole note or any 3-word run); fallback summary never reads private notes; someone-else calls keep no health data; recipients re-checked at send time (same family, opted in); local contact and admins never get quotes; every send gated and recorded |
| Database | Data exposure at rest, misuse of copied ciphertext | AES-256-GCM per column with key id, column name bound as associated data; keys from the environment; key rotation supported |
| Logs and job errors | Personal data in logs | Bodies never logged; phone numbers masked; vendor errors carry codes, not parameters; validation errors echo at most 20 characters of a bad value; job payloads hold IDs only |
| Any client -> HTTP server | Floods, clickjacking, sniffing | Per-instance in-flight limit (503 + Retry-After), read/write/idle timeouts, panic recovery without echo, security headers (CSP, frame-ancestors none, nosniff, no-referrer, no-store, HSTS behind TLS) |
| Workers | Double processing, lost jobs | `FOR UPDATE SKIP LOCKED`; fenced completion (a reaped worker cannot finish a job); reaper; exhausted jobs fail instead of looping; safety jobs dead-letter to admins; alerts claimed before summaries |

## Scaling notes
- `serve` is stateless: run several instances behind a load balancer. The in-flight limit is per instance.
- `worker` can run as several processes; claims, slot creation and alert escalation are safe under concurrency (tested with two workers x 8 loops and 4 concurrent schedulers).
- Pool size: set `pool_max_conns` in `DATABASE_URL`; keep `WORKER_CONCURRENCY` plus expected HTTP concurrency under it.

## Known gaps (tracked)
- Admin authentication, CSRF and rate limiting arrive with the admin pages (M7).
- Vendor signature schemes for the real voice platform arrive with its adapter (M8).
- Recording deletion at the voice platform and the retention job arrive in M9.
- `govulncheck` cannot reach vuln.go.dev from the build sandbox; it runs in CI.

## Checkpoint log
- 2026-10-06, after M6: reviewed webhooks, logging, prompt injection paths, outbound privacy, job fencing. Added security headers, in-flight limit, capped validation echoes, CI jobs for race-enabled integration tests, govulncheck and sqlc drift.
