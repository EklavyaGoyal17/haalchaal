//go:build integration

package sim_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/EklavyaGoyal17/haalchaal/internal/config"
	"github.com/EklavyaGoyal17/haalchaal/internal/sim"
	"github.com/EklavyaGoyal17/haalchaal/internal/testdb"
)

const dir = "../../testdata/transcripts"

func testConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, _, err := config.Load(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// TestScenarios replays every scenario file and checks its expectations,
// including that replaying the same events changes nothing.
func TestScenarios(t *testing.T) {
	pool := testdb.New(t)
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no scenarios: %v", err)
	}
	r := &sim.Runner{Pool: pool, Config: testConfig(t), Log: testdb.Log()}
	for _, f := range files {
		sc, err := sim.Load(f)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(sc.Name, func(t *testing.T) {
			res, err := r.Run(context.Background(), sc)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range sim.Check(sc.Expected, res) {
				t.Error(m)
			}
		})
	}
}

func TestNoAnswerGivesThreeAttemptsAndOneAlert(t *testing.T) {
	pool := testdb.New(t)
	sc, err := sim.LoadByName(dir, "no_answer")
	if err != nil {
		t.Fatal(err)
	}
	res, err := (&sim.Runner{Pool: pool, Config: testConfig(t), Log: testdb.Log()}).Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Attempts) != 3 || res.Dials != 3 || len(res.Alerts) != 1 || !res.ReplayUnchanged {
		t.Fatalf("result %+v", res)
	}
}

func TestSimRefusesProd(t *testing.T) {
	cfg := config.Config{AppEnv: config.EnvProd}
	if _, err := (&sim.Runner{Config: cfg, Log: testdb.Log()}).Run(context.Background(), sim.Scenario{}); err == nil {
		t.Fatal("ran in prod")
	}
}
