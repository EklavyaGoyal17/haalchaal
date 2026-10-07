// Package claude is the Anthropic (Claude) adapter for extract.LLM. It asks
// for structured output matching prompts/report.schema.json; the Go
// validator in internal/extract remains the authority on the result.
package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"

	"github.com/EklavyaGoyal17/haalchaal/prompts"
)

// DefaultModel is used when LLM_MODEL is unset.
const DefaultModel = "claude-opus-5-5"

// Client calls the Messages API.
type Client struct {
	client anthropic.Client
	model  string
	schema json.RawMessage
}

// New builds a client. baseURL is empty except in tests.
func New(apiKey, model, baseURL string) (*Client, error) {
	if apiKey == "" {
		return nil, errors.New("claude: LLM_API_KEY is required")
	}
	if model == "" {
		model = DefaultModel
	}
	schema, err := reducedSchema()
	if err != nil {
		return nil, err
	}
	opts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithRequestTimeout(90 * time.Second),
		option.WithMaxRetries(2), // 408/409/429/5xx and connection errors
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &Client{client: anthropic.NewClient(opts...), model: model, schema: schema}, nil
}

// Name is stored in call_reports.model.
func (c *Client) Name() string { return "anthropic:" + c.model }

// Complete sends one prompt and returns the model's text (a JSON object).
// Errors never include the prompt or the response text.
func (c *Client) Complete(ctx context.Context, prompt string) (string, error) {
	resp, err := c.client.Beta.Messages.New(ctx, anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: 16000,
		Messages:  []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(prompt))},
		OutputConfig: anthropic.BetaOutputConfigParam{
			Effort: anthropic.BetaOutputConfigEffortMedium,
			Format: anthropic.BetaJSONOutputFormatParam{Schema: c.schema},
		},
		// A safety-classifier decline is re-served by a fallback model
		// inside the same call instead of failing the extraction.
		Fallbacks: anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()},
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
	})
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			return "", fmt.Errorf("claude: status %d", apiErr.StatusCode)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return "", errors.New("claude: request timed out")
		}
		return "", fmt.Errorf("claude: request failed: %w", err)
	}
	switch resp.StopReason {
	case anthropic.BetaStopReasonRefusal:
		return "", errors.New("claude: request refused")
	case anthropic.BetaStopReasonMaxTokens:
		return "", errors.New("claude: output truncated at max_tokens")
	}
	var b strings.Builder
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	if b.Len() == 0 {
		return "", errors.New("claude: empty response")
	}
	return b.String(), nil
}

// reducedSchema is the report schema without keywords structured outputs may
// not accept (lengths, counts, ranges, patterns, metadata). The Go validator
// enforces all of them after the call.
func reducedSchema() (json.RawMessage, error) {
	raw, err := prompts.FS.ReadFile("report.schema.json")
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("report schema: %w", err)
	}
	strip(v)
	return json.Marshal(v)
}

var dropped = []string{"$schema", "$id", "title", "maxLength", "minLength", "maxItems", "minItems", "minimum", "maximum", "pattern"}

func strip(v any) {
	switch t := v.(type) {
	case map[string]any:
		for _, k := range dropped {
			delete(t, k)
		}
		for k, c := range t {
			if k == "properties" {
				// Keys here are field names (one is "pattern"), not keywords.
				if props, ok := c.(map[string]any); ok {
					for _, p := range props {
						strip(p)
					}
				}
				continue
			}
			strip(c)
		}
	case []any:
		for _, c := range t {
			strip(c)
		}
	}
}
