package vapi

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/EklavyaGoyal17/haalchaal/internal/alerts"
	"github.com/EklavyaGoyal17/haalchaal/internal/voice"
	"github.com/EklavyaGoyal17/haalchaal/prompts"
)

// The tool definitions pasted into the platform must match what the
// handlers understand, so a renamed category never silently becomes "other".
func TestToolDefinitionsMatchHandlers(t *testing.T) {
	raw, err := prompts.FS.ReadFile("voice_tools.json")
	if err != nil {
		t.Fatal(err)
	}
	var tools []struct {
		Name       string `json:"name"`
		Parameters struct {
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools {
		names = append(names, tl.Name)
		props := tl.Parameters.Properties
		switch tl.Name {
		case voice.ToolReportRedFlag:
			if !slices.Equal(props["category"].Enum, alerts.RedFlagCategories) {
				t.Errorf("red-flag categories %v, handlers know %v", props["category"].Enum, alerts.RedFlagCategories)
			}
			if !slices.Equal(props["severity"].Enum, []string{"emergency", "urgent"}) {
				t.Errorf("severity %v", props["severity"].Enum)
			}
		case voice.ToolReportScam:
			if !slices.Equal(props["pattern"].Enum, alerts.ScamPatterns) {
				t.Errorf("scam patterns %v, handlers know %v", props["pattern"].Enum, alerts.ScamPatterns)
			}
		}
	}
	if !slices.Equal(names, []string{voice.ToolReportRedFlag, voice.ToolReportScam, voice.ToolReportStopRequest}) {
		t.Errorf("tools %v", names)
	}
}
