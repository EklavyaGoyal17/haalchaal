package alerts

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func keys(p StepPlan) []string {
	var out []string
	for _, t := range p.Targets {
		out = append(out, t.Key())
	}
	return out
}

func TestPlanStepEmergency(t *testing.T) {
	m1, m2 := uuid.New(), uuid.New()
	to := Timeouts{Emergency: 10 * time.Minute, Urgent: time.Hour}
	aud := Audience{Members: []uuid.UUID{m1, m2}, LocalContact: true, Admins: 2}
	want := [][]string{
		{"member:" + m1.String(), "admin:0:alert", "admin:1:alert"},
		{"member:" + m2.String()},
		{"local_contact"},
		{"admin:0:unacknowledged", "admin:1:unacknowledged"},
	}
	for step, w := range want {
		p := PlanStep(TypeEmergency, "fall", aud, step, to)
		if got := keys(p); !equal(got, w) {
			t.Errorf("step %d targets %v, want %v", step, got, w)
		}
		if last := step == len(want)-1; p.Last != last || (!last && p.Next != 10*time.Minute) {
			t.Errorf("step %d last=%v next=%v", step, p.Last, p.Next)
		}
	}
	if p := PlanStep(TypeEmergency, "fall", aud, 4, to); !p.Last || len(p.Targets) != 0 {
		t.Error("step past the end")
	}

	// One member, no local contact.
	aud = Audience{Members: []uuid.UUID{m1}, Admins: 1}
	if got := keys(PlanStep(TypeEmergency, "fall", aud, 1, to)); !equal(got, []string{"admin:0:unacknowledged"}) {
		t.Errorf("short chain step 1: %v", got)
	}
	// No opted-in members: admins still hear at once.
	aud = Audience{Admins: 1}
	if got := keys(PlanStep(TypeEmergency, "fall", aud, 0, to)); !equal(got, []string{"admin:0:alert"}) {
		t.Errorf("no members: %v", got)
	}
}

func TestPlanStepOthers(t *testing.T) {
	m1, m2 := uuid.New(), uuid.New()
	to := Timeouts{Emergency: 10 * time.Minute, Urgent: time.Hour}
	aud := Audience{Members: []uuid.UUID{m1, m2}, LocalContact: true, Admins: 1}
	tests := []struct {
		typ, cat string
		step     int
		want     []string
		last     bool
	}{
		{TypeUrgent, "distressed", 0, []string{"member:" + m1.String(), "admin:0:alert"}, false},
		{TypeUrgent, "distressed", 1, []string{"member:" + m2.String()}, true},
		{TypeScam, "agency_threat", 0, []string{"member:" + m1.String(), "admin:0:alert"}, false},
		{TypeScam, CategoryScamKeyword, 0, []string{"admin:0:review"}, true},
		{TypeUrgent, CategoryStopRequested, 0, []string{"member:" + m1.String(), "admin:0:alert"}, true},
		{TypeMissedCalls, CategoryMissedCalls, 0, []string{"member:" + m1.String()}, true},
		{TypeWatch, "low_mood", 0, nil, true},
	}
	for _, tt := range tests {
		p := PlanStep(tt.typ, tt.cat, aud, tt.step, to)
		if got := keys(p); !equal(got, tt.want) || p.Last != tt.last {
			t.Errorf("%s/%s step %d: %v last=%v, want %v last=%v", tt.typ, tt.cat, tt.step, got, p.Last, tt.want, tt.last)
		}
	}
	if p := PlanStep(TypeUrgent, "distressed", Audience{Members: []uuid.UUID{m1}}, 0, to); !p.Last {
		t.Error("urgent with one member should stop after step 0")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
