# Backup and restore

Milestone 9 is done only when a restore has been tested once (SPEC §16).

## Backups
- Managed Postgres automated daily snapshots plus point-in-time recovery, retained at least 14 days, in the same India region.
- Encryption keys are backed up separately from the database (password manager or KMS). A database backup is useless without `ENCRYPTION_KEYS`; keys without the backup expose nothing.

## Restore drill (do it once before the pilot, then quarterly)
1. Restore the latest snapshot to a new, private database instance.
2. Point a one-off container at it with the production `ENCRYPTION_KEYS` and `APP_ENV=staging`, `CALLS_ENABLED=false`.
3. Run `haalchaal migrate status`: every migration applied.
4. Run `serve` on a private address; log in to `/admin`; open one parent and one call. Decryption works, an audit row is written.
5. Record the time taken and any problems in `docs/progress.md`. Delete the restored instance.

## Real restore
1. Stop `worker` everywhere (no calls or messages during the restore).
2. Restore to the chosen point in time.
3. Run `migrate up` with the current release, then start `worker` with `CALLS_ENABLED=false` to check the queue is sane (`/admin` dashboard).
4. Re-enable calls. Slots already past `window_end` are skipped and logged, never called late.
