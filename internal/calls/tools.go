package calls

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/EklavyaGoyal17/haalchaal/internal/alerts"
	"github.com/EklavyaGoyal17/haalchaal/internal/audit"
	"github.com/EklavyaGoyal17/haalchaal/internal/db"
	"github.com/EklavyaGoyal17/haalchaal/internal/voice"
)

// ErrUnknownTool means the tool name is not one the agent has.
var ErrUnknownTool = errors.New("unknown tool")

// ToolFinding turns a mid-call tool call into an alert finding. Unknown or
// missing arguments resolve towards more caution, never less: an unknown
// severity is an emergency, an unknown category is "other".
func ToolFinding(tc voice.ToolCall) (alerts.Finding, error) {
	switch tc.Tool {
	case voice.ToolReportRedFlag:
		cat := tc.Args["category"]
		if !slices.Contains(alerts.RedFlagCategories, cat) {
			cat = "other"
		}
		typ := alerts.TypeEmergency
		if tc.Args["severity"] == "urgent" && cat != "self_harm" {
			typ = alerts.TypeUrgent
		}
		return alerts.Finding{Type: typ, Category: cat, Source: alerts.SourceTool, Detail: tc.Args["quote"]}, nil
	case voice.ToolReportScam:
		pat := tc.Args["pattern"]
		if !slices.Contains(alerts.ScamPatterns, pat) || pat == "other" {
			pat = "scam_other"
		}
		return alerts.Finding{Type: alerts.TypeScam, Category: pat, Source: alerts.SourceTool, Detail: tc.Args["quote"]}, nil
	case voice.ToolReportStopRequest:
		return alerts.Finding{Type: alerts.TypeUrgent, Category: alerts.CategoryStopRequested, Source: alerts.SourceTool}, nil
	}
	return alerts.Finding{}, ErrUnknownTool
}

// HandleTool records a verified mid-call tool call: dedupe, then create or
// merge the alert immediately; a stop request also pauses the parent.
func (s *Service) HandleTool(ctx context.Context, provider string, tc voice.ToolCall) (EventResult, error) {
	f, err := ToolFinding(tc)
	if err != nil {
		return EventIgnored, err
	}
	res := EventApplied
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		now := s.Clock.Now()
		_, err := q.InsertWebhookEvent(ctx, db.InsertWebhookEventParams{Provider: provider + ":tool", EventID: tc.EventID, ReceivedAt: now})
		if errors.Is(err, pgx.ErrNoRows) {
			res = EventDuplicate
			return nil
		}
		if err != nil {
			return fmt.Errorf("record tool event: %w", err)
		}
		call, err := s.findCall(ctx, q, provider, tc.ProviderCallID, tc.CallID)
		if err != nil {
			return err
		}
		f.ParentID, f.CallID = call.ParentID, call.ID
		r, err := alerts.RaiseForCall(ctx, tx, s.Keyring, f, now)
		if err != nil {
			return err
		}
		s.Log.Warn("mid-call alert", "call_id", call.ID, "alert_id", r.AlertID, "type", r.Type, "category", f.Category, "created", r.Created)
		if tc.Tool == voice.ToolReportStopRequest {
			return PauseParent(ctx, tx, call.ParentID, "paused", ReasonStopRequested, audit.ActorSystem, now)
		}
		return nil
	})
	return res, err
}

// PauseParent sets a parent to paused or stopped and makes sure nothing still
// dials: scheduled attempts are cancelled with their place_call jobs. Safety
// alert jobs and processing of finished calls are left to run.
func PauseParent(ctx context.Context, tx pgx.Tx, parentID uuid.UUID, status, reason, actor string, now time.Time) error {
	if status != "paused" && status != "stopped" {
		return fmt.Errorf("pause parent: invalid status %q", status)
	}
	q := db.New(tx)
	if err := q.SetParentStatus(ctx, db.SetParentStatusParams{ID: parentID, Status: status}); err != nil {
		return fmt.Errorf("set parent status: %w", err)
	}
	if _, err := q.CancelOpenCallsForParent(ctx, db.CancelOpenCallsForParentParams{ParentID: parentID, EndReason: &reason, Now: &now}); err != nil {
		return fmt.Errorf("cancel scheduled calls: %w", err)
	}
	if _, err := q.CancelQueuedCallJobsForParent(ctx, db.CancelQueuedCallJobsForParentParams{ParentID: parentID.String(), Reason: reason}); err != nil {
		return fmt.Errorf("cancel call jobs: %w", err)
	}
	if _, err := q.CancelPendingSlotsForParent(ctx, parentID); err != nil {
		return fmt.Errorf("cancel slots: %w", err)
	}
	return audit.Write(ctx, tx, actor, "parent_"+status, "parent", parentID.String(), audit.Details{"reason": reason})
}
