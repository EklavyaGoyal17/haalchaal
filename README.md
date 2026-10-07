# HaalChaal

**An AI that phones elderly parents every day in their own language, checks on their wellbeing and medicines, remembers earlier calls, and sends the family a WhatsApp summary, with instant alerts when something sounds wrong.**

![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)
![PostgreSQL](https://img.shields.io/badge/PostgreSQL-16-4169E1?logo=postgresql&logoColor=white)
![Status](https://img.shields.io/badge/status-pre--pilot-orange)

> *HaalChaal* (हालचाल) means "how are you doing?" in Hindi.

This repository is the backend for a 12-week pilot with 10 to 30 families in India. Parents need only an ordinary phone, families use WhatsApp, and the founders use a small admin website.

**New here?** Read [`docs/PROJECT_OVERVIEW.md`](docs/PROJECT_OVERVIEW.md) for the full picture in plain language: the idea, what is built, the gaps that remain and the roadmap.

---

## Contents
- [Why](#why)
- [How it works](#how-it-works)
- [Safety rules](#safety-rules)
- [Features](#features)
- [Project status](#project-status)
- [Architecture](#architecture)
- [Getting started](#getting-started)
- [Configuration](#configuration)
- [Testing](#testing)
- [Repository layout](#repository-layout)
- [Documentation](#documentation)
- [Contributing](#contributing)

---

## Why

Many parents aged 60 to 85 live alone while their children work in other cities or abroad. Problems get hidden ("sab theek hai"), falls and missed medicines go unnoticed, and scams aimed at the elderly ("digital arrest", OTP fraud) are rising. Apps and wearables need tech skills, and caregivers are expensive.

HaalChaal replaces none of the family's care. It adds a friendly daily call, a memory of how the parent has been, and a fast alert when something is wrong.

## How it works

```
 Admin website ──► onboard family, record consent
                         │
 Scheduler ──► daily call in the parent's window (09:00–20:00 local)
                         │
 Voice platform (Vapi) ──► AI says it is an AI, checks mood, sleep, food, pain, medicines
        │                      │
        │ mid-call tools        └─► transcript
        ▼                                 ▼
  instant alert             AI report (Claude) + rule engine + keyword safety net
        │                                 │
        ▼                                 ▼
 WhatsApp alert, escalated      WhatsApp daily summary to the family
 until someone taps "I'm on it"     │
        │                           ▼
        └──────► human review queue for every emergency, scam or uncertain report
```

1. A founder onboards the family, medicines and consents on the admin website.
2. Every day the parent gets a call at their chosen time, retried after 15 and 45 minutes if unanswered.
3. Danger signs (fall, chest pain, breathing trouble, fainting, stroke signs, bleeding, new confusion, self-harm talk) and scam patterns raise an alert **during** the call.
4. After the call, the transcript becomes a structured report and the family gets a short summary.
5. Follow-ups ("knee pain") are remembered for the next call.

## Safety rules

These are non-negotiable and covered by tests (see [`CLAUDE.md`](CLAUDE.md)):

- The agent always says it is an AI, never a human or a family member.
- No medical advice, no changes to medicines, no diagnosis.
- Red flags always alert; when unsure, alert.
- Every emergency, urgent or scam alert, and every low-confidence report, goes to human review.
- A parent's private notes are never sent to the family.
- Nothing is dialled or messaged unless `CALLS_ENABLED=true`. Outside production only numbers in `DEV_ALLOWLIST` are contacted. Production dialling also needs `TELECOM_COMPLIANCE_ACK=true` after legal sign-off. Every switch fails closed.
- No calls outside the parent's window, and none without recorded consent. A stop request stops calls at once.
- No transcripts, medicine names, health details or full phone numbers in logs.
- The agent never asks for money, OTPs, bank or card details, or Aadhaar numbers.
- What is said during a call is treated as data: spoken instructions never change system behaviour.

## Features

| Area | Highlights |
| --- | --- |
| Calling | Per-parent timezone and call window, daily or 3-days-a-week plans, retries, missed-call alerts, stale-call sweeper |
| Mid-call tools | `report_red_flag`, `report_scam` and `report_stop_request`, recorded in under a second |
| Post-call report | Strict JSON schema with validation; keyword detector for Hindi (Latin and Devanagari), English and Tamil; low-confidence reports go to review |
| Alerts | Emergency, urgent, scam, missed-call and trend ("watch") alerts; escalation through children, then a local contact, then admins; "I'm on it" acknowledgement |
| WhatsApp | Template catalogue (English and Hindi), delivery tracking, inbound replies stored encrypted |
| Memory | Follow-ups carried into the next call, expiring after 7 days |
| Admin website | Dashboard metrics, onboarding, review queue, call detail (every view audited), consents, pause/stop/resume, medicines, test call, data export and erase |
| Privacy and security | AES-256-GCM field encryption, key rotation, audit log, masked phone numbers, strict CSP, CSRF protection, login rate limiting, request size caps, load shedding |
| Operations | Daily retention job, Docker image, CI, and runbooks for deploy, backups, key rotation, incidents and breaches |

## Project status

| Milestone | Status |
| --- | --- |
| M0–M7: skeleton, data layer, jobs, calls, extraction, alerts, memory, admin | ✅ Done |
| M8: real vendors | 🟡 Vapi (voice), Claude (reports) and WhatsApp Cloud adapters built and tested; first real call pending accounts |
| M9: deploy and operate | 🟡 Docker, retention, runbooks and a local restore drill done; hosting not yet chosen |

Open items are accounts and approvals rather than code: a Vapi account and Indian number, Meta template approval, hosting in an India region, and legal and medical sign-off. Details are in [`docs/PROJECT_OVERVIEW.md`](docs/PROJECT_OVERVIEW.md).

## Architecture

- **Go** (standard library `net/http`), one binary with subcommands: `serve`, `worker`, `dev`, `migrate`, `simcall`, `seed-demo`, `genkey`, `hashpw`, `rotate-keys`.
- **PostgreSQL 16** with `pgx/v5`, `sqlc` for queries and `goose` migrations. The job queue is also Postgres (`FOR UPDATE SKIP LOCKED`), with no Redis.
- **Vendors behind interfaces**, each with a `fake` implementation used by default in dev and tests:

  | Interface | Fake | Real adapter |
  | --- | --- | --- |
  | `internal/voice` | `voice/fake` | `voice/vapi` (Vapi) |
  | `internal/extract` | `extract/fake` | `extract/claude` (Anthropic Claude) |
  | `internal/notify` | `notify/fake` | `notify/cloud` (WhatsApp Cloud API) |

- **Admin UI:** server-rendered `html/template` pages, with no JavaScript framework.
- **Logging:** `log/slog` with JSON output.

## Getting started

### Prerequisites
- [Go](https://go.dev/dl/) 1.26 or newer
- [Docker](https://www.docker.com/) (for local PostgreSQL)
- `make` (on Windows: `winget install ezwinports.make`)

### Run locally (everything faked, nothing real is contacted)

```bash
git clone https://github.com/EklavyaGoyal17/haalchaal.git
cd haalchaal
cp .env.example .env          # reference only; the app reads real environment variables

# 1. An encryption key and an admin login
go run ./cmd/haalchaal genkey 1                      # -> ENCRYPTION_KEYS (kid 1)
echo 'a long password' | go run ./cmd/haalchaal hashpw   # -> bcrypt hash

# 2. Set the environment (bash shown; use $env:NAME='...' in PowerShell)
export APP_ENV=dev
export DATABASE_URL='postgres://haalchaal:haalchaal_dev@localhost:55432/haalchaal?sslmode=disable'
export ENCRYPTION_KEYS='1:<key from genkey>'
export ENCRYPTION_ACTIVE_KID=1
export ADMIN_USERS='you@example.com:<hash from hashpw>'

# 3. Start Postgres, apply migrations, run server and worker
make dev
```

Open **http://localhost:8080/admin/** and log in with the email and password from step 1.

Try it out:

```bash
make seed-demo                         # four made-up demo families (dev only)
make simcall SCENARIO=fall_emergency   # replay a full call: call -> report -> alert -> WhatsApp
```

Health endpoints: `GET /healthz` (process up) and `GET /readyz` (database reachable, migrations current).

### Run with Docker

```bash
docker build -t haalchaal .
docker run --env-file .env -p 8080:8080 haalchaal serve
```

The image is a static binary on distroless, running as non-root. Run `worker` as a separate container. Deployment steps are in [`docs/runbooks/deploy.md`](docs/runbooks/deploy.md).

## Configuration

Every variable is listed in [`.env.example`](.env.example) and documented in [`docs/SPEC.md`](docs/SPEC.md) §12. The most important ones:

| Variable | Purpose |
| --- | --- |
| `APP_ENV` | `dev`, `staging` or `prod`; fakes are refused in prod |
| `CALLS_ENABLED` | Master switch for any real call or message (default `false`) |
| `DEV_ALLOWLIST` | The only numbers that may be contacted outside prod |
| `TELECOM_COMPLIANCE_ACK` | Required for prod dialling; set only after legal sign-off |
| `ENCRYPTION_KEYS`, `ENCRYPTION_ACTIVE_KID` | Field-encryption keyring |
| `ADMIN_USERS` | `email:bcrypt_hash,...` for the admin website |
| `VOICE_PROVIDER` | `fake` or `vapi` (see [`docs/runbooks/vapi-setup.md`](docs/runbooks/vapi-setup.md)) |
| `LLM_PROVIDER` | `fake` or `anthropic` |
| `WHATSAPP_PROVIDER` | `fake` or `cloud` |

Never commit `.env` or any secret.

## Testing

```bash
make lint                # go vet (+ staticcheck if installed)
make test                # unit tests, no network
make test-db             # create the haalchaal_test database
make test-integration    # needs TEST_DATABASE_URL; run with -p 1 (shared database)
```

- 24 golden call transcripts in `testdata/transcripts/` are replayed through the real handlers, and each must produce its expected report, alerts and messages. Replaying every request a second time must change nothing.
- Safety tests that must always pass:
  - red flags always alert;
  - private notes never reach outbound messages;
  - no calls outside the window;
  - no calls without consent;
  - no real sends while `CALLS_ENABLED=false`.
- A load test simulates 1,000 parents with concurrent workers and duplicated webhooks.
- CI (`.github/workflows/ci.yml`) runs gofmt, vet, staticcheck, unit and race-enabled integration tests, `govulncheck` and an sqlc drift check.

## Repository layout

```
cmd/haalchaal/         main with subcommands
internal/config        environment config (fails closed)
internal/db            sqlc output (generated; never edit by hand)
internal/jobs          Postgres job queue: enqueue, claim, retry, reaper
internal/scheduler     due calls, call windows, retry policy
internal/calls         call lifecycle state machine, mid-call tools, post-call processing
internal/voice         VoiceProvider interface, fake, Vapi adapter
internal/extract       report schema and validation, fake, Claude adapter
internal/alerts        rules, keyword detector, escalation
internal/notify        Messenger interface, WhatsApp Cloud adapter, fake
internal/outbound      alerts, summaries and admin notices through the safety gate
internal/safety        the outbound gate (CALLS_ENABLED, allowlist, compliance)
internal/crypto        field encryption and key rotation
internal/admin         admin pages and templates
internal/maintenance   retention job and key rotation
internal/sim           scenario replay (simcall) and end-to-end tests
prompts/               agent and extraction prompts, report schema, voice tool definitions
migrations/            goose SQL migrations
queries/               sqlc SQL
testdata/transcripts/  golden call transcripts and expected outcomes
docs/                  spec, overview, runbooks, security notes, progress
```

## Documentation

| Document | What it covers |
| --- | --- |
| [`docs/PROJECT_OVERVIEW.md`](docs/PROJECT_OVERVIEW.md) | Plain-language overview, gaps, roadmap and requirements |
| [`docs/SPEC.md`](docs/SPEC.md) | Full build specification |
| [`docs/progress.md`](docs/progress.md) | What was built in each milestone |
| [`docs/security.md`](docs/security.md) | Threat model and security checkpoints |
| [`docs/processors.md`](docs/processors.md) | Every vendor that receives personal data, and what it receives |
| [`docs/runbooks/`](docs/runbooks/) | Deploy, Vapi setup, backup and restore, key rotation, incidents, breach |
| [`CLAUDE.md`](CLAUDE.md) | Engineering rules, conventions and decision log |

## Contributing

This is a private pre-pilot project. Before changing anything:

- Read [`CLAUDE.md`](CLAUDE.md). The safety rules there override everything else.
- Keep changes small, add table-driven tests, and run `make lint test` (plus `make test-integration` for database code).
- After editing `queries/`, run `make sqlc`. Never edit `internal/db` by hand.
- When a dependency is added, give a one-line reason in the commit message.
- Changes to prompts or rules must keep every golden transcript passing.

---

© 2026 HaalChaal. All rights reserved. No license has been chosen yet, so the code may not be reused without permission.
