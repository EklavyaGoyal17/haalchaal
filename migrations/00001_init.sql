-- +goose Up
-- Schema from docs/SPEC.md §4. Columns ending in _enc hold
-- key_id (1 byte) || nonce (12 bytes) || ciphertext, written only through internal/crypto.
-- Additive safety checks beyond §4: E.164 format on phone columns, a valid
-- call window, and family priority 1 to 5.

CREATE TABLE accounts (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  plan         TEXT NOT NULL CHECK (plan IN ('basic', 'daily')),
  status       TEXT NOT NULL DEFAULT 'trial'
               CHECK (status IN ('trial', 'active', 'paused', 'cancelled')),
  billing_note TEXT,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE family_members (
  id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id         UUID NOT NULL REFERENCES accounts(id),
  name               TEXT NOT NULL,
  phone_e164         TEXT NOT NULL CHECK (phone_e164 ~ '^\+[1-9][0-9]{7,14}$'),
  relation           TEXT,
  language           TEXT NOT NULL DEFAULT 'en',
  priority           INT  NOT NULL CHECK (priority BETWEEN 1 AND 5),
  whatsapp_opt_in_at TIMESTAMPTZ,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (account_id, priority)
);

-- One parent row per phone number. A couple sharing one phone is one row whose
-- preferred_name covers both (for example 'Mummy aur Papa'); revisit after the pilot.
CREATE TABLE parents (
  id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id               UUID NOT NULL REFERENCES accounts(id),
  preferred_name           TEXT NOT NULL,
  phone_e164               TEXT NOT NULL UNIQUE CHECK (phone_e164 ~ '^\+[1-9][0-9]{7,14}$'),
  language                 TEXT NOT NULL CHECK (language IN ('hi', 'ta', 'en')),
  timezone                 TEXT NOT NULL DEFAULT 'Asia/Kolkata',
  call_time_local          TIME NOT NULL,
  window_start             TIME NOT NULL DEFAULT '09:00',
  window_end               TIME NOT NULL DEFAULT '20:00',
  status                   TEXT NOT NULL DEFAULT 'onboarding'
                           CHECK (status IN ('onboarding', 'active', 'paused', 'stopped')),
  interests_enc            BYTEA,
  safe_word_enc            BYTEA,
  local_contact_name       TEXT,
  local_contact_phone_e164 TEXT CHECK (local_contact_phone_e164 ~ '^\+[1-9][0-9]{7,14}$'),
  first_call_done_at       TIMESTAMPTZ,
  created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (window_start < window_end)
);

CREATE TABLE consents (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  parent_id    UUID NOT NULL REFERENCES parents(id),
  kind         TEXT NOT NULL
               CHECK (kind IN ('calls', 'data_processing', 'share_with_family', 'recording')),
  given_by     TEXT NOT NULL CHECK (given_by IN ('parent', 'guardian')),
  method       TEXT NOT NULL CHECK (method IN ('in_person', 'first_call_voice', 'form')),
  text_version TEXT NOT NULL,
  evidence_ref TEXT,
  given_at     TIMESTAMPTZ NOT NULL,
  withdrawn_at TIMESTAMPTZ
);

CREATE TABLE medicines (
  id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  parent_id UUID NOT NULL REFERENCES parents(id),
  name_enc  BYTEA NOT NULL,
  timing    TEXT NOT NULL,
  active    BOOLEAN NOT NULL DEFAULT true
);

CREATE TABLE call_slots (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  parent_id  UUID NOT NULL REFERENCES parents(id),
  local_date DATE NOT NULL,
  status     TEXT NOT NULL DEFAULT 'pending'
             CHECK (status IN ('pending', 'completed', 'missed', 'cancelled')),
  UNIQUE (parent_id, local_date)
);

CREATE TABLE calls (
  id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  slot_id          UUID NOT NULL REFERENCES call_slots(id),
  parent_id        UUID NOT NULL REFERENCES parents(id),
  attempt_no       INT  NOT NULL CHECK (attempt_no BETWEEN 1 AND 3),
  scheduled_for    TIMESTAMPTZ NOT NULL,
  status           TEXT NOT NULL DEFAULT 'scheduled'
                   CHECK (status IN ('scheduled', 'dialing', 'ringing', 'in_progress', 'completed',
                                     'no_answer', 'busy', 'failed', 'cancelled')),
  provider         TEXT NOT NULL,
  provider_call_id TEXT UNIQUE,
  started_at       TIMESTAMPTZ,
  ended_at         TIMESTAMPTZ,
  duration_sec     INT,
  cost_paise       BIGINT,
  end_reason       TEXT,
  UNIQUE (slot_id, attempt_no)
);

CREATE TABLE transcripts (
  call_id      UUID PRIMARY KEY REFERENCES calls(id),
  turns_enc    BYTEA NOT NULL,
  language     TEXT,
  delete_after TIMESTAMPTZ NOT NULL
);

CREATE TABLE call_reports (
  call_id        UUID PRIMARY KEY REFERENCES calls(id),
  schema_version TEXT NOT NULL,
  report_enc     BYTEA NOT NULL,
  red_flag_count INT NOT NULL DEFAULT 0,
  confidence     REAL NOT NULL,
  needs_review   BOOLEAN NOT NULL,
  model          TEXT NOT NULL,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE memories (
  id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  parent_id      UUID NOT NULL REFERENCES parents(id),
  kind           TEXT NOT NULL CHECK (kind IN ('follow_up', 'fact')),
  content_enc    BYTEA NOT NULL,
  source_call_id UUID REFERENCES calls(id),
  expires_at     TIMESTAMPTZ,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE alerts (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  parent_id       UUID NOT NULL REFERENCES parents(id),
  call_id         UUID REFERENCES calls(id),
  slot_id         UUID REFERENCES call_slots(id),
  type            TEXT NOT NULL
                  CHECK (type IN ('emergency', 'urgent', 'scam', 'missed_calls', 'watch')),
  category        TEXT NOT NULL,
  source          TEXT NOT NULL CHECK (source IN ('tool', 'model', 'keyword', 'rule')),
  detail_enc      BYTEA,
  status          TEXT NOT NULL DEFAULT 'open'
                  CHECK (status IN ('open', 'notified', 'acknowledged', 'resolved', 'false_alarm')),
  escalation_step INT NOT NULL DEFAULT 0,
  acknowledged_by UUID REFERENCES family_members(id),
  acknowledged_at TIMESTAMPTZ,
  reviewed_by     TEXT,
  reviewed_at     TIMESTAMPTZ,
  review_note_enc BYTEA,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX alerts_one_per_call ON alerts (call_id, category) WHERE call_id IS NOT NULL;
CREATE UNIQUE INDEX alerts_one_per_slot ON alerts (slot_id, category) WHERE slot_id IS NOT NULL;

CREATE TABLE notifications (
  id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  alert_id            UUID REFERENCES alerts(id),
  call_id             UUID REFERENCES calls(id),
  family_member_id    UUID REFERENCES family_members(id),
  recipient_kind      TEXT NOT NULL CHECK (recipient_kind IN ('family', 'local_contact', 'admin')),
  channel             TEXT NOT NULL CHECK (channel IN ('whatsapp', 'voice')),
  template            TEXT NOT NULL,
  provider_message_id TEXT,
  status              TEXT NOT NULL DEFAULT 'queued'
                      CHECK (status IN ('queued', 'sent', 'delivered', 'read', 'failed')),
  error               TEXT,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE inbound_messages (
  id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  family_member_id UUID REFERENCES family_members(id),
  from_e164        TEXT NOT NULL,
  kind             TEXT NOT NULL CHECK (kind IN ('button', 'text')),
  body_enc         BYTEA,
  handled_at       TIMESTAMPTZ,
  received_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE webhook_events (
  provider    TEXT NOT NULL,
  event_id    TEXT NOT NULL,
  received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (provider, event_id)
);

CREATE TABLE jobs (
  id           BIGSERIAL PRIMARY KEY,
  kind         TEXT NOT NULL,
  dedupe_key   TEXT UNIQUE,
  payload      JSONB NOT NULL,
  run_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  status       TEXT NOT NULL DEFAULT 'queued'
               CHECK (status IN ('queued', 'running', 'done', 'failed')),
  attempts     INT NOT NULL DEFAULT 0,
  max_attempts INT NOT NULL DEFAULT 5,
  locked_by    TEXT,
  locked_at    TIMESTAMPTZ,
  last_error   TEXT
);
CREATE INDEX jobs_ready ON jobs (run_at) WHERE status = 'queued';

CREATE TABLE audit_log (
  id        BIGSERIAL PRIMARY KEY,
  actor     TEXT NOT NULL,
  action    TEXT NOT NULL,
  entity    TEXT NOT NULL,
  entity_id TEXT NOT NULL,
  at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  details   JSONB
);

CREATE INDEX calls_parent_scheduled ON calls (parent_id, scheduled_for);
CREATE INDEX alerts_status_created ON alerts (status, created_at);
CREATE INDEX memories_parent_expires ON memories (parent_id, expires_at);
CREATE INDEX notifications_alert ON notifications (alert_id);

-- +goose Down
DROP TABLE audit_log;
DROP TABLE jobs;
DROP TABLE webhook_events;
DROP TABLE inbound_messages;
DROP TABLE notifications;
DROP TABLE alerts;
DROP TABLE memories;
DROP TABLE call_reports;
DROP TABLE transcripts;
DROP TABLE calls;
DROP TABLE call_slots;
DROP TABLE medicines;
DROP TABLE consents;
DROP TABLE parents;
DROP TABLE family_members;
DROP TABLE accounts;
