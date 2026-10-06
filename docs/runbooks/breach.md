# Personal data breach (draft for the founder)

The founder owns this document (SPEC §13). Under the DPDP Act and Rules 2025
the Data Fiduciary must inform affected people without delay and the Data
Protection Board with a detailed report within 72 hours. Confirm the details
with the lawyer.

## First hour
1. Contain: rotate the exposed secret (key rotation runbook, vendor tokens, `ADMIN_USERS` passwords), revoke access, stop the affected component. Keep logs.
2. Decide scope with the queries below. Write down times in IST and UTC.
3. Start a timeline document: what happened, when it was found, who was told.

## Who was affected
Audit trail of admin access in a time window:
```sql
SELECT at, actor, action, entity, entity_id FROM audit_log
WHERE at BETWEEN $from AND $to ORDER BY at;
```
Parents whose calls, reports or transcripts fall in a window:
```sql
SELECT DISTINCT p.id, p.preferred_name FROM calls c JOIN parents p ON p.id = c.parent_id
WHERE c.scheduled_for BETWEEN $from AND $to;
```
Family members who were messaged in a window (phones are in `family_members`):
```sql
SELECT DISTINCT m.id, m.name FROM notifications n JOIN family_members m ON m.id = n.family_member_id
WHERE n.created_at BETWEEN $from AND $to;
```
Everything held about one parent: the admin export (`/admin/parents/{id}/export`).

## What could have been exposed
- Database without keys: names, phone numbers, schedules, statuses, alert categories. Transcripts, reports, medicines, memories, alert quotes and family messages are encrypted.
- Database with keys, or an admin account: everything for every parent.
- A vendor: see `docs/processors.md` for exactly what each vendor receives.

## Notify
- Affected parents and families, in their language, plainly: what happened, what data, what we did, what they can do, who to contact.
- The Data Protection Board within 72 hours, with the detailed report the Rules require.
- Vendors involved, if the breach came through them.
