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
