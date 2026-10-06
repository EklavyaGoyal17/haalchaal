package alerts

import (
	"strconv"
	"time"

	"github.com/google/uuid"
)

// Recipient kinds (notifications.recipient_kind).
const (
	RecipientFamily       = "family"
	RecipientLocalContact = "local_contact"
	RecipientAdmin        = "admin"
)

// Target is one recipient of one escalation step.
type Target struct {
	Kind       string
	MemberID   uuid.UUID // family only
	AdminIndex int       // admin only: index into ADMIN_ALERT_PHONES
	Purpose    string    // "alert", "unacknowledged" or "review"
}

// Key identifies a target in dedupe keys and job payloads; it holds no
// phone numbers.
func (t Target) Key() string {
	switch t.Kind {
	case RecipientFamily:
		return "member:" + t.MemberID.String()
	case RecipientLocalContact:
		return "local_contact"
	default:
		return "admin:" + strconv.Itoa(t.AdminIndex) + ":" + t.Purpose
	}
}

// Audience is who can be reached for an alert.
type Audience struct {
	Members      []uuid.UUID // opted-in family members, by priority
	LocalContact bool
	Admins       int
}

// Timeouts are the acknowledgement windows.
type Timeouts struct {
	Emergency time.Duration // ALERT_ACK_TIMEOUT
	Urgent    time.Duration // URGENT_ACK_TIMEOUT, also for scams
}

// StepPlan is what one escalation step does.
type StepPlan struct {
	Targets []Target
	Next    time.Duration // wait before the next step
	Last    bool
}

// PlanStep returns escalation step `step` for an alert (SPEC §9):
//
//   - emergency: priority 1 and all admins at once; then each further member,
//     then the local contact, each after ALERT_ACK_TIMEOUT; finally an
//     "unacknowledged emergency" message to admins.
//   - urgent and scam: priority 1 and admins; after URGENT_ACK_TIMEOUT the
//     next member.
//   - unconfirmed scam keyword: admins only, for review.
//   - stop requested: priority 1 (calls paused) and admins.
//   - missed calls: priority 1.
//   - watch: nobody; it is mentioned in the next summary.
func PlanStep(alertType, category string, a Audience, step int, t Timeouts) StepPlan {
	admins := func(purpose string) []Target {
		out := make([]Target, a.Admins)
		for i := range out {
			out[i] = Target{Kind: RecipientAdmin, AdminIndex: i, Purpose: purpose}
		}
		return out
	}
	member := func(i int) []Target {
		if i < len(a.Members) {
			return []Target{{Kind: RecipientFamily, MemberID: a.Members[i], Purpose: "alert"}}
		}
		return nil
	}

	switch {
	case alertType == TypeWatch:
		return StepPlan{Last: true}
	case alertType == TypeMissedCalls:
		if step == 0 {
			return StepPlan{Targets: member(0), Last: true}
		}
		return StepPlan{Last: true}
	case category == CategoryScamKeyword:
		if step == 0 {
			return StepPlan{Targets: admins("review"), Last: true}
		}
		return StepPlan{Last: true}
	case category == CategoryStopRequested:
		if step == 0 {
			return StepPlan{Targets: append(member(0), admins("alert")...), Last: true}
		}
		return StepPlan{Last: true}
	case alertType == TypeEmergency:
		// Steps: 0 = member 0 + admins, 1..n-1 = members, n = local contact
		// (when set), then the admins' unacknowledged message.
		n := max(len(a.Members), 1)
		switch {
		case step == 0:
			return StepPlan{Targets: append(member(0), admins("alert")...), Next: t.Emergency}
		case step < n:
			return StepPlan{Targets: member(step), Next: t.Emergency}
		case step == n && a.LocalContact:
			return StepPlan{Targets: []Target{{Kind: RecipientLocalContact, Purpose: "alert"}}, Next: t.Emergency}
		case step == n || (step == n+1 && a.LocalContact):
			return StepPlan{Targets: admins("unacknowledged"), Last: true}
		}
		return StepPlan{Last: true}
	default: // urgent and scam
		switch step {
		case 0:
			last := len(a.Members) < 2
			return StepPlan{Targets: append(member(0), admins("alert")...), Next: t.Urgent, Last: last}
		case 1:
			return StepPlan{Targets: member(1), Last: true}
		}
		return StepPlan{Last: true}
	}
}
