//go:build integration

package outbound_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EklavyaGoyal17/haalchaal/internal/alerts"
	"github.com/EklavyaGoyal17/haalchaal/internal/clock"
	"github.com/EklavyaGoyal17/haalchaal/internal/config"
	"github.com/EklavyaGoyal17/haalchaal/internal/crypto"
	"github.com/EklavyaGoyal17/haalchaal/internal/jobs"
	"github.com/EklavyaGoyal17/haalchaal/internal/notify"
	fakenotify "github.com/EklavyaGoyal17/haalchaal/internal/notify/fake"
	"github.com/EklavyaGoyal17/haalchaal/internal/outbound"
	"github.com/EklavyaGoyal17/haalchaal/internal/safety"
	"github.com/EklavyaGoyal17/haalchaal/internal/testdb"
)

const memberPhone = "+919900000001"

func setup(t *testing.T, gate safety.Gate) (*outbound.Service, *fakenotify.Messenger, testdb.Created) {
	t.Helper()
	pool := testdb.New(t)
	k, _ := crypto.GenerateKey()
	kr, _ := crypto.ParseKeyring("1:"+k, "1")
	m := fakenotify.New("s", "v", nil)
	s := &outbound.Service{Pool: pool, Clock: clock.NewFake(time.Date(2026, 10, 6, 5, 0, 0, 0, time.UTC)), Log: testdb.Log(),
		Keyring: kr, Messenger: m, Gate: gate, AdminPhones: []string{"+919900000099"},
		Timeouts: alerts.Timeouts{Emergency: 10 * time.Minute, Urgent: time.Hour}}
	fam := testdb.CreateFamily(t, pool, testdb.Family{MemberPhone: memberPhone})
	return s, m, fam
}

func allow() safety.Gate {
	return safety.Gate{Env: config.EnvDev, CallsEnabled: true, DevAllowlist: []string{memberPhone, "+919900000099"}}
}

func missedAlert(t *testing.T, s *outbound.Service, fam testdb.Created) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var slot uuid.UUID
	if err := s.Pool.QueryRow(ctx, `INSERT INTO call_slots (parent_id, local_date) VALUES ($1, '2026-10-06') RETURNING id`, fam.ParentID).Scan(&slot); err != nil {
		t.Fatal(err)
	}
	id, ok, err := alerts.RaiseMissedCalls(ctx, s.Pool, fam.ParentID, slot, s.Clock.Now())
	if err != nil || !ok {
		t.Fatal(err)
	}
	return id
}

func send(t *testing.T, s *outbound.Service, alertID uuid.UUID, recipient string) error {
	step := 0
	return s.SendAlert(context.Background(), jobs.Job{Payload: jobs.Payload{AlertID: &alertID, Step: &step, Recipient: recipient}})
}

func TestSendIsIdempotentAndRecorded(t *testing.T) {
	s, m, fam := setup(t, allow())
	id := missedAlert(t, s, fam)
	for range 3 {
		if err := send(t, s, id, "member:"+fam.MemberID.String()); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(m.Sent()); n != 1 {
		t.Fatalf("sent %d times", n)
	}
	var status, pmid string
	if err := s.Pool.QueryRow(context.Background(), `SELECT status, provider_message_id FROM notifications WHERE alert_id = $1`, id).Scan(&status, &pmid); err != nil {
		t.Fatal(err)
	}
	if status != "sent" || pmid != m.Sent()[0].ID {
		t.Fatalf("%s %s", status, pmid)
	}
}

func TestBlockedByGateIsRecordedNotRetried(t *testing.T) {
	s, m, fam := setup(t, safety.Gate{Env: config.EnvDev}) // CALLS_ENABLED=false
	id := missedAlert(t, s, fam)
	if err := send(t, s, id, "member:"+fam.MemberID.String()); err != nil {
		t.Fatalf("blocked send should not error: %v", err)
	}
	if len(m.Sent()) != 0 {
		t.Fatal("message sent with CALLS_ENABLED=false")
	}
	var status, reason string
	_ = s.Pool.QueryRow(context.Background(), `SELECT status, error FROM notifications WHERE alert_id = $1`, id).Scan(&status, &reason)
	if status != "failed" || reason != "blocked: calls_disabled" {
		t.Fatalf("%s %q", status, reason)
	}
}

func TestVendorErrorRetriesThenSendsOnce(t *testing.T) {
	s, m, fam := setup(t, allow())
	id := missedAlert(t, s, fam)
	m.FailNextSend(errors.New("graph down"))
	if err := send(t, s, id, "member:"+fam.MemberID.String()); err == nil {
		t.Fatal("vendor error swallowed")
	}
	if err := send(t, s, id, "member:"+fam.MemberID.String()); err != nil {
		t.Fatal(err)
	}
	if len(m.Sent()) != 1 {
		t.Fatalf("sent %d", len(m.Sent()))
	}
}

func TestMemberOfAnotherFamilyIsNotMessaged(t *testing.T) {
	s, m, fam := setup(t, allow())
	other := testdb.CreateFamily(t, s.Pool, testdb.Family{})
	id := missedAlert(t, s, fam)
	if err := send(t, s, id, "member:"+other.MemberID.String()); err != nil {
		t.Fatal(err)
	}
	if len(m.Sent()) != 0 {
		t.Fatal("messaged a member of another family")
	}
}

func TestInboundTextStoredEncryptedAndStatusesApplied(t *testing.T) {
	s, m, fam := setup(t, allow())
	ctx := context.Background()
	id := missedAlert(t, s, fam)
	if err := send(t, s, id, "member:"+fam.MemberID.String()); err != nil {
		t.Fatal(err)
	}
	wamid := m.Sent()[0].ID
	evs := []notify.InboundEvent{
		{EventID: "msg:1", Kind: notify.KindText, From: memberPhone, Payload: "Please resume Maa's calls", MessageID: "1"},
		{EventID: "status:" + wamid + ":read", Kind: notify.KindStatus, Payload: "read", MessageID: wamid},
		{EventID: "status:" + wamid + ":delivered", Kind: notify.KindStatus, Payload: "delivered", MessageID: wamid}, // late, must not go backwards
	}
	for _, ev := range evs {
		if _, err := s.HandleInbound(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	if res, _ := s.HandleInbound(ctx, evs[0]); res != outbound.InboundDuplicate {
		t.Fatalf("duplicate not detected: %s", res)
	}
	var body []byte
	var member *uuid.UUID
	if err := s.Pool.QueryRow(ctx, `SELECT body_enc, family_member_id FROM inbound_messages`).Scan(&body, &member); err != nil {
		t.Fatal(err)
	}
	if member == nil || *member != fam.MemberID || string(body) == "Please resume Maa's calls" {
		t.Fatal("inbound not linked or not encrypted")
	}
	if got, _ := s.Keyring.DecryptString(body, outbound.AADInbound); got != "Please resume Maa's calls" {
		t.Fatalf("decrypted %q", got)
	}
	var status string
	_ = s.Pool.QueryRow(ctx, `SELECT status FROM notifications WHERE provider_message_id = $1`, wamid).Scan(&status)
	if status != "read" {
		t.Fatalf("status %s", status)
	}
}
