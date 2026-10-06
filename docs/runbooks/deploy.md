# Deploy

The hosting provider is still an open decision (SPEC §17). This runbook is
provider-neutral; fill in the provider-specific commands once chosen.

## Shape
- One container image (`Dockerfile`), three roles:
  - `haalchaal serve`: HTTP (webhooks, admin pages). Stateless; run 2 or more behind a TLS-terminating load balancer.
  - `haalchaal worker`: scheduler tick, job workers, retention. Run 1 or more; they coordinate through Postgres.
  - `haalchaal migrate up`: run once per release, before new `serve`/`worker` start.
- Managed Postgres 16, India region, private network only, automated backups with point-in-time recovery.
- Secrets in the provider's secret store, injected as environment variables. Never in the image or the repo.

## Release checklist
1. CI green on the commit (unit, race integration, govulncheck, sqlc drift).
2. Build and push the image tagged with the commit SHA.
3. Run `haalchaal migrate up` as a one-off task with the new image. Migrations are additive; a failed migration rolls back its own transaction.
4. Roll `worker`, then `serve`. `/readyz` returns 503 until migrations are current, so the load balancer will not route to an instance that is ahead or behind.
5. Watch logs for 15 minutes: `level=ERROR`, `job dead-lettered`, `webhook signature rejected` spikes.

## Environment
Every variable is in `.env.example` and SPEC §12. Production must have:
- `APP_ENV=prod`, real vendors (fakes are refused in prod), `ENCRYPTION_KEYS` + `ENCRYPTION_ACTIVE_KID`, `ADMIN_USERS`, `ADMIN_ALERT_PHONES`, `PUBLIC_BASE_URL`, `TRUSTED_PROXIES` (load balancer ranges).
- `CALLS_ENABLED=true` only when the pilot is live.
- `TELECOM_COMPLIANCE_ACK=false` until the founder has written confirmation from the lawyer and the operator that the calling number and registration are compliant (SPEC §14). Dialling in prod is impossible until then, by design.
- `DATABASE_URL` with `sslmode=verify-full` and a `pool_max_conns` sized for `WORKER_CONCURRENCY` + HTTP load.

## Health and alerting
- Liveness: `GET /healthz`. Readiness: `GET /readyz` (database reachable, migrations current).
- Uptime check on `/healthz` every minute from outside the provider; page the founders after 3 failures.
- Log-based alerts: any `job dead-lettered`, any `admin notice with no ADMIN_ALERT_PHONES configured`, more than 10 `webhook signature rejected` in 5 minutes.

## Rollback
Redeploy the previous image tag. Migrations are only additive within a release, so the previous binary runs against the newer schema. Only run `migrate down` deliberately, after reading the Down section of that migration.
