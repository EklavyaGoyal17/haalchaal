package safety

import (
	"errors"
	"testing"

	"github.com/EklavyaGoyal17/haalchaal/internal/config"
)

func TestGate(t *testing.T) {
	const allowed, other = "+919876541234", "+919800000000"
	tests := []struct {
		name    string
		g       Gate
		phone   string
		call    error
		message error
	}{
		{"calls disabled in dev", Gate{Env: config.EnvDev, DevAllowlist: []string{allowed}}, allowed, ErrCallsDisabled, ErrCallsDisabled},
		{"calls disabled in prod", Gate{Env: config.EnvProd, ComplianceAck: true}, allowed, ErrCallsDisabled, ErrCallsDisabled},
		{"dev allowlisted", Gate{Env: config.EnvDev, CallsEnabled: true, DevAllowlist: []string{allowed}}, allowed, nil, nil},
		{"dev not allowlisted", Gate{Env: config.EnvDev, CallsEnabled: true, DevAllowlist: []string{allowed}}, other, ErrNotAllowlisted, ErrNotAllowlisted},
		{"dev empty allowlist", Gate{Env: config.EnvDev, CallsEnabled: true}, allowed, ErrNotAllowlisted, ErrNotAllowlisted},
		{"staging not allowlisted", Gate{Env: config.EnvStaging, CallsEnabled: true}, allowed, ErrNotAllowlisted, ErrNotAllowlisted},
		{"prod without ack", Gate{Env: config.EnvProd, CallsEnabled: true}, allowed, ErrComplianceNotAcked, nil},
		{"prod with ack", Gate{Env: config.EnvProd, CallsEnabled: true, ComplianceAck: true}, other, nil, nil},
		{"invalid number", Gate{Env: config.EnvProd, CallsEnabled: true, ComplianceAck: true}, "98765", ErrInvalidNumber, ErrInvalidNumber},
		{"unknown env", Gate{Env: "qa", CallsEnabled: true, DevAllowlist: []string{allowed}}, allowed, ErrUnknownEnvironment, ErrUnknownEnvironment},
		{"zero gate", Gate{}, allowed, ErrCallsDisabled, ErrCallsDisabled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.g.AllowCall(tt.phone); !errors.Is(err, tt.call) {
				t.Errorf("AllowCall = %v, want %v", err, tt.call)
			}
			if err := tt.g.AllowMessage(tt.phone); !errors.Is(err, tt.message) {
				t.Errorf("AllowMessage = %v, want %v", err, tt.message)
			}
		})
	}
}

func TestFromConfigCopiesAllowlist(t *testing.T) {
	c := config.Config{AppEnv: config.EnvDev, CallsEnabled: true, DevAllowlist: []string{"+919876541234"}}
	g := FromConfig(c)
	c.DevAllowlist[0] = "+919800000000"
	if err := g.AllowCall("+919876541234"); err != nil {
		t.Fatalf("gate changed with config slice: %v", err)
	}
}
