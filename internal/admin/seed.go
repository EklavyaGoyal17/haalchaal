package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// demoFamilies are made-up families for trying the admin pages in dev. The
// numbers use the +91 99999 0xxxx range so they are easy to spot; nothing is
// dialled in dev unless CALLS_ENABLED=true and the number is allowlisted.
func demoFamilies() []onboardForm {
	member := func(name, phone, rel, lang string, optIn bool) memberForm {
		return memberForm{Name: name, Phone: phone, Relation: rel, Language: lang, OptIn: optIn}
	}
	base := func(name, phone, lang, callTime string) onboardForm {
		f := onboardForm{Plan: "daily", ParentName: name, ParentPhone: phone, Language: lang,
			CallTime: callTime, WindowStart: "09:00", WindowEnd: "20:00", Timezone: "Asia/Kolkata",
			ConsentCalls: true, ConsentData: true, ConsentShare: true,
			GivenBy: "parent", TextVersion: "demo-v1", EvidenceRef: "demo data", Activate: true}
		f.Members = make([]memberForm, formMembers)
		f.Medicines = make([]medForm, formMedicines)
		return f
	}

	kamla := base("Kamla ji", "+919999900001", "hi", "10:00")
	kamla.Interests, kamla.SafeWord = "bhajan, gardening", "tulsi"
	kamla.Members[0] = member("Rahul", "+919999900101", "son", "en", true)
	kamla.Members[1] = member("Priya", "+919999900102", "daughter", "hi", true)
	kamla.Medicines[0] = medForm{"Metformin 500", "after breakfast"}
	kamla.Medicines[1] = medForm{"Amlodipine 5", "night"}
	kamla.LocalName, kamla.LocalPhone = "Sharma ji (neighbour)", "+919999900201"

	raghavan := base("Raghavan sir", "+919999900002", "ta", "11:30")
	raghavan.Plan = "basic"
	raghavan.Interests = "cricket, Carnatic music"
	raghavan.Members[0] = member("Divya", "+919999900103", "daughter", "en", true)
	raghavan.Medicines[0] = medForm{"Atorvastatin 10", "night"}

	margaret := base("Margaret", "+919999900003", "en", "09:30")
	margaret.Interests = "baking, crosswords"
	margaret.Members[0] = member("Neil", "+919999900104", "son", "en", true)
	margaret.Members[1] = member("Anita", "+919999900105", "daughter-in-law", "en", false)

	// A parent still waiting for consent: shows how calls stay off.
	suresh := base("Suresh ji", "+919999900004", "hi", "17:00")
	suresh.ConsentShare, suresh.Activate = false, false
	suresh.Members[0] = member("Amit", "+919999900106", "son", "hi", true)
	suresh.Medicines[0] = medForm{"Thyroxine 50", "empty stomach, morning"}

	return []onboardForm{kamla, raghavan, margaret, suresh}
}

// SeedDemo creates the demo families that do not exist yet, through the same
// validation and transaction as the onboarding form, and returns how many it
// created. It is for dev only; the caller enforces that.
func (h *Handler) SeedDemo(ctx context.Context) (int, error) {
	created := 0
	for _, f := range demoFamilies() {
		v, problems := validate(f)
		if len(problems) > 0 {
			return created, fmt.Errorf("demo family %s: %s", f.ParentName, strings.Join(problems, " "))
		}
		_, err := h.createFamily(ctx, v, "demo-seed")
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			continue // already seeded
		}
		if err != nil {
			return created, fmt.Errorf("demo family %s: %w", f.ParentName, err)
		}
		created++
	}
	return created, nil
}
