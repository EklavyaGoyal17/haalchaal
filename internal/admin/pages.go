package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/EklavyaGoyal17/haalchaal/internal/alerts"
	"github.com/EklavyaGoyal17/haalchaal/internal/audit"
	"github.com/EklavyaGoyal17/haalchaal/internal/calls"
	"github.com/EklavyaGoyal17/haalchaal/internal/db"
	"github.com/EklavyaGoyal17/haalchaal/internal/jobs"
	"github.com/EklavyaGoyal17/haalchaal/internal/outbound"
	"github.com/EklavyaGoyal17/haalchaal/internal/voice"
)

func pathID(r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	return id, err == nil
}

// dashboard shows today's calls, open alerts and pilot metrics (SPEC §11).
func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := db.New(h.Pool)
	now := h.Clock.Now()
	local := now.In(h.Location)
	dayStart := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, h.Location)
	monthStart := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, h.Location)
	since := now.AddDate(0, 0, -30)

	var d struct {
		Today         []db.ListCallsBetweenRow
		AlertsByType  []db.OpenAlertsByTypeRow
		OpenAlerts    int32
		Pickup        []db.PickupRatesRow
		AvgSeconds    float64
		MedianSummary float64
		Cost          []db.CostPerAccountRow
	}
	var err error
	if d.Today, err = q.ListCallsBetween(ctx, db.ListCallsBetweenParams{FromAt: dayStart.UTC(), ToAt: dayStart.AddDate(0, 0, 1).UTC()}); err != nil {
		h.fail(w, r, "could not load calls", err)
		return
	}
	if d.AlertsByType, err = q.OpenAlertsByType(ctx); err != nil {
		h.fail(w, r, "could not load alerts", err)
		return
	}
	for _, a := range d.AlertsByType {
		d.OpenAlerts += a.N
	}
	if d.Pickup, err = q.PickupRates(ctx, pgtype.Date{Time: since, Valid: true}); err != nil {
		h.fail(w, r, "could not load pickup rates", err)
		return
	}
	if d.AvgSeconds, err = q.AverageCallSeconds(ctx, &since); err != nil {
		h.fail(w, r, "could not load call minutes", err)
		return
	}
	if d.MedianSummary, err = q.MedianSummaryMinutes(ctx, &since); err != nil {
		h.fail(w, r, "could not load summary latency", err)
		return
	}
	if d.Cost, err = q.CostPerAccount(ctx, monthStart.UTC()); err != nil {
		h.fail(w, r, "could not load costs", err)
		return
	}
	h.render(w, r, "dashboard", "Dashboard", d)
}

type alertView struct {
	db.ListOpenAlertsRow
	Detail string
}

type inboundView struct {
	db.ListUnhandledInboundRow
	From string
	Body string
}

// review lists open alerts, reports needing review and family messages.
// Decrypted alert details and messages are shown, so viewing is audited.
func (h *Handler) review(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := db.New(h.Pool)
	var d struct {
		Alerts  []alertView
		Reports []db.ListReportsToReviewRow
		Inbound []inboundView
	}
	as, err := q.ListOpenAlerts(ctx)
	if err != nil {
		h.fail(w, r, "could not load alerts", err)
		return
	}
	for _, a := range as {
		v := alertView{ListOpenAlertsRow: a}
		if len(a.DetailEnc) > 0 {
			v.Detail, _ = h.Keyring.DecryptString(a.DetailEnc, alerts.AADDetail)
		}
		d.Alerts = append(d.Alerts, v)
	}
	if d.Reports, err = q.ListReportsToReview(ctx); err != nil {
		h.fail(w, r, "could not load reports", err)
		return
	}
	in, err := q.ListUnhandledInbound(ctx)
	if err != nil {
		h.fail(w, r, "could not load messages", err)
		return
	}
	for _, m := range in {
		v := inboundView{ListUnhandledInboundRow: m, From: m.FromE164}
		if len(m.BodyEnc) > 0 {
			v.Body, _ = h.Keyring.DecryptString(m.BodyEnc, outbound.AADInbound)
		}
		d.Inbound = append(d.Inbound, v)
	}
	if err := audit.Write(ctx, h.Pool, audit.Admin(Admin(ctx)), "view_review_queue", "review", "queue", audit.Details{
		"alerts": itoa(len(d.Alerts)), "messages": itoa(len(d.Inbound)),
	}); err != nil {
		h.fail(w, r, "could not write audit log", err)
		return
	}
	h.render(w, r, "review", "Review queue", d)
}

type turnView struct {
	Speaker string
	Text    string
	Offset  string
}

type callAlertView struct {
	db.Alert
	Detail string
}

// callDetail shows a call's report and transcript. The audit row is written
// before anything is shown (rule 11).
func (h *Handler) callDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	q := db.New(h.Pool)
	c, err := q.GetCall(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.fail(w, r, "could not load call", err)
		return
	}
	if err := audit.Write(ctx, h.Pool, audit.Admin(Admin(ctx)), "view_call", "call", id.String(), audit.Details{"parent_id": c.ParentID.String()}); err != nil {
		h.fail(w, r, "could not write audit log", err)
		return
	}
	p, err := q.GetParent(ctx, c.ParentID)
	if err != nil {
		h.fail(w, r, "could not load parent", err)
		return
	}
	var d struct {
		Call        db.Call
		ParentName  string
		Report      string
		NeedsReview bool
		ReviewedBy  string
		Turns       []turnView
		Alerts      []callAlertView
	}
	d.Call, d.ParentName = c, p.PreferredName
	if rep, err := q.GetCallReport(ctx, id); err == nil {
		plain, err := h.Keyring.Decrypt(rep.ReportEnc, calls.AADReport)
		if err != nil {
			h.fail(w, r, "could not decrypt report", err)
			return
		}
		var pretty any
		if json.Unmarshal(plain, &pretty) == nil {
			b, _ := json.MarshalIndent(pretty, "", "  ")
			d.Report = string(b)
		}
		d.NeedsReview = rep.NeedsReview
		if rep.ReviewedBy != nil {
			d.ReviewedBy = *rep.ReviewedBy
		}
	}
	if tr, err := q.GetTranscript(ctx, id); err == nil {
		plain, err := h.Keyring.Decrypt(tr.TurnsEnc, calls.AADTranscript)
		if err != nil {
			h.fail(w, r, "could not decrypt transcript", err)
			return
		}
		turns, _ := voice.UnmarshalTurns(plain)
		for _, t := range turns {
			d.Turns = append(d.Turns, turnView{Speaker: t.Speaker, Text: t.Text, Offset: t.Offset.Truncate(time.Second).String()})
		}
	}
	as, err := q.ListAlertsForCall(ctx, &id)
	if err != nil {
		h.fail(w, r, "could not load alerts", err)
		return
	}
	for _, a := range as {
		v := callAlertView{Alert: a}
		if len(a.DetailEnc) > 0 {
			v.Detail, _ = h.Keyring.DecryptString(a.DetailEnc, alerts.AADDetail)
		}
		d.Alerts = append(d.Alerts, v)
	}
	h.render(w, r, "call", "Call", d)
}

func (h *Handler) markReviewed(w http.ResponseWriter, r *http.Request) {
	if !h.checkForm(w, r) {
		return
	}
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	now := h.Clock.Now()
	who := Admin(ctx)
	err := pgx.BeginFunc(ctx, h.Pool, func(tx pgx.Tx) error {
		if _, err := db.New(tx).MarkReportReviewed(ctx, db.MarkReportReviewedParams{CallID: id, ReviewedBy: &who, ReviewedAt: &now}); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Admin(who), "review_report", "call", id.String(), nil)
	})
	if err != nil {
		h.fail(w, r, "could not save", err)
		return
	}
	redirect(w, r, "/admin/calls/"+id.String(), "Report marked as reviewed.", "")
}

// resolveAlert closes an alert as resolved or a false alarm, with a note.
func (h *Handler) resolveAlert(w http.ResponseWriter, r *http.Request) {
	if !h.checkForm(w, r) {
		return
	}
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	status := r.PostForm.Get("status")
	note := strings.TrimSpace(r.PostForm.Get("note"))
	if (status != "resolved" && status != "false_alarm") || note == "" || len(note) > 500 {
		redirect(w, r, "/admin/review", "", "Choose resolved or false alarm and add a short note.")
		return
	}
	enc, err := h.Keyring.EncryptString(note, "alerts.review_note_enc")
	if err != nil {
		h.fail(w, r, "could not encrypt note", err)
		return
	}
	now := h.Clock.Now()
	who := Admin(ctx)
	var n int64
	err = pgx.BeginFunc(ctx, h.Pool, func(tx pgx.Tx) error {
		var err error
		if n, err = db.New(tx).ResolveAlert(ctx, db.ResolveAlertParams{ID: id, Status: status, ReviewedBy: &who, ReviewedAt: &now, ReviewNoteEnc: enc}); err != nil || n == 0 {
			return err
		}
		return audit.Write(ctx, tx, audit.Admin(who), "resolve_alert", "alert", id.String(), audit.Details{"status": status})
	})
	if err != nil {
		h.fail(w, r, "could not save", err)
		return
	}
	if n == 0 {
		redirect(w, r, "/admin/review", "", "That alert was already closed.")
		return
	}
	redirect(w, r, "/admin/review", "Alert closed.", "")
}

func (h *Handler) markInboundHandled(w http.ResponseWriter, r *http.Request) {
	if !h.checkForm(w, r) {
		return
	}
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	now := h.Clock.Now()
	err := pgx.BeginFunc(ctx, h.Pool, func(tx pgx.Tx) error {
		if _, err := db.New(tx).MarkInboundHandled(ctx, db.MarkInboundHandledParams{ID: id, HandledAt: &now}); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Admin(Admin(ctx)), "handle_inbound", "inbound_message", id.String(), nil)
	})
	if err != nil {
		h.fail(w, r, "could not save", err)
		return
	}
	redirect(w, r, "/admin/review", "Message marked as handled.", "")
}

func itoa(n int) string { return strconv.Itoa(n) }

var enqueue = jobs.Enqueue
