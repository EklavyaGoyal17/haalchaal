package sim

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/EklavyaGoyal17/haalchaal/internal/calls"
	"github.com/EklavyaGoyal17/haalchaal/internal/db"
)

type simPhones struct {
	parent  string
	members []string
	local   string
}

func (p simPhones) all() []string {
	out := append([]string{p.parent}, p.members...)
	if p.local != "" {
		out = append(out, p.local)
	}
	return out
}

func (rn *run) phones() simPhones {
	p := simPhones{parent: randomPhone()}
	for range rn.sc.Family {
		p.members = append(p.members, randomPhone())
	}
	if rn.sc.Parent.LocalContact != "" {
		p.local = randomPhone()
	}
	return p
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func pgTime(s string) (pgtype.Time, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return pgtype.Time{}, fmt.Errorf("time %q: %w", s, err)
	}
	d := time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute
	return pgtype.Time{Microseconds: d.Microseconds(), Valid: true}, nil
}

// createFamily inserts the scenario's account, members, parent, consents,
// medicines and follow-ups in one transaction, encrypting what must be.
func (rn *run) createFamily(ctx context.Context, ph simPhones) error {
	sp := rn.sc.Parent
	kr := rn.app.Keyring
	rn.tz = or(sp.Timezone, "Asia/Kolkata")
	return pgx.BeginFunc(ctx, rn.r.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		note := "simcall " + rn.sc.Name
		acc, err := q.CreateAccount(ctx, db.CreateAccountParams{Plan: or(sp.Plan, "daily"), BillingNote: &note})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE accounts SET status = 'active' WHERE id = $1`, acc.ID); err != nil {
			return err
		}
		optIn := rn.clk.Now().AddDate(0, -1, 0)
		for i, m := range rn.sc.Family {
			rel := m.Relation
			var opt *time.Time
			if !m.NoOptIn {
				opt = &optIn
			}
			if _, err := q.CreateFamilyMember(ctx, db.CreateFamilyMemberParams{
				AccountID: acc.ID, Name: m.Name, PhoneE164: ph.members[i], Relation: &rel,
				Language: or(m.Language, "en"), Priority: int32(i + 1), WhatsappOptInAt: opt,
			}); err != nil {
				return err
			}
		}
		callTime, err := pgTime(or(sp.CallTime, "10:00"))
		if err != nil {
			return err
		}
		ws, err := pgTime(or(sp.WindowStart, "09:00"))
		if err != nil {
			return err
		}
		we, err := pgTime(or(sp.WindowEnd, "20:00"))
		if err != nil {
			return err
		}
		params := db.CreateParentParams{
			AccountID: acc.ID, PreferredName: or(sp.PreferredName, "Kamla ji"), PhoneE164: ph.parent,
			Language: or(sp.Language, "hi"), Timezone: rn.tz, CallTimeLocal: callTime, WindowStart: ws, WindowEnd: we,
		}
		if len(sp.Interests) > 0 {
			raw, _ := json.Marshal(sp.Interests)
			if params.InterestsEnc, err = kr.Encrypt(raw, calls.AADInterests); err != nil {
				return err
			}
		}
		if sp.SafeWord != "" {
			if params.SafeWordEnc, err = kr.EncryptString(sp.SafeWord, calls.AADSafeWord); err != nil {
				return err
			}
		}
		if sp.LocalContact != "" {
			params.LocalContactName = &sp.LocalContact
			params.LocalContactPhoneE164 = &ph.local
		}
		p, err := q.CreateParent(ctx, params)
		if err != nil {
			return err
		}
		rn.parentID = p.ID
		if err := q.SetParentStatus(ctx, db.SetParentStatusParams{ID: p.ID, Status: "active"}); err != nil {
			return err
		}
		if sp.FirstCallDone {
			if err := q.MarkFirstCallDone(ctx, db.MarkFirstCallDoneParams{ID: p.ID, At: &optIn}); err != nil {
				return err
			}
		}
		for _, k := range calls.RequiredConsents {
			ev := "simcall"
			if _, err := q.RecordConsent(ctx, db.RecordConsentParams{
				ParentID: p.ID, Kind: k, GivenBy: "parent", Method: "in_person", TextVersion: "sim-v1",
				EvidenceRef: &ev, GivenAt: optIn,
			}); err != nil {
				return err
			}
		}
		for _, m := range rn.sc.Medicines {
			enc, err := kr.EncryptString(m.Name, calls.AADMedicineName)
			if err != nil {
				return err
			}
			if _, err := q.CreateMedicine(ctx, db.CreateMedicineParams{ParentID: p.ID, NameEnc: enc, Timing: m.Timing}); err != nil {
				return err
			}
		}
		exp := rn.clk.Now().Add(7 * 24 * time.Hour)
		for _, f := range rn.sc.FollowUps {
			enc, err := kr.EncryptString(f, calls.AADMemory)
			if err != nil {
				return err
			}
			if _, err := q.InsertMemory(ctx, db.InsertMemoryParams{ParentID: p.ID, Kind: "follow_up", ContentEnc: enc, ExpiresAt: &exp}); err != nil {
				return err
			}
		}
		return nil
	})
}
