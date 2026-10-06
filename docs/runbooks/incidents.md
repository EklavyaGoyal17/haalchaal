# Incident runbook

Safety first: a missed emergency is worse than a false alarm (CLAUDE.md rule 3).

## An emergency alert was not acknowledged
The system already escalates: every family member, the local contact, then an "UNACKNOWLEDGED" admin message. When an admin gets that message:
1. Open `/admin/review`, find the alert, open the call.
2. Phone the family directly. If nobody answers and the parent is at risk, call the local contact, then 112 with the parent's address from the family.
3. Resolve the alert with a note describing what was done.

## WhatsApp messages are failing
Symptoms: `notifications.status = failed`, admin notice "dead_letter_send_alert".
1. Check Meta's status page and the error code in the worker logs (`whatsapp cloud: status ... code ...`).
2. Template rejected or paused by Meta: fix the template; until then phone families about any open emergency or urgent alert from the review queue.
3. Token expired: rotate `WHATSAPP_TOKEN` and redeploy. Failed jobs retry with backoff; dead-lettered ones need a manual phone call.

## The voice platform is down or the caller number is rejected
Symptoms: attempts ending `start_failed`, admin notice "start_failed".
- The system fails closed: no fallback number, no silent re-dial. Retries follow the normal policy and the slot is marked missed with a missed-calls message to the family.
- Fix the platform or number; do not switch numbers without the compliance check (SPEC §14).

## A parent asks to stop, by any channel
Set them to `paused` or `stopped` on their admin page with a reason. Scheduled attempts and queued calls are cancelled at once. If they withdraw consent, use Withdraw on the consent row instead.

## Queue backlog
Dashboard numbers stall; `jobs` has many `queued` rows with `run_at` in the past.
- Add worker processes or raise `WORKER_CONCURRENCY` (keep under `pool_max_conns`). Alerts are claimed before summaries, so a backlog delays summaries first.
- A worker that died mid-job is reaped after 5 minutes automatically.
