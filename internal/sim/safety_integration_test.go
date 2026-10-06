//go:build integration

package sim_test

import (
	"context"
	"strings"
	"testing"

	"github.com/EklavyaGoyal17/haalchaal/internal/extract"
	"github.com/EklavyaGoyal17/haalchaal/internal/sim"
	"github.com/EklavyaGoyal17/haalchaal/internal/testdb"
)

// leakyLLM is a model that ignores the rules: it repeats the private note
// in the family summary and in the messages for the family.
type leakyLLM struct{}

func (leakyLLM) Name() string { return "leaky" }
func (leakyLLM) Complete(context.Context, string) (string, error) {
	return `{"schema_version":"1","call_quality":"good","answered_by":"parent","mood":"good","sleep":"good","appetite":"normal",
	 "medicines":[],"pain":[],"red_flags":[],"scam_signals":[],
	 "call_preferences":{"stop_requested":false,"new_time_requested":null},
	 "messages_for_family":["Tell Rahul: I am worried about MONEY FOR THE LOAN every night"],
	 "follow_ups":[],"private_notes":["worried about money for the loan"],
	 "family_summary":"Kamla ji is well but is WORRIED ABOUT MONEY for the loan.","confidence":0.95}`, nil
}

// Safety test (CLAUDE.md): private notes never reach outbound messages, even
// when the model leaks them into the summary and the family messages.
func TestSafetyPrivateNotesNeverSent(t *testing.T) {
	pool := testdb.New(t)
	sc, err := sim.LoadByName(dir, "private_topic")
	if err != nil {
		t.Fatal(err)
	}
	r := &sim.Runner{Pool: pool, Config: testConfig(t), Log: testdb.Log(), Extractor: extract.ModelExtractor{LLM: leakyLLM{}}}
	res, err := r.Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) == 0 {
		t.Fatal("no summary sent at all")
	}
	for _, m := range res.Messages {
		low := strings.ToLower(m.Body)
		if strings.Contains(low, "money") || strings.Contains(low, "loan") {
			t.Errorf("private note leaked to %s via %s: %s", m.To, m.Template, m.Body)
		}
	}
}

// Safety test: with CALLS_ENABLED=false nothing is dialled and nothing is sent.
func TestSafetyNoContactWhenCallsDisabled(t *testing.T) {
	pool := testdb.New(t)
	sc, err := sim.LoadByName(dir, "fall_emergency")
	if err != nil {
		t.Fatal(err)
	}
	r := &sim.Runner{Pool: pool, Config: testConfig(t), Log: testdb.Log(), CallsDisabled: true}
	res, err := r.Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Dials != 0 || len(res.Messages) != 0 {
		t.Fatalf("dials %d, messages %d with CALLS_ENABLED=false", res.Dials, len(res.Messages))
	}
	if len(res.Attempts) != 1 || res.Attempts[0] != "cancelled" {
		t.Fatalf("attempts %v", res.Attempts)
	}
}
