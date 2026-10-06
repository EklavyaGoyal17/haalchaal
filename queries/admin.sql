-- name: ListParentsOverview :many
SELECT p.id, p.preferred_name, p.status, p.language, p.timezone, p.call_time_local, p.first_call_done_at,
       a.plan, a.status AS account_status,
       (SELECT count(DISTINCT c.kind) FROM consents c
         WHERE c.parent_id = p.id AND c.withdrawn_at IS NULL
           AND c.kind IN ('calls', 'data_processing', 'share_with_family'))::int AS required_consents,
       (SELECT count(*) FROM alerts al WHERE al.parent_id = p.id AND al.status IN ('open', 'notified'))::int AS open_alerts
FROM parents p JOIN accounts a ON a.id = p.account_id
ORDER BY p.created_at DESC
LIMIT 500;

-- name: ListConsentsForParent :many
SELECT * FROM consents WHERE parent_id = $1 ORDER BY given_at DESC, id;

-- name: ListAllMedicines :many
SELECT * FROM medicines WHERE parent_id = $1 ORDER BY active DESC, timing, id;

-- name: SetMedicineActive :exec
UPDATE medicines SET active = @active WHERE id = @id AND parent_id = @parent_id;

-- name: ListRecentCallsForParent :many
SELECT c.*, r.needs_review AS report_needs_review, r.red_flag_count AS report_red_flags, r.schema_version AS report_version
FROM calls c LEFT JOIN call_reports r ON r.call_id = c.id
WHERE c.parent_id = @parent_id
ORDER BY c.scheduled_for DESC
LIMIT @max_items;

-- name: ListCallsBetween :many
SELECT c.id, c.attempt_no, c.status, c.scheduled_for, c.duration_sec, c.end_reason,
       p.id AS parent_id, p.preferred_name
FROM calls c JOIN parents p ON p.id = c.parent_id
WHERE c.scheduled_for >= @from_at AND c.scheduled_for < @to_at
ORDER BY c.scheduled_for, p.preferred_name;

-- name: ListOpenAlerts :many
SELECT al.*, p.preferred_name
FROM alerts al JOIN parents p ON p.id = al.parent_id
WHERE al.status IN ('open', 'notified')
ORDER BY CASE al.type WHEN 'emergency' THEN 0 WHEN 'urgent' THEN 1 WHEN 'scam' THEN 2 WHEN 'missed_calls' THEN 3 ELSE 4 END,
         al.created_at
LIMIT 200;

-- name: ListReportsToReview :many
SELECT r.call_id, r.confidence, r.red_flag_count, r.schema_version, r.created_at, p.id AS parent_id, p.preferred_name
FROM call_reports r JOIN calls c ON c.id = r.call_id JOIN parents p ON p.id = c.parent_id
WHERE r.needs_review AND r.reviewed_at IS NULL
ORDER BY r.created_at
LIMIT 200;

-- name: MarkReportReviewed :execrows
UPDATE call_reports SET reviewed_by = @reviewed_by, reviewed_at = @reviewed_at
WHERE call_id = @call_id AND reviewed_at IS NULL;

-- name: ListUnhandledInbound :many
SELECT i.*, m.name AS member_name
FROM inbound_messages i LEFT JOIN family_members m ON m.id = i.family_member_id
WHERE i.handled_at IS NULL
ORDER BY i.received_at
LIMIT 200;

-- name: MarkInboundHandled :execrows
UPDATE inbound_messages SET handled_at = @handled_at WHERE id = @id AND handled_at IS NULL;

-- name: ResolveAlert :execrows
UPDATE alerts SET status = @status, reviewed_by = @reviewed_by, reviewed_at = @reviewed_at, review_note_enc = @review_note_enc
WHERE id = @id AND status IN ('open', 'notified', 'acknowledged');

-- name: PickupRates :many
-- Completed slots over due (non-cancelled) slots per parent since a date.
SELECT p.id, p.preferred_name,
       count(*) FILTER (WHERE s.status = 'completed')::int AS completed,
       count(*) FILTER (WHERE s.status <> 'cancelled')::int AS due
FROM parents p JOIN call_slots s ON s.parent_id = p.id
WHERE s.local_date >= @since
GROUP BY p.id, p.preferred_name
ORDER BY p.preferred_name;

-- name: AverageCallSeconds :one
SELECT COALESCE(avg(duration_sec), 0)::float8 FROM calls
WHERE status = 'completed' AND duration_sec IS NOT NULL AND ended_at >= @since;

-- name: CostPerAccount :many
SELECT a.id, a.plan, COALESCE(sum(c.cost_paise), 0)::bigint AS paise,
       (SELECT string_agg(p2.preferred_name, ', ') FROM parents p2 WHERE p2.account_id = a.id) AS parents
FROM accounts a JOIN parents p ON p.account_id = a.id JOIN calls c ON c.parent_id = p.id
WHERE c.scheduled_for >= @since
GROUP BY a.id, a.plan
ORDER BY paise DESC;

-- name: OpenAlertsByType :many
SELECT type, count(*)::int AS n FROM alerts WHERE status IN ('open', 'notified') GROUP BY type ORDER BY type;

-- name: MedianSummaryMinutes :one
-- Minutes from the end of a call to its first daily summary going out.
SELECT COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM (n.sent_at - c.ended_at)) / 60), 0)::float8
FROM notifications n JOIN calls c ON c.id = n.call_id
WHERE n.template = 'daily_summary_v1' AND n.sent_at IS NOT NULL AND c.ended_at IS NOT NULL AND c.ended_at >= @since;

-- name: ListTranscriptsForParent :many
SELECT t.* FROM transcripts t JOIN calls c ON c.id = t.call_id WHERE c.parent_id = $1 ORDER BY c.scheduled_for;

-- name: ListReportsForParent :many
SELECT r.* FROM call_reports r JOIN calls c ON c.id = r.call_id WHERE c.parent_id = $1 ORDER BY r.created_at;

-- name: ListAllMemories :many
SELECT * FROM memories WHERE parent_id = $1 ORDER BY created_at;

-- name: ListInboundForAccount :many
SELECT i.* FROM inbound_messages i JOIN family_members m ON m.id = i.family_member_id
WHERE m.account_id = $1 ORDER BY i.received_at;

-- name: ListAllCallsForParent :many
SELECT * FROM calls WHERE parent_id = $1 ORDER BY scheduled_for;

-- name: CountAttemptsForSlot :one
SELECT count(*)::int FROM calls WHERE slot_id = $1;

-- name: UpdateParentSchedule :exec
UPDATE parents SET call_time_local = @call_time_local, window_start = @window_start, window_end = @window_end
WHERE id = @id;

-- name: EraseParent :exec
-- Anonymise on request (SPEC §13). The phone becomes a non-dialable
-- placeholder that still satisfies the E.164 and uniqueness checks.
UPDATE parents
SET preferred_name = 'Erased', phone_e164 = @placeholder_phone, interests_enc = NULL, safe_word_enc = NULL,
    local_contact_name = NULL, local_contact_phone_e164 = NULL, status = 'stopped'
WHERE id = @id;

-- name: EraseTranscripts :exec
DELETE FROM transcripts t USING calls c WHERE t.call_id = c.id AND c.parent_id = @parent_id;

-- name: EraseReports :exec
DELETE FROM call_reports r USING calls c WHERE r.call_id = c.id AND c.parent_id = @parent_id;

-- name: EraseMemories :exec
DELETE FROM memories WHERE parent_id = @parent_id;

-- name: EraseMedicines :exec
DELETE FROM medicines WHERE parent_id = @parent_id;

-- name: EraseAlertDetails :exec
UPDATE alerts SET detail_enc = NULL, review_note_enc = NULL WHERE parent_id = @parent_id;
