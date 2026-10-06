package calls

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EklavyaGoyal17/haalchaal/internal/alerts"
	"github.com/EklavyaGoyal17/haalchaal/internal/audit"
	"github.com/EklavyaGoyal17/haalchaal/internal/clock"
	"github.com/EklavyaGoyal17/haalchaal/internal/crypto"
	"github.com/EklavyaGoyal17/haalchaal/internal/db"
	"github.com/EklavyaGoyal17/haalchaal/internal/extract"
	"github.com/EklavyaGoyal17/haalchaal/internal/jobs"
	"github.com/EklavyaGoyal17/haalchaal/internal/safety"
	"github.com/EklavyaGoyal17/haalchaal/internal/scheduler"
	"github.com/EklavyaGoyal17/haalchaal/internal/voice"
)

// More end reasons.
const (
	ReasonStartFailed      = "start_failed"
	ReasonNoEvents         = "no_provider_events"
	ReasonStopRequested    = "stop_requested"
	ReasonConsentWithdrawn = "consent_withdrawn"
)

// StaleAfter is how long an attempt may sit in a live state with no provider
// event before the sweeper fails it.
const StaleAfter = 30 * time.Minute

// Service runs the call lifecycle.
type Service struct {
	Pool                *pgxpool.Pool
	Clock               clock.Clock
	Log                 *slog.Logger
	Keyring             *crypto.Keyring
	Voice               voice.Provider
	Gate                safety.Gate
	RetryOffsets        []time.Duration
	TranscriptRetention time.Duration
	Extractor           extract.Extractor
	FollowUpTTL         time.Duration
}

// ErrUnknownCall means an event or tool call names no attempt we know.
var ErrUnknownCall = errors.New("unknown call")

// PlaceCall is the place_call job handler (SPEC §5). It re-checks every
// guard, moves the attempt to dialing before asking the platform to dial (so
// a re-run never dials twice), then records the provider call id.
func (s *Service) PlaceCall(ctx context.Context, j jobs.Job) error {
	if j.Payload.CallID == nil {
		return jobs.Permanent(errors.New("place_call without call_id"))
	}
	callID := *j.Payload.CallID
	now := s.Clock.Now()
	q := db.New(s.Pool)
	st, err := q.GetCallGuardState(ctx, db.GetCallGuardStateParams{CallID: callID, Now: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.Permanent(fmt.Errorf("call %s not found", callID))
	}
	if err != nil {
		return fmt.Errorf("load guard state: %w", err)
	}
	if st.CallStatus != StatusScheduled {
		return nil // already handled; jobs are idempotent
	}
	log := s.Log.With("call_id", callID, "parent_id", st.ParentID, "attempt", st.AttemptNo)

	if reason := CheckGuards(s.Gate, st, now); reason != "" {
		err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			q := db.New(tx)
			n, err := q.TransitionCall(ctx, db.TransitionCallParams{
				ID: callID, FromStatus: StatusScheduled, ToStatus: StatusCancelled, EndedAt: &now, EndReason: &reason,
			})
			if err != nil || n == 0 {
				return err
			}
			if _, err := q.SetSlotStatus(ctx, db.SetSlotStatusParams{ID: st.SlotID, Status: "cancelled"}); err != nil {
				return err
			}
			return audit.Write(ctx, tx, audit.ActorSystem, "call_blocked", "call", callID.String(), audit.Details{"reason": reason})
		})
		if err != nil {
			return fmt.Errorf("cancel blocked call: %w", err)
		}
		log.Info("call blocked by guard", "reason", reason)
		return nil
	}

	cc, _, err := LoadContext(ctx, q, s.Keyring, st.ParentID, now)
	if err != nil {
		return fmt.Errorf("load call context: %w", err)
	}
	prompt, err := cc.Render()
	if err != nil {
		return jobs.Permanent(err)
	}
	lang, err := q.GetParent(ctx, st.ParentID)
	if err != nil {
		return fmt.Errorf("load parent: %w", err)
	}

	n, err := q.MarkCallDialing(ctx, callID)
	if err != nil {
		return fmt.Errorf("mark dialing: %w", err)
	}
	if n == 0 {
		return nil // another worker got here first
	}

	started, startErr := s.Voice.StartCall(ctx, voice.CallRequest{
		CallID: callID.String(), To: st.PhoneE164, Language: lang.Language,
		SystemPrompt: prompt, Variables: cc.Variables(),
	})
	if startErr != nil {
		// Fail closed: no silent retry on another path. The attempt fails
		// and the normal retry policy decides about the next one.
		log.Error("start call failed", "error", startErr)
		reason := ReasonStartFailed
		end := s.Clock.Now()
		return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			q := db.New(tx)
			n, err := q.TransitionCall(ctx, db.TransitionCallParams{
				ID: callID, FromStatus: StatusDialing, ToStatus: StatusFailed, EndedAt: &end, EndReason: &reason,
			})
			if err != nil || n == 0 {
				return err
			}
			call, err := q.GetCall(ctx, callID)
			if err != nil {
				return err
			}
			return s.afterUnanswered(ctx, tx, call, end)
		})
	}
	if _, err := q.SetProviderCallID(ctx, db.SetProviderCallIDParams{ID: callID, ProviderCallID: &started.ProviderCallID}); err != nil {
		return fmt.Errorf("save provider call id: %w", err)
	}
	log.Info("call started")
	return nil
}

// afterUnanswered schedules the next attempt, or marks the slot missed and
// raises a missed_calls alert when no attempt is left.
func (s *Service) afterUnanswered(ctx context.Context, tx pgx.Tx, call db.Call, endedAt time.Time) error {
	q := db.New(tx)
	slot, err := q.GetSlot(ctx, call.SlotID)
	if err != nil {
		return fmt.Errorf("load slot: %w", err)
	}
	if slot.Status != "pending" {
		return nil
	}
	p, err := q.GetParent(ctx, call.ParentID)
	if err != nil {
		return fmt.Errorf("load parent: %w", err)
	}
	w, err := scheduler.WindowFromPG(p.CallTimeLocal, p.WindowStart, p.WindowEnd)
	if err != nil {
		return err
	}
	if next, ok := NextAttemptAt(int(call.AttemptNo), endedAt, s.RetryOffsets, w, p.Timezone, slot.LocalDate.Time); ok && p.Status == "active" {
		nc, err := q.InsertCallAttempt(ctx, db.InsertCallAttemptParams{
			SlotID: slot.ID, ParentID: call.ParentID, AttemptNo: call.AttemptNo + 1, ScheduledFor: next, Provider: call.Provider,
		})
		if err != nil {
			return fmt.Errorf("insert next attempt: %w", err)
		}
		if _, _, err := jobs.Enqueue(ctx, tx, scheduler.PlaceCallSpec(call.ParentID, nc.ID, next)); err != nil {
			return err
		}
		s.Log.Info("next attempt scheduled", "parent_id", call.ParentID, "attempt", nc.AttemptNo, "at", next)
		return nil
	}
	if _, err := q.SetSlotStatus(ctx, db.SetSlotStatusParams{ID: slot.ID, Status: "missed"}); err != nil {
		return err
	}
	if _, _, err := alerts.RaiseMissedCalls(ctx, tx, call.ParentID, slot.ID, s.Clock.Now()); err != nil {
		return err
	}
	s.Log.Info("slot missed", "parent_id", call.ParentID, "slot_id", slot.ID)
	return nil
}

// EventResult says what ApplyEvent did with one event.
type EventResult string

const (
	EventApplied   EventResult = "applied"
	EventDuplicate EventResult = "duplicate"
	EventIgnored   EventResult = "ignored" // out of order or no-op
)

// ApplyEvent records one verified webhook event: dedupe on (provider,
// event_id), move the attempt forward, store a transcript and enqueue its
// processing, and schedule a retry after an unanswered attempt. Everything
// happens in one transaction, so a failure leaves the event unrecorded and the
// provider's retry is processed afresh.
func (s *Service) ApplyEvent(ctx context.Context, provider string, ev voice.Event) (EventResult, error) {
	res := EventIgnored
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		_, err := q.InsertWebhookEvent(ctx, db.InsertWebhookEventParams{Provider: provider, EventID: ev.EventID, ReceivedAt: s.Clock.Now()})
		if errors.Is(err, pgx.ErrNoRows) {
			res = EventDuplicate
			return nil
		}
		if err != nil {
			return fmt.Errorf("record webhook event: %w", err)
		}
		call, err := s.findCall(ctx, q, provider, ev.ProviderCallID, ev.CallID)
		if err != nil {
			return err
		}
		log := s.Log.With("call_id", call.ID, "event", ev.Type)

		at := ev.At
		if at.IsZero() {
			at = s.Clock.Now()
		}
		if to, ok := StatusForEvent(ev.Type); ok {
			if CanTransition(call.Status, to) {
				p := db.TransitionCallParams{ID: call.ID, FromStatus: call.Status, ToStatus: to}
				if to == StatusInProgress {
					p.StartedAt = &at
				}
				if Terminal(to) {
					p.EndedAt = &at
					reason := string(ev.Type)
					p.EndReason = &reason
					if ev.DurationSec > 0 {
						d := int32(ev.DurationSec)
						p.DurationSec = &d
					}
					if ev.CostPaise > 0 {
						p.CostPaise = &ev.CostPaise
					}
				}
				if _, err := q.TransitionCall(ctx, p); err != nil {
					return fmt.Errorf("transition: %w", err)
				}
				res = EventApplied
				call.Status = to
				if Unanswered(to) {
					if err := s.afterUnanswered(ctx, tx, call, at); err != nil {
						return err
					}
				}
			} else {
				log.Debug("event ignored: not a forward transition", "from", call.Status)
			}
		}
		if len(ev.Transcript) > 0 {
			stored, err := s.storeTranscript(ctx, tx, call, ev.Transcript)
			if err != nil {
				return err
			}
			if stored {
				res = EventApplied
			}
		}
		return nil
	})
	return res, err
}

func (s *Service) findCall(ctx context.Context, q *db.Queries, provider, providerCallID, callID string) (db.Call, error) {
	if providerCallID != "" {
		c, err := q.GetCallByProviderID(ctx, db.GetCallByProviderIDParams{Provider: provider, ProviderCallID: &providerCallID})
		if err == nil {
			return q.GetCallForUpdate(ctx, c.ID)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return db.Call{}, err
		}
	}
	id, err := uuid.Parse(callID)
	if err != nil {
		return db.Call{}, ErrUnknownCall
	}
	c, err := q.GetCallForUpdate(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && c.Provider != provider) {
		return db.Call{}, ErrUnknownCall
	}
	if err != nil {
		return db.Call{}, err
	}
	// The event beat StartCall's return: remember the provider id now.
	if c.ProviderCallID == nil && providerCallID != "" {
		if _, err := q.SetProviderCallID(ctx, db.SetProviderCallIDParams{ID: c.ID, ProviderCallID: &providerCallID}); err != nil {
			return db.Call{}, err
		}
		c.ProviderCallID = &providerCallID
	} else if c.ProviderCallID != nil && providerCallID != "" && *c.ProviderCallID != providerCallID {
		return db.Call{}, ErrUnknownCall // metadata and provider id disagree
	}
	return c, nil
}

func (s *Service) storeTranscript(ctx context.Context, tx pgx.Tx, call db.Call, turns []voice.Turn) (bool, error) {
	if s.Keyring == nil {
		return false, errors.New("store transcript: no encryption keys configured")
	}
	raw, err := voice.MarshalTurns(turns)
	if err != nil {
		return false, err
	}
	enc, err := s.Keyring.Encrypt(raw, AADTranscript)
	if err != nil {
		return false, fmt.Errorf("encrypt transcript: %w", err)
	}
	q := db.New(tx)
	p, err := q.GetParent(ctx, call.ParentID)
	if err != nil {
		return false, err
	}
	n, err := q.InsertTranscript(ctx, db.InsertTranscriptParams{
		CallID: call.ID, TurnsEnc: enc, Language: &p.Language, DeleteAfter: s.Clock.Now().Add(s.TranscriptRetention),
	})
	if err != nil {
		return false, fmt.Errorf("insert transcript: %w", err)
	}
	if n == 0 {
		return false, nil
	}
	_, _, err = jobs.Enqueue(ctx, tx, ProcessCallSpec(call.ParentID, call.ID, s.Clock.Now()))
	return true, err
}

// ProcessCallSpec is the process_call job for a finished call.
func ProcessCallSpec(parentID, callID uuid.UUID, runAt time.Time) jobs.Spec {
	return jobs.Spec{
		Kind: jobs.KindProcessCall, DedupeKey: "process_call:" + callID.String(),
		Payload: jobs.Payload{ParentID: &parentID, CallID: &callID}, RunAt: runAt, MaxAttempts: 8,
	}
}

// SweepStale fails attempts stuck in a live state with no provider event for
// StaleAfter (for example, the process died between dialing and saving the
// provider id), and runs the retry policy for each.
func (s *Service) SweepStale(ctx context.Context) (int, error) {
	now := s.Clock.Now()
	stale, err := db.New(s.Pool).ListStaleCalls(ctx, now.Add(-StaleAfter))
	if err != nil {
		return 0, fmt.Errorf("list stale calls: %w", err)
	}
	n := 0
	for _, c := range stale {
		reason := ReasonNoEvents
		err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			q := db.New(tx)
			changed, err := q.TransitionCall(ctx, db.TransitionCallParams{
				ID: c.ID, FromStatus: c.Status, ToStatus: StatusFailed, EndedAt: &now, EndReason: &reason,
			})
			if err != nil || changed == 0 {
				return err
			}
			n++
			return s.afterUnanswered(ctx, tx, c, now)
		})
		if err != nil {
			s.Log.Error("sweep stale call", "call_id", c.ID, "error", err)
		} else {
			s.Log.Warn("stale call failed", "call_id", c.ID, "was", c.Status)
		}
	}
	return n, nil
}
