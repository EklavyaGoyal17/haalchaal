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
