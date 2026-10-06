// Package alerts creates and merges alerts (SPEC §9). Every alert, once
// created, gets an escalate_alert job that notifies people; when unsure, the
// rule is to alert.
package alerts

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/EklavyaGoyal17/haalchaal/internal/crypto"
	"github.com/EklavyaGoyal17/haalchaal/internal/db"
	"github.com/EklavyaGoyal17/haalchaal/internal/jobs"
)

// Alert types.
const (
	TypeEmergency   = "emergency"
	TypeUrgent      = "urgent"
	TypeScam        = "scam"
	TypeMissedCalls = "missed_calls"
	TypeWatch       = "watch"
)

// Alert sources.
const (
	SourceTool    = "tool"
	SourceModel   = "model"
	SourceKeyword = "keyword"
	SourceRule    = "rule"
)

// Categories that are not red-flag or scam categories.
const (
	CategoryMissedCalls   = "missed_calls"
	CategoryStopRequested = "stop_requested"
)

// Red-flag categories (SPEC §8).
var RedFlagCategories = []string{"fall", "chest_pain", "breathing", "fainting", "stroke_signs", "bleeding", "confusion", "self_harm", "other"}

// Scam patterns (SPEC §8).
var ScamPatterns = []string{"agency_threat", "otp_or_bank_request", "money_request", "video_call_pressure", "other"}

// AADDetail binds alert details to their column.
const AADDetail = "alerts.detail_enc"

// MaxQuote is the longest quote stored or sent.
const MaxQuote = 200

// Finding is one reason to alert about a call.
type Finding struct {
	ParentID uuid.UUID
	CallID   uuid.UUID
	Type     string
	Category string
	Source   string
	Detail   string // quote or reason; encrypted at rest
}

// Raised reports what RaiseForCall did.
type Raised struct {
	AlertID  uuid.UUID
	Type     string
	Created  bool
	Upgraded bool // an existing alert became an emergency
}

// RaiseForCall creates the alert for (call, category) or merges into the
// existing one, and enqueues its escalation. It runs inside tx.
func RaiseForCall(ctx context.Context, tx db.DBTX, kr *crypto.Keyring, f Finding, now time.Time) (Raised, error) {
	var detail []byte
	if f.Detail != "" {
		if kr == nil {
			return Raised{}, errors.New("raise alert: no encryption keys configured")
		}
		var err error
		if detail, err = kr.EncryptString(Truncate(f.Detail, MaxQuote), AADDetail); err != nil {
			return Raised{}, fmt.Errorf("raise alert: %w", err)
		}
	}
	q := db.New(tx)
	var prevType string
	if existing, err := q.ListAlertsForCall(ctx, &f.CallID); err == nil {
		for _, a := range existing {
			if a.Category == f.Category {
				prevType = a.Type
			}
		}
	} else {
		return Raised{}, fmt.Errorf("raise alert: %w", err)
	}
	row, err := q.UpsertCallAlert(ctx, db.UpsertCallAlertParams{
		ParentID: f.ParentID, CallID: &f.CallID, Type: f.Type, Category: f.Category, Source: f.Source, DetailEnc: detail,
	})
	if err != nil {
		return Raised{}, fmt.Errorf("raise alert: %w", err)
	}
	r := Raised{AlertID: row.ID, Type: row.Type, Created: row.Inserted}
	r.Upgraded = !row.Inserted && row.Type == TypeEmergency && prevType != TypeEmergency
	if r.Created || r.Upgraded {
		if err := enqueueEscalation(ctx, tx, f.ParentID, row.ID, now, r.Upgraded); err != nil {
			return r, err
		}
	}
	return r, nil
}

// RaiseMissedCalls creates the missed_calls alert for a slot, once.
func RaiseMissedCalls(ctx context.Context, tx db.DBTX, parentID, slotID uuid.UUID, now time.Time) (uuid.UUID, bool, error) {
	id, err := db.New(tx).InsertSlotAlert(ctx, db.InsertSlotAlertParams{
		ParentID: parentID, SlotID: &slotID, Type: TypeMissedCalls, Category: CategoryMissedCalls,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("raise missed_calls: %w", err)
	}
	return id, true, enqueueEscalation(ctx, tx, parentID, id, now, false)
}

// EscalationSpec is the escalate_alert job for one step of an alert.
func EscalationSpec(parentID, alertID uuid.UUID, step int, runAt time.Time, suffix string) jobs.Spec {
	key := "escalate_alert:" + alertID.String() + ":" + strconv.Itoa(step)
	if suffix != "" {
		key += ":" + suffix
	}
	return jobs.Spec{
		Kind:        jobs.KindEscalateAlert,
		DedupeKey:   key,
		Payload:     jobs.Payload{ParentID: &parentID, AlertID: &alertID, Step: &step},
		RunAt:       runAt,
		MaxAttempts: 10, // safety messages get more tries than ordinary jobs
	}
}

func enqueueEscalation(ctx context.Context, tx db.DBTX, parentID, alertID uuid.UUID, now time.Time, upgraded bool) error {
	suffix := ""
	if upgraded {
		suffix = "upgraded"
	}
	if _, _, err := jobs.Enqueue(ctx, tx, EscalationSpec(parentID, alertID, 0, now, suffix)); err != nil {
		return fmt.Errorf("enqueue escalation: %w", err)
	}
	return nil
}

// Truncate shortens s to at most n runes.
func Truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
