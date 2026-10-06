package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EklavyaGoyal17/haalchaal/internal/clock"
	"github.com/EklavyaGoyal17/haalchaal/internal/db"
	"github.com/EklavyaGoyal17/haalchaal/internal/jobs"
)

// TickInterval is how often the scheduler runs.
const TickInterval = time.Minute

// Scheduler creates one call slot per parent per due day. It is safe to run
// in several processes at once: the slot's unique key decides which one wins,
// and only the winner creates attempt 1 and its place_call job.
type Scheduler struct {
	Pool     *pgxpool.Pool
	Clock    clock.Clock
	Log      *slog.Logger
	Provider string // VOICE_PROVIDER, stored on each attempt

	mu         sync.Mutex
	skipLogged map[uuid.UUID]string // parent -> local date already logged as skipped
}

// TickResult counts what one tick did.
type TickResult struct {
	Eligible int // parents with active status and valid consents
	Created  int // slots (with attempt 1 and a place_call job) created
	Skipped  int // due days missed because the window had closed with no slot
}

// Run ticks immediately and then every TickInterval until ctx is done.
func (s *Scheduler) Run(ctx context.Context) error {
	t := time.NewTicker(TickInterval)
	defer t.Stop()
	for {
		if _, err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			s.Log.Error("scheduler tick", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Tick runs one scheduling pass. An error for one parent is logged and does
// not stop the others.
func (s *Scheduler) Tick(ctx context.Context) (TickResult, error) {
	var res TickResult
	now := s.Clock.Now()
	parents, err := db.New(s.Pool).ListSchedulableParents(ctx, now)
	if err != nil {
		return res, fmt.Errorf("list schedulable parents: %w", err)
	}
	res.Eligible = len(parents)
	for _, p := range parents {
		created, skipped, err := s.scheduleParent(ctx, now, p)
		if err != nil {
			s.Log.Error("schedule parent", "parent_id", p.ID, "error", err)
			continue
		}
		if created {
			res.Created++
		}
		if skipped {
			res.Skipped++
		}
	}
	return res, nil
}

func (s *Scheduler) scheduleParent(ctx context.Context, now time.Time, p db.ListSchedulableParentsRow) (created, skipped bool, err error) {
	local, err := LocalNow(now, p.Timezone)
	if err != nil {
		return false, false, err
	}
	if !Due(p.Plan, local.Day) {
		return false, false, nil
	}
	w, err := WindowFromPG(p.CallTimeLocal, p.WindowStart, p.WindowEnd)
	if err != nil {
		return false, false, err
	}
	if !w.Valid() {
		return false, false, errors.New("call window cannot hold a call")
	}
	if local.Wall >= w.End {
		return false, s.noteSkippedDay(ctx, p.ID, local), nil
	}
	if !w.ReadyForFirstAttempt(local.Wall) {
		return false, false, nil
	}

	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		slotID, err := q.InsertSlot(ctx, db.InsertSlotParams{ParentID: p.ID, LocalDate: local.PGDate()})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // today's slot exists; another tick or process won
		}
		if err != nil {
			return fmt.Errorf("insert slot: %w", err)
		}
		call, err := q.InsertCallAttempt(ctx, db.InsertCallAttemptParams{
			SlotID: slotID, ParentID: p.ID, AttemptNo: 1, ScheduledFor: now, Provider: s.Provider,
		})
		if err != nil {
			return fmt.Errorf("insert attempt: %w", err)
		}
		if _, _, err := jobs.Enqueue(ctx, tx, PlaceCallSpec(p.ID, call.ID, now)); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return false, false, err
	}
	if created {
		s.Log.Info("slot created", "parent_id", p.ID, "local_date", local.DateString())
	}
	return created, false, nil
}

// PlaceCallSpec is the place_call job for an attempt.
func PlaceCallSpec(parentID, callID uuid.UUID, runAt time.Time) jobs.Spec {
	return jobs.Spec{
		Kind:      jobs.KindPlaceCall,
		DedupeKey: "place_call:" + callID.String(),
		Payload:   jobs.Payload{ParentID: &parentID, CallID: &callID},
		RunAt:     runAt,
	}
}

// noteSkippedDay logs, once per parent and date, a due day that passed with
// no slot (for example, the server was down all day). It reports whether this
// call was the one that noticed.
func (s *Scheduler) noteSkippedDay(ctx context.Context, parentID uuid.UUID, local Local) bool {
	date := local.DateString()
	s.mu.Lock()
	if s.skipLogged == nil {
		s.skipLogged = map[uuid.UUID]string{}
	}
	already := s.skipLogged[parentID] == date
	s.mu.Unlock()
	if already {
		return false
	}
	exists, err := db.New(s.Pool).SlotExists(ctx, db.SlotExistsParams{ParentID: parentID, LocalDate: local.PGDate()})
	if err != nil {
		s.Log.Error("check skipped day", "parent_id", parentID, "error", err)
		return false
	}
	s.mu.Lock()
	s.skipLogged[parentID] = date
	s.mu.Unlock()
	if exists {
		return false
	}
	s.Log.Warn("due day skipped: window closed with no slot", "parent_id", parentID, "local_date", date)
	return true
}
