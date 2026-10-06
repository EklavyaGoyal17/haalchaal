//go:build integration

package sim_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/EklavyaGoyal17/haalchaal/internal/app"
	"github.com/EklavyaGoyal17/haalchaal/internal/clock"
	"github.com/EklavyaGoyal17/haalchaal/internal/config"
	"github.com/EklavyaGoyal17/haalchaal/internal/httpapi"
	"github.com/EklavyaGoyal17/haalchaal/internal/safety"
	"github.com/EklavyaGoyal17/haalchaal/internal/testdb"
	"github.com/EklavyaGoyal17/haalchaal/internal/voice/fake"
)

// TestLoad: 1000 parents, one tick, 4 workers dial everyone, then 3000
// webhook events (ringing, answered, completed) arrive concurrently, each
// delivered twice. Everything must settle exactly once, within budget.
func TestLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("load test")
	}
	const parents = 1000
	pool := testdb.New(t)
	ctx := context.Background()
	var phones []string
	for i := range parents {
		p := fmt.Sprintf("+9197%08d", i)
		phones = append(phones, p)
		testdb.CreateFamily(t, pool, testdb.Family{Phone: p})
	}
	clk := clock.NewFake(time.Date(2026, 10, 6, 4, 30, 0, 0, time.UTC))
	v := fake.New("load-secret")
	gate := safety.Gate{Env: config.EnvDev, CallsEnabled: true, DevAllowlist: phones}
	a, err := app.New(testConfig(t), pool, testdb.Log(), app.Options{Clock: clk, Voice: v, Gate: &gate})
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	res, err := a.Scheduler.Tick(ctx)
	if err != nil || res.Created != parents {
		t.Fatalf("tick: %+v %v", res, err)
	}
	tickDur := time.Since(start)

	start = time.Now()
	var wg sync.WaitGroup
	for w := range 4 {
		worker := a.NewWorker()
		wg.Go(func() {
			for {
				ran, err := worker.RunOnce(ctx, fmt.Sprintf("load-%d", w))
				if err != nil {
					t.Error(err)
					return
				}
				if !ran {
					return
				}
			}
		})
	}
	wg.Wait()
	dialDur := time.Since(start)
	if n := len(v.Calls()); n != parents {
		t.Fatalf("dialled %d, want %d", n, parents)
	}

	h := (&httpapi.Server{Log: testdb.Log(), Voice: a.Voice, Calls: a.Calls, MaxInFlight: 1000}).Handler()
	srv := httptest.NewServer(h)
	defer srv.Close()
	start = time.Now()
	sem := make(chan struct{}, 32)
	var mu sync.Mutex
	codes := map[int]int{}
	for i, c := range v.Calls() {
		pcid, _ := v.ProviderCallID(c.CallID)
		for round := range 2 { // every delivery repeated
			for j, typ := range []string{"ringing", "answered", "completed"} {
				body, _ := json.Marshal(map[string]any{"events": []fake.WireEvent{{
					EventID: fmt.Sprintf("load-%d-%d", i, j), ProviderCallID: pcid, Type: typ, At: clk.Now(), DurationSec: 120,
				}}})
				sem <- struct{}{}
				wg.Go(func() {
					defer func() { <-sem }()
					req, _ := http.NewRequest("POST", srv.URL+"/v1/webhooks/voice/fake", bytes.NewReader(body))
					req.Header.Set(fake.SignatureHeader, v.Sign(body))
					resp, err := http.DefaultClient.Do(req)
					if err != nil {
						t.Error(err)
						return
					}
					resp.Body.Close()
					mu.Lock()
					codes[resp.StatusCode]++
					mu.Unlock()
				})
				_ = round
			}
		}
	}
	wg.Wait()
	hookDur := time.Since(start)
	if codes[200] != parents*6 {
		t.Fatalf("status codes %v", codes)
	}
	var completed, events int
	_ = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM calls WHERE status = 'completed'), (SELECT count(*) FROM webhook_events)`).Scan(&completed, &events)
	if completed != parents || events != parents*3 {
		t.Fatalf("completed %d events %d", completed, events)
	}
	t.Logf("tick %v for %d parents; dial %v with 4 workers; %d webhook requests in %v (%.0f req/s)",
		tickDur, parents, dialDur, parents*6, hookDur, float64(parents*6)/hookDur.Seconds())
	if tickDur > 30*time.Second || dialDur > 60*time.Second || hookDur > 60*time.Second {
		t.Fatal("too slow")
	}
}
