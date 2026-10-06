# Progress

## M0: skeleton (2026-10-07)
- Go module `github.com/EklavyaGoyal17/haalchaal`, `cmd/haalchaal` with `serve` (worker, migrate, simcall are stubs until their milestones).
- `internal/config`: every SPEC §12 variable; `CALLS_ENABLED` and `TELECOM_COMPLIANCE_ACK` fail closed (unparseable → false with a warning); secrets required outside dev; config logs never show secrets or full numbers.
- `internal/logging`: slog JSON, request ids, shared `MaskPhone`. `internal/domain.ValidE164`. `internal/clock` real and fake.
- `internal/httpapi`: `GET /healthz`, `GET /readyz` (database ping; migration check comes in M1), request-id and panic-recovery middleware.
- Docker Compose Postgres 16 on 127.0.0.1:55432 (5432 is taken by a local Postgres on the founder's PC) with a named volume; Makefile; `.env.example`; GitHub Actions running vet and tests.
- Dependency: `pgx/v5` (Postgres driver, stack decision).

## M1: data layer (2026-10-07)
- `migrations/00001_init.sql`: every SPEC §4 table and index, plus E.164, call-window and priority checks; full Down. Embedded and run by `haalchaal migrate up|down|status` (goose as a library).
- `queries/` + `internal/db` (sqlc, pgx/v5, `google/uuid`): accounts, family members, parents, medicines, consents, audit log.
- `internal/crypto`: AES-256-GCM keyring, `key_id || nonce || ciphertext`, column name as associated data, rotation (`NeedsRotation`, `Rotate`), `make genkey`. Config parses `ENCRYPTION_KEYS` through it.
- `/readyz` now also returns 503 while migrations are pending. `make dev` applies migrations first.
- Integration tests (`make test-db test-integration`): up, down, up; constraint checks; encrypted column round trip.
- Dependencies: `pressly/goose/v3` (migrations, stack decision), `google/uuid` (UUID type for sqlc). goose raised the `go` directive to 1.26.

## M2: jobs and scheduler (2026-10-06)
- `internal/jobs`: Postgres queue. `Enqueue` (inside the caller's transaction, `ON CONFLICT (dedupe_key) DO NOTHING`), claim with `FOR UPDATE SKIP LOCKED` limited to kinds the worker handles, backoff 30s*2^attempts capped at 30m, `Permanent` errors, panic recovery, per-job deadline, fenced complete/retry/fail (a reaped worker cannot finish a job), reaper (5 minutes; exhausted jobs fail instead of looping), dead-letter hook, per-parent cancellation. Payloads are a typed struct of IDs only.
- `internal/scheduler`: plan days (`daily`, `basic` = Mon/Wed/Fri), wall-clock windows in the parent's timezone (DST-safe), `Tick` creating slot + attempt 1 + `place_call:<call_id>` in one transaction, only for the inserting transaction. Skipped days logged once. Safe across processes.
- `internal/safety`: the outbound gate (`CALLS_ENABLED`, `DEV_ALLOWLIST` outside prod, `TELECOM_COMPLIANCE_ACK` for prod dialling), fail closed.
- `internal/calls.CheckGuards`: every dial-time guard, returning an `end_reason`.
- `haalchaal worker` (scheduler + workers, refuses to start with pending migrations) and `haalchaal dev` (serve + worker, used by `make dev`). `WORKER_CONCURRENCY`.
- `migrations/00002`: partial indexes for the reaper and per-parent cancellation.
- Integration: 300 jobs raced by two workers x 8 loops are each claimed exactly once; 50 concurrent ticks give one slot per parent per day; 200 parents across 4 schedulers; every scheduling rule.

## M3: calls with the fake voice provider (2026-10-06)
- `internal/voice`: SPEC §6 interface; `voice/fake` records calls and uses signed JSON webhooks (HMAC-SHA256, fails closed without a secret) so real handlers run unchanged.
- `internal/calls`: state machine (forward-only, out-of-order and duplicate events ignored), retry policy (RETRY_OFFSETS, never past window_end or the slot date), `place_call` (re-checks guards; marks `dialing` before StartCall so a re-run never dials twice; StartCall failure fails the attempt, no silent re-dial), event application in one transaction with `(provider, event_id)` dedupe, encrypted transcripts + `process_call` job, stale-attempt sweeper (30 min), mid-call tools (unknown severity -> emergency, self_harm always emergency), `PauseParent` (cancels scheduled attempts and place_call jobs only; alert and processing jobs survive).
- Call context rendering from `prompts/agent_system.tmpl` (embedded); follow-ups and names are sanitised so call content cannot restructure the prompt.
- `internal/alerts` (create/merge per call+category, never downgrade an emergency, missed_calls per slot, escalation job enqueued), `internal/audit` writer.
- HTTP: `POST /v1/webhooks/voice/{provider}`, `POST /v1/voice/tools/{tool}`; 1 MB body cap; 401 on bad signature; bodies never logged.
- `internal/app` wiring; `internal/sim` + `haalchaal simcall <scenario>` replay scenario files through the real handlers with a fake clock, then replay every request and check nothing changes.
- Config: fake vendors are refused in prod.
- `make simcall SCENARIO=no_answer`: 3 attempts (10:00, +15m, +45m), slot missed, one missed_calls alert, replay unchanged.

## M4: extraction (2026-10-06)
- `prompts/extract.tmpl` (transcript fenced as data; text cannot forge the closing marker or a speaker line), `prompts/report.schema.json` (JSON Schema, test-checked against the Go enums), `prompts/summary_fallback.tmpl` (English and Hindi, one line, never sees private notes).
- `internal/extract`: Report v1, strict `Parse` (unknown fields, enum values, trailing data rejected; lists and lengths capped), `NeedsReview`, `ModelExtractor` (retry once with the validation error; then `InvalidOutputError` with the raw output), `LeaksPrivate` (whole note or any 3-word run, after normalising) and `SafeSummary`.
- `internal/extract/fake`: deterministic rule-based extractor (Hindi romanised and Devanagari, English, Tamil vocabulary). A scam needs a pressure element; "police" alone is not one.
- `internal/alerts/keywords.yaml` + detector (Latin phrases on word boundaries, other scripts as substrings; parent turns only). `internal/alerts/rules`: red flags by severity (self_harm always emergency), keyword-only hits still alert, scam signals collapse to one alert, unconfirmed scam keyword -> admin-only `scam_keyword`, stop, distress, same medicine missed twice, low mood / poor sleep three in a row (watch).
- `process_call`: decrypt transcript, extract, merge keyword flags into the stored report, strip health data when someone else answered (safety findings kept; transcript deleted when there are none), encrypted `call_reports`, alerts, pause on stop, follow-ups as memories (FOLLOW_UP_TTL), first call done + `first_call_voice` consent when the parent stayed on and did not ask to stop, unusable call -> retry policy, slot completed, `send_summary` jobs per opted-in member. Invalid model output: raw stored encrypted, needs_review, keyword alerts still raised.
- 13 golden scenarios from SPEC §15 plus 4 lifecycle scenarios, all replayed through the real handlers; each checks attempts, slot, alerts (type, category, source), report fields, needs_review, transcript deletion and summary privacy, and that a full replay changes nothing.

## M5: alerts and notifications (2026-10-06)
- `internal/notify`: Messenger interface, template catalog (SPEC §10 plus `admin_alert_v1`), parameter cleaning, Meta webhook signature check (`X-Hub-Signature-256`, constant time, empty secret rejects) and parser; `notify/fake` (records, masked log, real Meta format) and `notify/cloud` (Graph API client, 10 s timeout, errors never echo parameters or the phone-number id).
- `internal/alerts.PlanStep`: emergency = priority 1 + admins, then each member, then the local contact, every ALERT_ACK_TIMEOUT, then "unacknowledged" to admins; urgent/scam = priority 1 + admins, then the next member after URGENT_ACK_TIMEOUT; unconfirmed scam keyword = admins only; stop = calls_paused + admins; missed calls = priority 1; watch = next summary. Chains are keyed by alert type, so an urgent alert upgraded to an emergency restarts on the emergency timetable and the old chain stops.
- `internal/outbound`: escalate_alert (one step, one send_alert job per recipient, audit row per step), send_alert, send_summary (parent active, share_with_family consent, opted-in member of the same family; private-note check again at send time; points to the separate alert when the call raised one; watch trends appended), admin_notice (invalid extraction, failed dial, dead-lettered safety jobs). Every send goes through the safety gate and a notifications row with a dedupe key: a blocked send is recorded and not retried, a vendor error is retried, a sent message is never re-sent.
- Inbound: `GET/POST /v1/webhooks/whatsapp`; "I'm on it" acknowledges only when pressed by a member of that parent's family; other messages stored encrypted for review; delivery statuses only move forward.
- Job priorities (`migrations/00004`): alerts and admin notices, then call processing, then dialing, then summaries.
- `migrations/00003`: notification dedupe key, sent_at, indexes.
- Scenarios now assert every outbound message (recipient role, template, content); new: fall_acknowledged, fall_local_contact, stranger_ack_ignored, primary_not_opted_in. Safety tests: a model that leaks the private note into the summary and family messages never gets it sent; CALLS_ENABLED=false means no dial and no message.

## M6: memory loop (2026-10-06)
- Follow-ups from a usable call by the parent are saved as encrypted memories expiring after FOLLOW_UP_TTL; an active follow-up with the same normalised text is extended instead of duplicated. Memory timestamps come from the injectable clock.
- The next call's context loads up to 3 unexpired follow-ups, newest first, sanitised into the agent prompt (and passed to extraction).
- `DeleteExpiredMemories` query ready for the retention job (M9).
- Scenarios: `memory_follow_up` (yesterday's knee pain is in today's prompt and stays one memory), `memory_expired` (past the TTL it is gone). Scenario files can now check the agent prompt and override FOLLOW_UP_TTL.

## M7: admin pages (2026-10-07)
- `internal/admin`: server-rendered `html/template` pages behind HTTP Basic auth (bcrypt hashes from `ADMIN_USERS`, cost >= 10; unknown users take the same time; failures limited to 5 per 15 minutes per address and per account). CSRF tokens on every form (HMAC over admin + issue time, 12 h validity, key derived from the encryption key so all instances agree) plus an Origin check. Strict CSP: no inline scripts or styles; one embedded stylesheet.
- Pages: dashboard (today's calls, open alerts by type, pickup rate per parent, average call minutes, cost per family this month, median minutes from call end to summary), review queue (open alerts with resolve/false-alarm + encrypted note, reports needing review with mark-reviewed, family messages with mark-handled), call detail (report and transcript; audit row written before display), parents list and detail (consents record/withdraw, status pause/stop/resume, schedule, medicines add/stop, test call, export, erase).
- Onboarding: one form for plan, parent, up to 3 members (WhatsApp opt-in), up to 5 medicines, in-person consents and "start now"; friendly phone input (spaces, dashes, 10-digit Indian mobiles); every problem listed at once with values kept; duplicate phone refused politely; one transaction with audit rows.
- Consent withdrawal: calls or data_processing -> stopped, share_with_family -> paused; both cancel scheduled attempts. Resume needs the three consents. Test call uses today's slot and still passes every guard at dial time.
- Export: every record about a parent as decrypted JSON, audited. Erase: exact confirmation phrase; deletes transcripts, reports, memories, medicines and alert details, anonymises the parent with a non-dialable +999 placeholder, stops calls, audited.
- `haalchaal hashpw` / `make hashpw` for ADMIN_USERS. Keyring gained `DeriveKey`.
- Integration tests drive the pages over HTTP: auth and lockout, CSRF, validation, onboarding through to the scheduler's dial with the right prompt, audit rows, withdrawal, refused resume, export, erase, test call blocked by the allowlist. Checked visually with Playwright screenshots.

## M9 (part 1): operations (2026-10-07)
- Daily retention job (`retention:<date>`, 03:00 in DEFAULT_TIMEZONE, enqueued by the scheduler loop): deletes transcripts past `delete_after` (asking the voice platform to delete recordings first when it supports it; a failure retries instead of losing track), expired follow-ups, family messages older than the transcript retention, webhook dedupe rows and finished jobs older than 30 days, audit rows older than RETENTION_AUDIT_DAYS; writes an audit row with counts.
- `haalchaal rotate-keys`: re-encrypts every `_enc` column under the active key, paged, compare-and-set, re-runnable; audited.
- `Dockerfile` (static binary on distroless, non-root, one image for every role) and `.dockerignore`.
- Runbooks: deploy, backup and restore, key rotation, incidents, breach (draft for the founder, with the "who was affected" queries).
- Local restore drill: `pg_dump` -> `pg_restore` into a new database -> `migrate status` all applied -> admin export of a parent decrypts with the production keys and writes its audit row. The drill on the real hosting is still to do once it is chosen (SPEC §16 "done when").
- `TELECOM_COMPLIANCE_ACK` stays false by default; prod dialling is impossible without it.
