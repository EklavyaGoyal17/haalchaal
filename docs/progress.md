# Progress

## M0: skeleton (2026-10-07)
- Go module `github.com/EklavyaGoyal17/haalchaal`, `cmd/haalchaal` with `serve` (worker, migrate, simcall are stubs until their milestones).
- `internal/config`: every SPEC §12 variable; `CALLS_ENABLED` and `TELECOM_COMPLIANCE_ACK` fail closed (unparseable → false with a warning); secrets required outside dev; config logs never show secrets or full numbers.
- `internal/logging`: slog JSON, request ids, shared `MaskPhone`. `internal/domain.ValidE164`. `internal/clock` real and fake.
- `internal/httpapi`: `GET /healthz`, `GET /readyz` (database ping; migration check comes in M1), request-id and panic-recovery middleware.
- Docker Compose Postgres 16 on 127.0.0.1:55432 (5432 is taken by a local Postgres on the founder's PC) with a named volume; Makefile; `.env.example`; GitHub Actions running vet and tests.
- Dependency: `pgx/v5` (Postgres driver, stack decision).
