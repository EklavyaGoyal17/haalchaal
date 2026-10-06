// Package rules decides which alerts a processed call raises (SPEC §9). It is
// pure: the caller supplies the report, keyword hits and recent history, and
// writes the findings.
package rules

import (
	"slices"
	"strings"

	"github.com/EklavyaGoyal17/haalchaal/internal/alerts"
	"github.com/EklavyaGoyal17/haalchaal/internal/extract"
)

// Categories raised by rules rather than by a single red flag.
const (
	CategoryDistressed     = "distressed"
	CategoryMissedMedicine = "missed_medicine"
	CategoryLowMood        = "low_mood"
	CategoryPoorSleep      = "poor_sleep"
)

// Finding is an alert to raise, without parent or call ids.
type Finding struct {
	Type, Category, Source, Detail string
	// AdminOnly marks an alert that goes to admins for review before any
	// family member (a scam keyword the model did not confirm).
	AdminOnly bool
}

// Evaluate returns the alerts for one processed call. history holds earlier
// usable reports for the same parent, newest first. Every red flag alerts:
// when unsure, alert.
func Evaluate(r extract.Report, hits []alerts.Hit, history []extract.Report) []Finding {
	var out []Finding
	seen := map[string]int{} // category -> index in out

	add := func(f Finding) {
		if i, ok := seen[f.Category]; ok {
			// Same category twice: keep the stronger type, prefer the model's
			// quote over a keyword's.
			if f.Type == alerts.TypeEmergency {
				out[i].Type = alerts.TypeEmergency
			}
			return
		}
		seen[f.Category] = len(out)
		out = append(out, f)
	}

	for _, rf := range r.RedFlags {
		typ := alerts.TypeUrgent
		if rf.Severity == "emergency" || rf.Category == "self_harm" {
			typ = alerts.TypeEmergency
		}
		add(Finding{Type: typ, Category: rf.Category, Source: alerts.SourceModel, Detail: rf.Quote})
	}
	scamKeyword := false
	var scamQuote string
	for _, h := range hits {
		if h.Category == "scam" {
			scamKeyword, scamQuote = true, h.Quote
			continue
		}
		// Keyword-only emergency hits still alert the family.
		add(Finding{Type: alerts.RedFlagSeverity(h.Category), Category: h.Category, Source: alerts.SourceKeyword, Detail: h.Quote})
	}

	// All of a call's scam signals become one alert, named after the most
	// serious pattern, so the family gets one message, not one per pattern.
	if len(r.ScamSignals) > 0 {
		best := r.ScamSignals[0]
		for _, s := range r.ScamSignals[1:] {
			if scamRank(s.Pattern) < scamRank(best.Pattern) {
				best = s
			}
		}
		add(Finding{Type: alerts.TypeScam, Category: ScamCategory(best.Pattern), Source: alerts.SourceModel, Detail: best.Quote})
	}
	if scamKeyword && len(r.ScamSignals) == 0 {
		add(Finding{Type: alerts.TypeScam, Category: alerts.CategoryScamKeyword, Source: alerts.SourceKeyword, Detail: scamQuote, AdminOnly: true})
	}

	if r.CallPreferences.StopRequested {
		add(Finding{Type: alerts.TypeUrgent, Category: alerts.CategoryStopRequested, Source: alerts.SourceModel})
	}
	if r.Mood == "distressed" {
		add(Finding{Type: alerts.TypeUrgent, Category: CategoryDistressed, Source: alerts.SourceRule})
	}
	if missed := missedTwice(r, history); len(missed) > 0 {
		add(Finding{Type: alerts.TypeUrgent, Category: CategoryMissedMedicine, Source: alerts.SourceRule, Detail: strings.Join(missed, ", ")})
	}
	if threeInARow(r, history, func(x extract.Report) bool { return x.Mood == "low" }) {
		add(Finding{Type: alerts.TypeWatch, Category: CategoryLowMood, Source: alerts.SourceRule})
	}
	if threeInARow(r, history, func(x extract.Report) bool { return x.Sleep == "poor" }) {
		add(Finding{Type: alerts.TypeWatch, Category: CategoryPoorSleep, Source: alerts.SourceRule})
	}
	return out
}

// ScamCategory is the alert category for a scam pattern; mid-call tool
// reports use the same mapping, so they merge with post-call findings.
func ScamCategory(pattern string) string {
	if pattern == "other" || scamRank(pattern) == len(scamOrder) {
		return "scam_other"
	}
	return pattern
}

var scamOrder = []string{"agency_threat", "otp_or_bank_request", "money_request", "video_call_pressure", "other"}

func scamRank(p string) int {
	if i := slices.Index(scamOrder, p); i >= 0 {
		return i
	}
	return len(scamOrder)
}

// missedTwice lists medicines marked "no" today and on the previous call.
func missedTwice(r extract.Report, history []extract.Report) []string {
	if len(history) == 0 {
		return nil
	}
	var prev []string
	for _, m := range history[0].Medicines {
		if m.Taken == "no" {
			prev = append(prev, strings.ToLower(strings.TrimSpace(m.Name)))
		}
	}
	var out []string
	for _, m := range r.Medicines {
		if m.Taken == "no" && slices.Contains(prev, strings.ToLower(strings.TrimSpace(m.Name))) {
			out = append(out, m.Name)
		}
	}
	return out
}

func threeInARow(r extract.Report, history []extract.Report, pred func(extract.Report) bool) bool {
	if len(history) < 2 || !pred(r) {
		return false
	}
	return pred(history[0]) && pred(history[1])
}
