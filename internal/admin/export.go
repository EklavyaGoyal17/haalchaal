package admin

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/EklavyaGoyal17/haalchaal/internal/alerts"
	"github.com/EklavyaGoyal17/haalchaal/internal/audit"
	"github.com/EklavyaGoyal17/haalchaal/internal/calls"
	"github.com/EklavyaGoyal17/haalchaal/internal/db"
	"github.com/EklavyaGoyal17/haalchaal/internal/outbound"
	"github.com/EklavyaGoyal17/haalchaal/internal/voice"
)

// exportDoc is everything held about one parent (SPEC §13 data rights).
type exportDoc struct {
	ExportedAt time.Time        `json:"exported_at"`
	Parent     map[string]any   `json:"parent"`
	Family     []map[string]any `json:"family_members"`
	Consents   []db.Consent     `json:"consents"`
	Medicines  []map[string]any `json:"medicines"`
	Calls      []map[string]any `json:"calls"`
	Alerts     []map[string]any `json:"alerts"`
	Memories   []map[string]any `json:"memories"`
	Inbound    []map[string]any `json:"messages_from_family"`
}

// export downloads every record about a parent as JSON, decrypted, and
// writes an audit row first.
func (h *Handler) export(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	q := db.New(h.Pool)
	p, err := q.GetParentWithAccount(ctx, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := audit.Write(ctx, h.Pool, audit.Admin(Admin(ctx)), "export_parent", "parent", id.String(), nil); err != nil {
		h.fail(w, r, "could not write audit log", err)
		return
	}
	dec := func(b []byte, aad string) string {
		if len(b) == 0 {
			return ""
		}
		s, err := h.Keyring.DecryptString(b, aad)
		if err != nil {
			return "<undecryptable>"
		}
		return s
	}
	doc := exportDoc{ExportedAt: h.Clock.Now()}
	doc.Parent = map[string]any{
		"id": p.ID, "preferred_name": p.PreferredName, "phone": p.PhoneE164, "language": p.Language, "timezone": p.Timezone,
		"call_time": pgClock(p.CallTimeLocal), "window_start": pgClock(p.WindowStart), "window_end": pgClock(p.WindowEnd),
		"status": p.Status, "interests": dec(p.InterestsEnc, calls.AADInterests), "safe_word": dec(p.SafeWordEnc, calls.AADSafeWord),
		"local_contact_name": p.LocalContactName, "local_contact_phone": p.LocalContactPhoneE164,
		"first_call_done_at": p.FirstCallDoneAt, "created_at": p.CreatedAt, "plan": p.Plan,
	}
	members, err := q.ListFamilyMembers(ctx, p.AccountID)
	if err != nil {
		h.fail(w, r, "export failed", err)
		return
	}
	for _, m := range members {
		doc.Family = append(doc.Family, map[string]any{"name": m.Name, "phone": m.PhoneE164, "relation": m.Relation,
			"language": m.Language, "priority": m.Priority, "whatsapp_opt_in_at": m.WhatsappOptInAt})
	}
	if doc.Consents, err = q.ListConsentsForParent(ctx, id); err != nil {
		h.fail(w, r, "export failed", err)
		return
	}
	meds, _ := q.ListAllMedicines(ctx, id)
	for _, m := range meds {
		doc.Medicines = append(doc.Medicines, map[string]any{"name": dec(m.NameEnc, calls.AADMedicineName), "timing": m.Timing, "active": m.Active})
	}
	cs, _ := q.ListAllCallsForParent(ctx, id)
	for _, c := range cs {
		entry := map[string]any{"id": c.ID, "attempt": c.AttemptNo, "scheduled_for": c.ScheduledFor, "status": c.Status,
			"started_at": c.StartedAt, "ended_at": c.EndedAt, "duration_sec": c.DurationSec, "end_reason": c.EndReason}
		if t, err := q.GetTranscript(ctx, c.ID); err == nil {
			plain, err := h.Keyring.Decrypt(t.TurnsEnc, calls.AADTranscript)
			if err == nil {
				turns, _ := voice.UnmarshalTurns(plain)
				entry["transcript"] = turns
			}
		}
		if rep, err := q.GetCallReport(ctx, c.ID); err == nil {
			if plain, err := h.Keyring.Decrypt(rep.ReportEnc, calls.AADReport); err == nil {
				entry["report"] = json.RawMessage(plain)
			}
		}
		doc.Calls = append(doc.Calls, entry)
	}
	as, _ := q.ListAlertsForParent(ctx, id)
	for _, a := range as {
		doc.Alerts = append(doc.Alerts, map[string]any{"type": a.Type, "category": a.Category, "source": a.Source, "status": a.Status,
			"detail": dec(a.DetailEnc, alerts.AADDetail), "created_at": a.CreatedAt, "acknowledged_at": a.AcknowledgedAt,
			"reviewed_by": a.ReviewedBy, "review_note": dec(a.ReviewNoteEnc, "alerts.review_note_enc")})
	}
	mems, _ := q.ListAllMemories(ctx, id)
	for _, m := range mems {
		doc.Memories = append(doc.Memories, map[string]any{"kind": m.Kind, "content": dec(m.ContentEnc, calls.AADMemory), "expires_at": m.ExpiresAt})
	}
	in, _ := q.ListInboundForAccount(ctx, p.AccountID)
	for _, m := range in {
		doc.Inbound = append(doc.Inbound, map[string]any{"received_at": m.ReceivedAt, "kind": m.Kind, "body": dec(m.BodyEnc, outbound.AADInbound)})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="haalchaal-export-%s.json"`, id.String()[:8]))
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(doc)
}

// erase anonymises a parent on request: content deleted, identity replaced,
// calls stopped. Counts and the audit trail remain.
func (h *Handler) erase(w http.ResponseWriter, r *http.Request) {
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
	if strings.TrimSpace(r.PostForm.Get("confirm")) != "ERASE "+id.String()[:8] {
		redirect(w, r, back, "", "Confirmation text did not match; nothing was erased.")
		return
	}
	now := h.Clock.Now()
	who := audit.Admin(Admin(ctx))
	err := pgx.BeginFunc(ctx, h.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err := calls.PauseParent(ctx, tx, id, "stopped", "erased", who, now); err != nil {
			return err
		}
		for _, f := range []func() error{
			func() error { return q.EraseTranscripts(ctx, id) },
			func() error { return q.EraseReports(ctx, id) },
			func() error { return q.EraseMemories(ctx, id) },
			func() error { return q.EraseMedicines(ctx, id) },
			func() error { return q.EraseAlertDetails(ctx, id) },
			func() error {
				return q.EraseParent(ctx, db.EraseParentParams{ID: id, PlaceholderPhone: placeholderPhone()})
			},
		} {
			if err := f(); err != nil {
				return err
			}
		}
		return audit.Write(ctx, tx, who, "erase_parent", "parent", id.String(), nil)
	})
	if err != nil {
		h.fail(w, r, "could not erase", err)
		return
	}
	redirect(w, r, back, "Parent erased.", "")
}

// placeholderPhone is a unique, never-dialable number in the unassigned +999
// country code range, so erased rows keep the E.164 and unique checks.
func placeholderPhone() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(1_000_000_000_000))
	return fmt.Sprintf("+999%012d", n.Int64())
}
