package config

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	c, warns, err := Load(env(nil))
	if err != nil {
		t.Fatalf("Load with empty env: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if c.AppEnv != EnvDev || c.HTTPAddr != ":8080" {
		t.Errorf("AppEnv/HTTPAddr = %q/%q", c.AppEnv, c.HTTPAddr)
	}
	if c.CallsEnabled || c.TelecomComplianceAck {
		t.Error("safety switches must default to false")
	}
	if c.VoiceProvider != "fake" || c.WhatsAppProvider != "fake" || c.LLMProvider != "fake" {
		t.Error("vendors must default to fake")
	}
	if got := c.RetryOffsets; len(got) != 2 || got[0] != 15*time.Minute || got[1] != 45*time.Minute {
		t.Errorf("RetryOffsets = %v", got)
	}
	if c.AlertAckTimeout != 10*time.Minute || c.UrgentAckTimeout != 60*time.Minute || c.FollowUpTTL != 168*time.Hour {
		t.Errorf("timeouts = %v %v %v", c.AlertAckTimeout, c.UrgentAckTimeout, c.FollowUpTTL)
	}
	if c.RetentionTranscriptDays != 365 || c.RetentionAuditDays != 400 {
		t.Errorf("retention = %d %d", c.RetentionTranscriptDays, c.RetentionAuditDays)
	}
	if c.DefaultTimezone.String() != "Asia/Kolkata" {
		t.Errorf("DefaultTimezone = %v", c.DefaultTimezone)
	}
	if err := c.RequireDatabase(); err == nil {
		t.Error("RequireDatabase should fail without DATABASE_URL")
	}
}

func TestFailClosedBooleans(t *testing.T) {
	tests := []struct {
		val      string
		want     bool
		wantWarn bool
	}{
		{"", false, false},
		{"false", false, false},
		{"true", true, false},
		{"TRUE", true, false},
		{"yes", false, true},
		{"ture", false, true},
		{"on", false, true},
	}
	for _, tt := range tests {
		c, warns, err := Load(env(map[string]string{"CALLS_ENABLED": tt.val, "TELECOM_COMPLIANCE_ACK": tt.val}))
		if err != nil {
			t.Fatalf("%q: %v", tt.val, err)
		}
		if c.CallsEnabled != tt.want || c.TelecomComplianceAck != tt.want {
			t.Errorf("%q: CallsEnabled=%v TelecomComplianceAck=%v, want %v", tt.val, c.CallsEnabled, c.TelecomComplianceAck, tt.want)
		}
		if (len(warns) > 0) != tt.wantWarn {
			t.Errorf("%q: warnings = %v, wantWarn %v", tt.val, warns, tt.wantWarn)
		}
	}
}

func TestInvalid(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{"bad env", map[string]string{"APP_ENV": "production"}, "APP_ENV"},
		{"bad duration", map[string]string{"ALERT_ACK_TIMEOUT": "ten minutes"}, "ALERT_ACK_TIMEOUT"},
		{"zero duration", map[string]string{"FOLLOW_UP_TTL": "0s"}, "FOLLOW_UP_TTL"},
		{"bad retry list", map[string]string{"RETRY_OFFSETS": "15m,soon"}, "RETRY_OFFSETS"},
		{"too many retries", map[string]string{"RETRY_OFFSETS": "5m,10m,15m"}, "at most 2"},
		{"bad allowlist", map[string]string{"DEV_ALLOWLIST": "+919876541234,98765 41234"}, "DEV_ALLOWLIST"},
		{"bad admin phone", map[string]string{"ADMIN_ALERT_PHONES": "0919876541234"}, "ADMIN_ALERT_PHONES"},
		{"bad from number", map[string]string{"VOICE_FROM_NUMBER": "12345"}, "VOICE_FROM_NUMBER"},
		{"bad retention", map[string]string{"RETENTION_TRANSCRIPT_DAYS": "-1"}, "RETENTION_TRANSCRIPT_DAYS"},
		{"audit too short", map[string]string{"RETENTION_AUDIT_DAYS": "90"}, "at least 365"},
		{"bad timezone", map[string]string{"DEFAULT_TIMEZONE": "Mars/Base"}, "DEFAULT_TIMEZONE"},
		{"prod needs secrets", map[string]string{"APP_ENV": "prod"}, "ENCRYPTION_KEYS is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Load(env(tt.env))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestErrorsDoNotEchoFullPhone(t *testing.T) {
	_, _, err := Load(env(map[string]string{"DEV_ALLOWLIST": "919876541234"}))
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "9876541234") {
		t.Fatalf("error leaks the phone number: %v", err)
	}
}

// testKey is 32 bytes of 'A', base64 encoded.
const testKey = "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE="

func TestEncryptionKeys(t *testing.T) {
	tests := []struct {
		name        string
		env         map[string]string
		wantKeyring bool
		wantErr     string
	}{
		{"unset in dev", nil, false, ""},
		{"valid", map[string]string{"ENCRYPTION_KEYS": "1:" + testKey, "ENCRYPTION_ACTIVE_KID": "1"}, true, ""},
		{"active missing", map[string]string{"ENCRYPTION_KEYS": "1:" + testKey}, false, "ENCRYPTION_ACTIVE_KID"},
		{"short key", map[string]string{"ENCRYPTION_KEYS": "1:c2VjcmV0", "ENCRYPTION_ACTIVE_KID": "1"}, false, "32 bytes"},
		{"kid out of range", map[string]string{"ENCRYPTION_KEYS": "300:" + testKey, "ENCRYPTION_ACTIVE_KID": "300"}, false, "1 to 255"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _, err := Load(env(tt.env))
			if tt.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.wantErr)
			}
			if (c.Keyring != nil) != tt.wantKeyring {
				t.Fatalf("Keyring set = %v, want %v", c.Keyring != nil, tt.wantKeyring)
			}
			if err != nil && strings.Contains(err.Error(), testKey) {
				t.Fatal("error leaks key material")
			}
		})
	}
}

func TestLogValueHidesSecrets(t *testing.T) {
	c, _, err := Load(env(map[string]string{
		"DATABASE_URL":          "postgres://u:supersecret@localhost/db",
		"ENCRYPTION_KEYS":       "1:" + testKey,
		"ENCRYPTION_ACTIVE_KID": "1",
		"DEV_ALLOWLIST":         "+919876541234",
		"LLM_API_KEY":           "sk-secret",
	}))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("config", "config", c)
	out := buf.String()
	if !strings.Contains(out, `"encryption_keys_set":true`) {
		t.Errorf("expected encryption_keys_set true: %s", out)
	}
	for _, secret := range []string{"supersecret", testKey, "sk-secret", "9876541234"} {
		if strings.Contains(out, secret) {
			t.Errorf("log output contains %q: %s", secret, out)
		}
	}
	if !strings.Contains(out, "+91******1234") {
		t.Errorf("expected masked number in log output: %s", out)
	}
}
