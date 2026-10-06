// Package calls owns the call lifecycle: the guards re-checked before every
// dial, the attempt state machine and retries (SPEC §5).
package calls

import (
	"slices"
	"time"

	"github.com/EklavyaGoyal17/haalchaal/internal/db"
	"github.com/EklavyaGoyal17/haalchaal/internal/safety"
	"github.com/EklavyaGoyal17/haalchaal/internal/scheduler"
)

// End reasons written to calls.end_reason when a guard blocks an attempt.
const (
	ReasonParentNotActive  = "parent_not_active"
	ReasonAccountNotActive = "account_not_active"
	ReasonConsentMissing   = "consent_missing"
	ReasonOutsideWindow    = "outside_window"
	ReasonBadSchedule      = "invalid_schedule"
)

// RequiredConsents must all be given and not withdrawn before any call
// (SPEC §13). recording is optional.
var RequiredConsents = []string{"calls", "data_processing", "share_with_family"}

// CheckGuards re-checks, at dial time, everything that must hold for a call:
// parent and account status, consents, the call window, and the outbound
// contact gate. It returns "" when the call may go ahead, otherwise the first
// failing reason. Anything it cannot evaluate blocks the call.
func CheckGuards(gate safety.Gate, s db.GetCallGuardStateRow, now time.Time) string {
	if s.ParentStatus != "active" {
		return ReasonParentNotActive
	}
	if s.AccountStatus != "trial" && s.AccountStatus != "active" {
		return ReasonAccountNotActive
	}
	for _, k := range RequiredConsents {
		if !slices.Contains(s.ConsentKinds, k) {
			return ReasonConsentMissing
		}
	}
	w, err := scheduler.WindowFromPG(s.CallTimeLocal, s.WindowStart, s.WindowEnd)
	if err != nil {
		return ReasonBadSchedule
	}
	local, err := scheduler.LocalNow(now, s.Timezone)
	if err != nil {
		return ReasonBadSchedule
	}
	if !w.Contains(local.Wall) {
		return ReasonOutsideWindow
	}
	if err := gate.AllowCall(s.PhoneE164); err != nil {
		return err.Error()
	}
	return ""
}
