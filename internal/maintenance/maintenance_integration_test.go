//go:build integration

package maintenance_test

import (
	"context"
	"testing"
	"time"

	"github.com/EklavyaGoyal17/haalchaal/internal/calls"
	"github.com/EklavyaGoyal17/haalchaal/internal/clock"
	"github.com/EklavyaGoyal17/haalchaal/internal/crypto"
	"github.com/EklavyaGoyal17/haalchaal/internal/db"
	"github.com/EklavyaGoyal17/haalchaal/internal/jobs"
	"github.com/EklavyaGoyal17/haalchaal/internal/maintenance"
	"github.com/EklavyaGoyal17/haalchaal/internal/testdb"
	"github.com/EklavyaGoyal17/haalchaal/internal/voice/fake"
)

var t0 = time.Date(2026, 10, 6, 5, 0, 0, 0, time.UTC)

func TestRetention(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	fam := testdb.CreateFamily(t, pool, testdb.Family{})
	k, _ := crypto.GenerateKey()
	kr, _ := crypto.ParseKeyring("1:"+k, "1")
	enc, _ := kr.EncryptString("x", calls.AADTranscript)

	var slot string
	_ = pool.QueryRow(ctx, `INSERT INTO call_slots (parent_id, local_date) VALUES ($1, '2026-10-01') RETURNING id`, fam.ParentID).Scan(&slot)
	mk := func(deleteAfter time.Time) {
		var call string
		if err := pool.QueryRow(ctx, `INSERT INTO calls (slot_id, parent_id, attempt_no, scheduled_for, provider, status)
			VALUES ($1, $2, (SELECT count(*)+1 FROM calls WHERE slot_id = $1), $3, 'fake', 'completed') RETURNING id`, slot, fam.ParentID, t0).Scan(&call); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO transcripts (call_id, turns_enc, delete_after) VALUES ($1, $2, $3)`, call, enc, deleteAfter); err != nil {
			t.Fatal(err)
		}
	}
	mk(t0.Add(-time.Hour)) // expired
	mk(t0.Add(time.Hour))  // kept
	q := db.New(pool)
	past, future := t0.Add(-time.Minute), t0.Add(time.Hour)
	for _, exp := range []*time.Time{&past, &future} {
		if _, err := q.InsertMemory(ctx, db.InsertMemoryParams{ParentID: fam.ParentID, Kind: "follow_up", ContentEnc: enc, ExpiresAt: exp, CreatedAt: t0}); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = pool.Exec(ctx, `INSERT INTO webhook_events (provider, event_id, received_at) VALUES ('fake', 'old', $1), ('fake', 'new', $2)`, t0.AddDate(0, 0, -31), t0)
	_, _ = pool.Exec(ctx, `INSERT INTO audit_log (actor, action, entity, entity_id, at) VALUES ('system', 'x', 'y', 'z', $1)`, t0.AddDate(0, 0, -401))

	r := &maintenance.Retention{Pool: pool, Clock: clock.NewFake(t0), Log: testdb.Log(), Voice: fake.New("s"),
		TranscriptTTL: 365 * 24 * time.Hour, AuditTTL: 400 * 24 * time.Hour, Location: time.UTC}
	if err := r.Run(ctx, jobs.Job{}); err != nil {
		t.Fatal(err)
	}
	var transcripts, memories, events, oldAudit int
	_ = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM transcripts), (SELECT count(*) FROM memories), (SELECT count(*) FROM webhook_events),
		(SELECT count(*) FROM audit_log WHERE action = 'x')`).Scan(&transcripts, &memories, &events, &oldAudit)
	if transcripts != 1 || memories != 1 || events != 1 || oldAudit != 0 {
		t.Fatalf("after retention: transcripts %d memories %d events %d old audit %d", transcripts, memories, events, oldAudit)
	}
	// Scheduling twice on the same day enqueues one job.
	for range 2 {
		if err := r.Schedule(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = 'retention'`).Scan(&n)
	if n != 1 {
		t.Fatalf("retention jobs %d", n)
	}
}

func TestRotateKeys(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	fam := testdb.CreateFamily(t, pool, testdb.Family{})
	k1, _ := crypto.GenerateKey()
	k2, _ := crypto.GenerateKey()
	old, _ := crypto.ParseKeyring("1:"+k1, "1")
	q := db.New(pool)
	for _, name := range []string{"Amlodipine", "Metformin"} {
		enc, _ := old.EncryptString(name, calls.AADMedicineName)
		if _, err := q.CreateMedicine(ctx, db.CreateMedicineParams{ParentID: fam.ParentID, NameEnc: enc, Timing: "night"}); err != nil {
			t.Fatal(err)
		}
	}
	sw, _ := old.EncryptString("gulab", calls.AADSafeWord)
	_, _ = pool.Exec(ctx, `UPDATE parents SET safe_word_enc = $1`, sw)

	both, _ := crypto.ParseKeyring("1:"+k1+",2:"+k2, "2")
	n, err := maintenance.RotateKeys(ctx, pool, both, testdb.Log())
	if err != nil || n != 3 {
		t.Fatalf("rotated %d, err %v", n, err)
	}
	if n, _ := maintenance.RotateKeys(ctx, pool, both, testdb.Log()); n != 0 {
		t.Fatalf("second run rotated %d", n)
	}
	// Only the new key is needed now.
	onlyNew, _ := crypto.ParseKeyring("2:"+k2, "2")
	meds, _ := q.ListAllMedicines(ctx, fam.ParentID)
	for _, m := range meds {
		if _, err := onlyNew.DecryptString(m.NameEnc, calls.AADMedicineName); err != nil {
			t.Fatalf("medicine not readable with the new key: %v", err)
		}
	}
	var got []byte
	_ = pool.QueryRow(ctx, `SELECT safe_word_enc FROM parents`).Scan(&got)
	if s, err := onlyNew.DecryptString(got, calls.AADSafeWord); err != nil || s != "gulab" {
		t.Fatalf("safe word %q %v", s, err)
	}
}
