// Package safety holds the outbound-contact gate (CLAUDE.md rule 6, SPEC §14).
// Every real call and message passes through it, and it fails closed: when
// anything is missing or unclear, the answer is no.
package safety

import (
	"errors"
	"slices"

	"github.com/EklavyaGoyal17/haalchaal/internal/config"
	"github.com/EklavyaGoyal17/haalchaal/internal/domain"
)

// Reasons a contact is blocked. They double as calls.end_reason values.
var (
	ErrCallsDisabled      = errors.New("calls_disabled")
	ErrNotAllowlisted     = errors.New("not_in_dev_allowlist")
	ErrComplianceNotAcked = errors.New("telecom_compliance_not_acked")
	ErrInvalidNumber      = errors.New("invalid_phone_number")
	ErrUnknownEnvironment = errors.New("unknown_environment")
)

// Gate decides whether a phone number may be contacted.
type Gate struct {
	Env           config.Env
	CallsEnabled  bool
	DevAllowlist  []string
	ComplianceAck bool
}

// FromConfig builds a Gate from loaded configuration.
func FromConfig(c config.Config) Gate {
	return Gate{
		Env:           c.AppEnv,
		CallsEnabled:  c.CallsEnabled,
		DevAllowlist:  slices.Clone(c.DevAllowlist),
		ComplianceAck: c.TelecomComplianceAck,
	}
}

// AllowMessage reports whether a WhatsApp message may go to phone.
func (g Gate) AllowMessage(phone string) error {
	if !g.CallsEnabled {
		return ErrCallsDisabled
	}
	if !domain.ValidE164(phone) {
		return ErrInvalidNumber
	}
	switch g.Env {
	case config.EnvProd:
		return nil
	case config.EnvDev, config.EnvStaging:
		if slices.Contains(g.DevAllowlist, phone) {
			return nil
		}
		return ErrNotAllowlisted
	default:
		return ErrUnknownEnvironment
	}
}

// AllowCall reports whether phone may be dialled. Production dialling also
// needs the telecom compliance acknowledgement.
func (g Gate) AllowCall(phone string) error {
	if err := g.AllowMessage(phone); err != nil {
		return err
	}
	if g.Env == config.EnvProd && !g.ComplianceAck {
		return ErrComplianceNotAcked
	}
	return nil
}
