package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/EklavyaGoyal17/haalchaal/internal/audit"
	"github.com/EklavyaGoyal17/haalchaal/internal/calls"
	"github.com/EklavyaGoyal17/haalchaal/internal/db"
	"github.com/EklavyaGoyal17/haalchaal/internal/domain"
	"github.com/EklavyaGoyal17/haalchaal/internal/scheduler"
)

const (
	formMembers   = 3
	formMedicines = 5
)

type memberForm struct {
	Name, Phone, Relation, Language string
	OptIn                           bool
}

type medForm struct{ Name, Timing string }

// onboardForm is the onboarding form's values, kept to re-render on errors.
type onboardForm struct {
	Plan, ParentName, ParentPhone, Language                   string
	CallTime, WindowStart, WindowEnd, Timezone                string
	Interests, SafeWord, LocalName, LocalPhone                string
	Members                                                   []memberForm
	Medicines                                                 []medForm
	ConsentCalls, ConsentData, ConsentShare, ConsentRecording bool
	GivenBy, TextVersion, EvidenceRef                         string
	Activate                                                  bool
}

func (h *Handler) blankForm() onboardForm {
	f := onboardForm{Plan: "daily", Language: "hi", CallTime: "10:00", WindowStart: "09:00", WindowEnd: "20:00",
		Timezone: h.Location.String(), GivenBy: "parent", Activate: true}
	f.Members = make([]memberForm, formMembers)
	for i := range f.Members {
		f.Members[i].Language = "en"
		f.Members[i].OptIn = i == 0
	}
	f.Medicines = make([]medForm, formMedicines)
	return f
}

func (h *Handler) onboardForm(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "onboard", "Onboard a family", map[string]any{"F": h.blankForm(), "Problems": nil})
}

func readForm(r *http.Request) onboardForm {
	g := func(k string) string { return strings.TrimSpace(r.PostForm.Get(k)) }
	f := onboardForm{
		Plan: g("plan"), ParentName: g("parent_name"), ParentPhone: g("parent_phone"), Language: g("language"),
		CallTime: g("call_time"), WindowStart: g("window_start"), WindowEnd: g("window_end"), Timezone: g("timezone"),
		Interests: g("interests"), SafeWord: g("safe_word"), LocalName: g("local_name"), LocalPhone: g("local_phone"),
		ConsentCalls: g("consent_calls") == "yes", ConsentData: g("consent_data") == "yes", ConsentShare: g("consent_share") == "yes",
		ConsentRecording: g("consent_recording") == "yes", GivenBy: g("given_by"), TextVersion: g("text_version"),
		EvidenceRef: g("evidence_ref"), Activate: g("activate") == "yes",
	}
	names, phones, rels, langs := r.PostForm["member_name"], r.PostForm["member_phone"], r.PostForm["member_relation"], r.PostForm["member_language"]
	for i := range formMembers {
		m := memberForm{Language: "en"}
		if i < len(names) {
			m.Name = strings.TrimSpace(names[i])
		}
		if i < len(phones) {
			m.Phone = strings.TrimSpace(phones[i])
		}
		if i < len(rels) {
			m.Relation = strings.TrimSpace(rels[i])
		}
		if i < len(langs) && langs[i] == "hi" {
			m.Language = "hi"
		}
		m.OptIn = r.PostForm.Get(fmt.Sprintf("member_optin_%d", i)) == "yes"
		f.Members = append(f.Members, m)
	}
	mn, mt := r.PostForm["med_name"], r.PostForm["med_timing"]
	for i := range formMedicines {
		var m medForm
		if i < len(mn) {
			m.Name = strings.TrimSpace(mn[i])
		}
		if i < len(mt) {
			m.Timing = strings.TrimSpace(mt[i])
		}
		f.Medicines = append(f.Medicines, m)
	}
	return f
}

// validated is a checked, normalised onboarding request.
type validated struct {
	f         onboardForm
	parentPh  string
	localPh   string
	memberPh  []string
	interests []string
	tz        *time.Location
}

func validate(f onboardForm) (validated, []string) {
	v := validated{f: f}
	var p []string
	bad := func(msg string) { p = append(p, msg) }
	if f.Plan != scheduler.PlanDaily && f.Plan != scheduler.PlanBasic {
		bad("Choose a plan.")
	}
	if f.ParentName == "" || len([]rune(f.ParentName)) > 60 {
		bad("Give the name the agent should use (up to 60 characters).")
	}
	var ok bool
	if v.parentPh, ok = domain.NormalizePhone(f.ParentPhone); !ok {
		bad("The parent's phone number is not valid.")
	}
	if f.Language != "hi" && f.Language != "ta" && f.Language != "en" {
		bad("Choose a language.")
	}
	ct, ok1 := parseClock(f.CallTime)
	ws, ok2 := parseClock(f.WindowStart)
	we, ok3 := parseClock(f.WindowEnd)
	if !ok1 || !ok2 || !ok3 {
		bad("Times must look like 10:00.")
	} else if w, _ := scheduler.WindowFromPG(ct, ws, we); !w.Valid() {
		bad("The call window must start before it ends, and the call time must be before the window ends.")
	}
	tz, err := time.LoadLocation(f.Timezone)
	if err != nil || f.Timezone == "" {
		bad("Unknown timezone (use names like Asia/Kolkata).")
	}
	v.tz = tz
	for _, s := range strings.Split(f.Interests, ",") {
		if s = strings.TrimSpace(s); s != "" {
			v.interests = append(v.interests, s)
		}
	}
	if len(v.interests) > 3 {
		bad("At most 3 interests.")
	}
	if len([]rune(f.SafeWord)) > 40 {
		bad("The code word is too long.")
	}
	if f.LocalPhone != "" || f.LocalName != "" {
		if f.LocalName == "" {
			bad("Give the local contact's name.")
		}
		if v.localPh, ok = domain.NormalizePhone(f.LocalPhone); !ok {
			bad("The local contact's phone number is not valid.")
		}
	}
	seen := map[string]bool{}
	members := 0
	for i, m := range f.Members {
		if m.Name == "" && m.Phone == "" {
			v.memberPh = append(v.memberPh, "")
			continue
		}
		members++
		ph, ok := domain.NormalizePhone(m.Phone)
		if m.Name == "" || !ok {
			bad(fmt.Sprintf("Family member %d needs a name and a valid phone number.", i+1))
		}
		if seen[ph] {
			bad(fmt.Sprintf("Family member %d has the same number as another member.", i+1))
		}
		if ph == v.parentPh {
			bad(fmt.Sprintf("Family member %d has the parent's number.", i+1))
		}
		seen[ph] = true
		v.memberPh = append(v.memberPh, ph)
	}
	if members == 0 {
		bad("Add at least one family member.")
	}
	if f.Members[0].Name == "" {
		bad("The first family member is the primary contact; fill in that row first.")
	}
	for i, m := range f.Medicines {
		if (m.Name == "") != (m.Timing == "") {
			bad(fmt.Sprintf("Medicine %d needs both a name and a timing.", i+1))
		}
		if len(m.Name) > 80 || len(m.Timing) > 40 {
			bad(fmt.Sprintf("Medicine %d is too long.", i+1))
		}
	}
	anyConsent := f.ConsentCalls || f.ConsentData || f.ConsentShare || f.ConsentRecording
	if anyConsent && (f.TextVersion == "" || len(f.TextVersion) > 40 || (f.GivenBy != "parent" && f.GivenBy != "guardian")) {
		bad("Give the consent script version and who gave consent.")
	}
	if f.Activate && !(f.ConsentCalls && f.ConsentData && f.ConsentShare) {
		bad("Calls can only start with the calls, data processing and family sharing consents.")
	}
	return v, p
}

func (h *Handler) onboardSubmit(w http.ResponseWriter, r *http.Request) {
	if !h.checkForm(w, r) {
		return
	}
	ctx := r.Context()
	f := readForm(r)
	v, problems := validate(f)
	if len(problems) > 0 {
		w.WriteHeader(http.StatusUnprocessableEntity)
		h.render(w, r, "onboard", "Onboard a family", map[string]any{"F": f, "Problems": problems})
		return
	}
	parentID, err := h.createFamily(ctx, v, Admin(ctx))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		w.WriteHeader(http.StatusUnprocessableEntity)
		h.render(w, r, "onboard", "Onboard a family", map[string]any{"F": f, "Problems": []string{"That parent's phone number is already registered."}})
		return
	}
	if err != nil {
		h.fail(w, r, "could not create the family", err)
		return
	}
	redirect(w, r, "/admin/parents/"+parentID.String(), "Family created.", "")
}

// createFamily writes everything in one transaction, with audit rows.
func (h *Handler) createFamily(ctx context.Context, v validated, admin string) (uuid.UUID, error) {
	f := v.f
	now := h.Clock.Now()
	who := audit.Admin(admin)
	var parentID uuid.UUID
	err := pgx.BeginFunc(ctx, h.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		acc, err := q.CreateAccount(ctx, db.CreateAccountParams{Plan: f.Plan})
		if err != nil {
			return err
		}
		prio := int32(0)
		for i, m := range f.Members {
			if m.Name == "" {
				continue
			}
			prio++
			rel := m.Relation
			var optIn *time.Time
			if m.OptIn {
				optIn = &now
			}
			if _, err := q.CreateFamilyMember(ctx, db.CreateFamilyMemberParams{
				AccountID: acc.ID, Name: m.Name, PhoneE164: v.memberPh[i], Relation: &rel, Language: m.Language,
				Priority: prio, WhatsappOptInAt: optIn,
			}); err != nil {
				return err
			}
		}
		ct, _ := parseClock(f.CallTime)
		ws, _ := parseClock(f.WindowStart)
		we, _ := parseClock(f.WindowEnd)
		params := db.CreateParentParams{
			AccountID: acc.ID, PreferredName: f.ParentName, PhoneE164: v.parentPh, Language: f.Language,
			Timezone: v.tz.String(), CallTimeLocal: ct, WindowStart: ws, WindowEnd: we,
		}
		if len(v.interests) > 0 {
			raw, _ := json.Marshal(v.interests)
			if params.InterestsEnc, err = h.Keyring.Encrypt(raw, calls.AADInterests); err != nil {
				return err
			}
		}
		if f.SafeWord != "" {
			if params.SafeWordEnc, err = h.Keyring.EncryptString(f.SafeWord, calls.AADSafeWord); err != nil {
				return err
			}
		}
		if v.localPh != "" {
			params.LocalContactName, params.LocalContactPhoneE164 = &f.LocalName, &v.localPh
		}
		p, err := q.CreateParent(ctx, params)
		if err != nil {
			return err
		}
		parentID = p.ID
		for _, m := range f.Medicines {
			if m.Name == "" {
				continue
			}
			enc, err := h.Keyring.EncryptString(m.Name, calls.AADMedicineName)
			if err != nil {
				return err
			}
			if _, err := q.CreateMedicine(ctx, db.CreateMedicineParams{ParentID: p.ID, NameEnc: enc, Timing: m.Timing}); err != nil {
				return err
			}
		}
		ev := f.EvidenceRef
		for kind, given := range map[string]bool{"calls": f.ConsentCalls, "data_processing": f.ConsentData, "share_with_family": f.ConsentShare, "recording": f.ConsentRecording} {
			if !given {
				continue
			}
			if _, err := q.RecordConsent(ctx, db.RecordConsentParams{
				ParentID: p.ID, Kind: kind, GivenBy: f.GivenBy, Method: "in_person", TextVersion: f.TextVersion, EvidenceRef: &ev, GivenAt: now,
			}); err != nil {
				return err
			}
			if err := audit.Write(ctx, tx, who, "record_consent", "parent", p.ID.String(), audit.Details{"kind": kind, "given_by": f.GivenBy, "method": "in_person"}); err != nil {
				return err
			}
		}
		if f.Activate {
			if err := q.SetParentStatus(ctx, db.SetParentStatusParams{ID: p.ID, Status: "active"}); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE accounts SET status = 'active' WHERE id = $1`, acc.ID); err != nil {
				return err
			}
		}
		return audit.Write(ctx, tx, who, "onboard_family", "parent", p.ID.String(), audit.Details{"account_id": acc.ID.String(), "activated": fmt.Sprint(f.Activate)})
	})
	return parentID, err
}
