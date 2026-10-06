package calls

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/EklavyaGoyal17/haalchaal/internal/config"
	"github.com/EklavyaGoyal17/haalchaal/internal/db"
	"github.com/EklavyaGoyal17/haalchaal/internal/safety"
)

func pgt(h, m int) pgtype.Time {
	return pgtype.Time{Microseconds: (time.Duration(h)*time.Hour + time.Duration(m)*time.Minute).Microseconds(), Valid: true}
}

const phone = "+919876541234"

func okState() db.GetCallGuardStateRow {
	return db.GetCallGuardStateRow{
		ParentStatus:  "active",
		AccountStatus: "active",
		PhoneE164:     phone,
		Timezone:      "Asia/Kolkata",
		CallTimeLocal: pgt(10, 0),
		WindowStart:   pgt(9, 0),
		WindowEnd:     pgt(20, 0),
		ConsentKinds:  []string{"calls", "data_processing", "share_with_family"},
	}
}

// 11:00 in Kolkata.
var inWindow = time.Date(2026, 10, 6, 5, 30, 0, 0, time.UTC)

func TestCheckGuards(t *testing.T) {
	devGate := safety.Gate{Env: config.EnvDev, CallsEnabled: true, DevAllowlist: []string{phone}}
	tests := []struct {
		name   string
		gate   safety.Gate
		mutate func(*db.GetCallGuardStateRow)
		now    time.Time
		want   string
	}{
		{"all good", devGate, nil, inWindow, ""},
		{"trial account ok", devGate, func(s *db.GetCallGuardStateRow) { s.AccountStatus = "trial" }, inWindow, ""},
		{"recording consent not required", devGate, nil, inWindow, ""},
		{"parent paused", devGate, func(s *db.GetCallGuardStateRow) { s.ParentStatus = "paused" }, inWindow, ReasonParentNotActive},
		{"parent stopped", devGate, func(s *db.GetCallGuardStateRow) { s.ParentStatus = "stopped" }, inWindow, ReasonParentNotActive},
		{"parent onboarding", devGate, func(s *db.GetCallGuardStateRow) { s.ParentStatus = "onboarding" }, inWindow, ReasonParentNotActive},
		{"account paused", devGate, func(s *db.GetCallGuardStateRow) { s.AccountStatus = "paused" }, inWindow, ReasonAccountNotActive},
		{"account cancelled", devGate, func(s *db.GetCallGuardStateRow) { s.AccountStatus = "cancelled" }, inWindow, ReasonAccountNotActive},
		{"no consents", devGate, func(s *db.GetCallGuardStateRow) { s.ConsentKinds = nil }, inWindow, ReasonConsentMissing},
		{"calls consent withdrawn", devGate, func(s *db.GetCallGuardStateRow) { s.ConsentKinds = []string{"data_processing", "share_with_family"} }, inWindow, ReasonConsentMissing},
		{"share consent missing", devGate, func(s *db.GetCallGuardStateRow) { s.ConsentKinds = []string{"calls", "data_processing", "recording"} }, inWindow, ReasonConsentMissing},
		{"before window", devGate, nil, time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC), ReasonOutsideWindow},
		{"at window end", devGate, nil, time.Date(2026, 10, 6, 14, 30, 0, 0, time.UTC), ReasonOutsideWindow},
		{"night", devGate, nil, time.Date(2026, 10, 6, 20, 0, 0, 0, time.UTC), ReasonOutsideWindow},
		{"bad timezone", devGate, func(s *db.GetCallGuardStateRow) { s.Timezone = "Nowhere/City" }, inWindow, ReasonBadSchedule},
		{"null window", devGate, func(s *db.GetCallGuardStateRow) { s.WindowEnd = pgtype.Time{} }, inWindow, ReasonBadSchedule},
		{"calls disabled", safety.Gate{Env: config.EnvDev, DevAllowlist: []string{phone}}, nil, inWindow, "calls_disabled"},
		{"not allowlisted", safety.Gate{Env: config.EnvDev, CallsEnabled: true}, nil, inWindow, "not_in_dev_allowlist"},
		{"prod without ack", safety.Gate{Env: config.EnvProd, CallsEnabled: true}, nil, inWindow, "telecom_compliance_not_acked"},
		{"prod with ack", safety.Gate{Env: config.EnvProd, CallsEnabled: true, ComplianceAck: true}, nil, inWindow, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := okState()
			if tt.mutate != nil {
				tt.mutate(&s)
			}
			if got := CheckGuards(tt.gate, s, tt.now); got != tt.want {
				t.Errorf("CheckGuards = %q, want %q", got, tt.want)
			}
		})
	}
}
