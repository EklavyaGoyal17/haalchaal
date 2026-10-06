package admin

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/EklavyaGoyal17/haalchaal/internal/audit"
	"github.com/EklavyaGoyal17/haalchaal/internal/calls"
	"github.com/EklavyaGoyal17/haalchaal/internal/db"
	"github.com/EklavyaGoyal17/haalchaal/internal/scheduler"
)

func (h *Handler) parents(w http.ResponseWriter, r *http.Request) {
	rows, err := db.New(h.Pool).ListParentsOverview(r.Context())
	if err != nil {
		h.fail(w, r, "could not load parents", err)
		return
	}
	h.render(w, r, "parents", "Parents", rows)
}

type medicineView struct {
	ID     uuid.UUID
	Name   string
	Timing string
	Active bool
}

func (h *Handler) parentDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	q := db.New(h.Pool)
	p, err := q.GetParentWithAccount(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.fail(w, r, "could not load parent", err)
		return
	}
	var d struct {
		Parent        db.Parent
		Plan          string
		AccountStatus string
		Members       []db.FamilyMember
		Consents      []db.Consent
		Medicines     []medicineView
		Calls         []db.ListRecentCallsForParentRow
	}
	d.Parent = db.Parent{ID: p.ID, AccountID: p.AccountID, PreferredName: p.PreferredName, PhoneE164: p.PhoneE164, Language: p.Language,
		Timezone: p.Timezone, CallTimeLocal: p.CallTimeLocal, WindowStart: p.WindowStart, WindowEnd: p.WindowEnd, Status: p.Status,
		FirstCallDoneAt: p.FirstCallDoneAt, CreatedAt: p.CreatedAt}
	d.Plan, d.AccountStatus = p.Plan, p.AccountStatus
	if d.Members, err = q.ListFamilyMembers(ctx, p.AccountID); err != nil {
		h.fail(w, r, "could not load family", err)
		return
	}
	if d.Consents, err = q.ListConsentsForParent(ctx, id); err != nil {
		h.fail(w, r, "could not load consents", err)
		return
	}
	meds, err := q.ListAllMedicines(ctx, id)
	if err != nil {
		h.fail(w, r, "could not load medicines", err)
		return
	}
	for _, m := range meds {
		name, err := h.Keyring.DecryptString(m.NameEnc, calls.AADMedicineName)
		if err != nil {
			h.fail(w, r, "could not decrypt medicines", err)
			return
		}
		d.Medicines = append(d.Medicines, medicineView{ID: m.ID, Name: name, Timing: m.Timing, Active: m.Active})
	}
	if d.Calls, err = q.ListRecentCallsForParent(ctx, db.ListRecentCallsForParentParams{ParentID: id, MaxItems: 30}); err != nil {
		h.fail(w, r, "could not load calls", err)
		return
	}
	if err := audit.Write(ctx, h.Pool, audit.Admin(Admin(ctx)), "view_parent", "parent", id.String(), nil); err != nil {
		h.fail(w, r, "could not write audit log", err)
		return
	}
	h.render(w, r, "parent", p.PreferredName, d)
}

var consentKinds = []string{"calls", "data_processing", "share_with_family", "recording"}

// consent records or withdraws one consent. Withdrawing calls or
// data_processing stops the parent; withdrawing share_with_family pauses
// them. Both cancel anything still scheduled (SPEC §13).
func (h *Handler) consent(w http.ResponseWriter, r *http.Request) {
	if !h.checkForm(w, r) {
		return
	}
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	back := "/admin/parents/" + id.String()
	f := r.PostForm
	kind := f.Get("kind")
	if !slices.Contains(consentKinds, kind) {
		redirect(w, r, back, "", "Unknown consent kind.")
		return
	}
	now := h.Clock.Now()
	who := Admin(ctx)
	switch f.Get("action") {
	case "record":
		givenBy, method, version := f.Get("given_by"), f.Get("method"), strings.TrimSpace(f.Get("text_version"))
		if (givenBy != "parent" && givenBy != "guardian") || (method != "in_person" && method != "form") || version == "" || len(version) > 40 {
			redirect(w, r, back, "", "Choose who gave consent, how, and the script version.")
			return
		}
		ev := strings.TrimSpace(f.Get("evidence_ref"))
		if len(ev) > 100 {
			ev = ev[:100]
		}
		err := pgx.BeginFunc(ctx, h.Pool, func(tx pgx.Tx) error {
			if _, err := db.New(tx).RecordConsent(ctx, db.RecordConsentParams{
				ParentID: id, Kind: kind, GivenBy: givenBy, Method: method, TextVersion: version, EvidenceRef: &ev, GivenAt: now,
			}); err != nil {
				return err
			}
			return audit.Write(ctx, tx, audit.Admin(who), "record_consent", "parent", id.String(), audit.Details{"kind": kind, "given_by": givenBy, "method": method})
		})
		if err != nil {
			h.fail(w, r, "could not record consent", err)
			return
		}
		redirect(w, r, back, "Consent recorded.", "")
	case "withdraw":
		err := pgx.BeginFunc(ctx, h.Pool, func(tx pgx.Tx) error {
			n, err := db.New(tx).WithdrawConsent(ctx, db.WithdrawConsentParams{ParentID: id, Kind: kind, WithdrawnAt: &now})
			if err != nil || n == 0 {
				return err
			}
			if err := audit.Write(ctx, tx, audit.Admin(who), "withdraw_consent", "parent", id.String(), audit.Details{"kind": kind}); err != nil {
				return err
			}
			switch kind {
			case "calls", "data_processing":
				return calls.PauseParent(ctx, tx, id, "stopped", calls.ReasonConsentWithdrawn, audit.Admin(who), now)
			case "share_with_family":
				return calls.PauseParent(ctx, tx, id, "paused", calls.ReasonConsentWithdrawn, audit.Admin(who), now)
			}
			return nil
		})
		if err != nil {
			h.fail(w, r, "could not withdraw consent", err)
			return
		}
		redirect(w, r, back, "Consent withdrawn. Calls stop immediately where required.", "")
	default:
		redirect(w, r, back, "", "Unknown action.")
	}
}

// setStatus pauses, stops or resumes a parent. Resuming needs the three
// required consents.
func (h *Handler) setStatus(w http.ResponseWriter, r *http.Request) {
	if !h.checkForm(w, r) {
		return
	}
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	back := "/admin/parents/" + id.String()
	status, reason := r.PostForm.Get("status"), strings.TrimSpace(r.PostForm.Get("reason"))
	if reason == "" || len(reason) > 100 {
		redirect(w, r, back, "", "Give a short reason.")
		return
	}
	now := h.Clock.Now()
	who := audit.Admin(Admin(ctx))
	var msgErr string
	err := pgx.BeginFunc(ctx, h.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		switch status {
		case "paused", "stopped":
			if err := calls.PauseParent(ctx, tx, id, status, "admin", who, now); err != nil {
				return err
			}
			return audit.Write(ctx, tx, who, "status_note", "parent", id.String(), audit.Details{"reason": reason})
		case "active":
			kinds, err := q.ListValidConsentKinds(ctx, id)
			if err != nil {
				return err
			}
			for _, k := range calls.RequiredConsents {
				if !slices.Contains(kinds, k) {
					msgErr = "Record the calls, data_processing and share_with_family consents before resuming."
					return nil
				}
			}
			if err := q.SetParentStatus(ctx, db.SetParentStatusParams{ID: id, Status: "active"}); err != nil {
				return err
			}
			return audit.Write(ctx, tx, who, "parent_active", "parent", id.String(), audit.Details{"reason": reason})
		}
		msgErr = "Unknown status."
		return nil
	})
	if err != nil {
		h.fail(w, r, "could not change status", err)
		return
	}
	if msgErr != "" {
		redirect(w, r, back, "", msgErr)
		return
	}
	redirect(w, r, back, "Status changed to "+status+".", "")
}

func parseClock(s string) (pgtype.Time, bool) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return pgtype.Time{}, false
	}
	d := time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute
	return pgtype.Time{Microseconds: d.Microseconds(), Valid: true}, true
}

func (h *Handler) setSchedule(w http.ResponseWriter, r *http.Request) {
	if !h.checkForm(w, r) {
		return
	}
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	back := "/admin/parents/" + id.String()
	ct, ok1 := parseClock(r.PostForm.Get("call_time"))
	ws, ok2 := parseClock(r.PostForm.Get("window_start"))
	we, ok3 := parseClock(r.PostForm.Get("window_end"))
	if !ok1 || !ok2 || !ok3 {
		redirect(w, r, back, "", "Times must look like 10:00.")
		return
	}
	win, _ := scheduler.WindowFromPG(ct, ws, we)
	if !win.Valid() {
		redirect(w, r, back, "", "The window must start before it ends, and the call time must be before the window ends.")
		return
	}
	err := pgx.BeginFunc(ctx, h.Pool, func(tx pgx.Tx) error {
		if err := db.New(tx).UpdateParentSchedule(ctx, db.UpdateParentScheduleParams{ID: id, CallTimeLocal: ct, WindowStart: ws, WindowEnd: we}); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Admin(Admin(ctx)), "update_schedule", "parent", id.String(), nil)
	})
	if err != nil {
		h.fail(w, r, "could not save schedule", err)
		return
	}
	redirect(w, r, back, "Schedule saved.", "")
}

func (h *Handler) medicines(w http.ResponseWriter, r *http.Request) {
	if !h.checkForm(w, r) {
		return
	}
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	back := "/admin/parents/" + id.String()
	f := r.PostForm
	who := audit.Admin(Admin(ctx))
	var err error
	switch f.Get("action") {
	case "add":
		name, timing := strings.TrimSpace(f.Get("name")), strings.TrimSpace(f.Get("timing"))
		if name == "" || timing == "" || len(name) > 80 || len(timing) > 40 {
			redirect(w, r, back, "", "Give the medicine name and timing.")
			return
		}
		enc, encErr := h.Keyring.EncryptString(name, calls.AADMedicineName)
		if encErr != nil {
			h.fail(w, r, "could not encrypt", encErr)
			return
		}
		err = pgx.BeginFunc(ctx, h.Pool, func(tx pgx.Tx) error {
			if _, err := db.New(tx).CreateMedicine(ctx, db.CreateMedicineParams{ParentID: id, NameEnc: enc, Timing: timing}); err != nil {
				return err
			}
			return audit.Write(ctx, tx, who, "add_medicine", "parent", id.String(), nil)
		})
	case "deactivate":
		mid, perr := uuid.Parse(f.Get("medicine_id"))
		if perr != nil {
			redirect(w, r, back, "", "Unknown medicine.")
			return
		}
		err = pgx.BeginFunc(ctx, h.Pool, func(tx pgx.Tx) error {
			if err := db.New(tx).SetMedicineActive(ctx, db.SetMedicineActiveParams{ID: mid, ParentID: id, Active: false}); err != nil {
				return err
			}
			return audit.Write(ctx, tx, who, "deactivate_medicine", "parent", id.String(), audit.Details{"medicine_id": mid.String()})
		})
	default:
		redirect(w, r, back, "", "Unknown action.")
		return
	}
	if err != nil {
		h.fail(w, r, "could not save medicine", err)
		return
	}
	redirect(w, r, back, "Medicines updated.", "")
}

// testCall schedules an attempt now on today's slot. Every guard still
// applies when place_call runs; this only skips the wait.
func (h *Handler) testCall(w http.ResponseWriter, r *http.Request) {
	if !h.checkForm(w, r) {
		return
	}
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	back := "/admin/parents/" + id.String()
	now := h.Clock.Now()
	var msgErr string
	err := pgx.BeginFunc(ctx, h.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		p, err := q.GetParent(ctx, id)
		if err != nil {
			return err
		}
		local, err := scheduler.LocalNow(now, p.Timezone)
		if err != nil {
			return err
		}
		slotID, err := q.InsertSlot(ctx, db.InsertSlotParams{ParentID: id, LocalDate: local.PGDate()})
		if errors.Is(err, pgx.ErrNoRows) {
			var slot db.CallSlot
			if err := tx.QueryRow(ctx, `SELECT id, status FROM call_slots WHERE parent_id = $1 AND local_date = $2`, id, local.PGDate()).Scan(&slot.ID, &slot.Status); err != nil {
				return err
			}
			if slot.Status != "pending" {
				msgErr = "Today's call is already " + slot.Status + "; a test call can run tomorrow."
				return nil
			}
			slotID = slot.ID
		} else if err != nil {
			return err
		}
		n, err := q.CountAttemptsForSlot(ctx, slotID)
		if err != nil {
			return err
		}
		if n >= calls.MaxAttempts {
			msgErr = "Today's slot has used all its attempts."
			return nil
		}
		var open int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM calls WHERE slot_id = $1 AND status IN ('scheduled', 'dialing', 'ringing', 'in_progress')`, slotID).Scan(&open); err != nil {
			return err
		}
		if open > 0 {
			msgErr = "A call for today is already scheduled or in progress."
			return nil
		}
		call, err := q.InsertCallAttempt(ctx, db.InsertCallAttemptParams{SlotID: slotID, ParentID: id, AttemptNo: n + 1, ScheduledFor: now, Provider: h.Calls.Voice.Name()})
		if err != nil {
			return err
		}
		if _, _, err := enqueue(ctx, tx, scheduler.PlaceCallSpec(id, call.ID, now)); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Admin(Admin(ctx)), "test_call", "parent", id.String(), audit.Details{"call_id": call.ID.String()})
	})
	if err != nil {
		h.fail(w, r, "could not schedule test call", err)
		return
	}
	if msgErr != "" {
		redirect(w, r, back, "", msgErr)
		return
	}
	redirect(w, r, back, "Test call scheduled. It will be cancelled if any guard fails (consent, window, CALLS_ENABLED, allowlist).", "")
}
