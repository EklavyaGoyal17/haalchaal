// Package cloud is the WhatsApp Cloud API adapter (Meta Graph API). It only
// runs with WHATSAPP_PROVIDER=cloud; tests use a local test server.
package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/EklavyaGoyal17/haalchaal/internal/notify"
)

// DefaultBaseURL is Meta's Graph API.
const DefaultBaseURL = "https://graph.facebook.com"

// Client sends template messages through the Cloud API.
type Client struct {
	BaseURL       string // DefaultBaseURL unless testing
	APIVersion    string // WHATSAPP_API_VERSION, e.g. v21.0
	PhoneNumberID string
	Token         string
	AppSecret     []byte
	Verify        string
	HTTP          *http.Client
}

// New returns a client with a bounded HTTP timeout.
func New(version, phoneNumberID, token, appSecret, verifyToken string) (*Client, error) {
	if version == "" || phoneNumberID == "" || token == "" || appSecret == "" || verifyToken == "" {
		return nil, errors.New("whatsapp cloud: WHATSAPP_API_VERSION, WHATSAPP_PHONE_NUMBER_ID, WHATSAPP_TOKEN, WHATSAPP_APP_SECRET and WHATSAPP_VERIFY_TOKEN are required")
	}
	return &Client{
		BaseURL: DefaultBaseURL, APIVersion: version, PhoneNumberID: phoneNumberID, Token: token,
		AppSecret: []byte(appSecret), Verify: verifyToken, HTTP: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (c *Client) Name() string        { return "whatsapp" }
func (c *Client) VerifyToken() string { return c.Verify }

type param struct {
	Type    string `json:"type"`
	Text    string `json:"text,omitempty"`
	Payload string `json:"payload,omitempty"`
}

type component struct {
	Type       string  `json:"type"`
	SubType    string  `json:"sub_type,omitempty"`
	Index      string  `json:"index,omitempty"`
	Parameters []param `json:"parameters"`
}

// SendTemplate posts one template message and returns Meta's message id.
// Error text never includes the message parameters.
func (c *Client) SendTemplate(ctx context.Context, m notify.TemplateMessage) (string, error) {
	if err := notify.Validate(m); err != nil {
		return "", err
	}
	body := []component{{Type: "body"}}
	for _, p := range m.Params {
		body[0].Parameters = append(body[0].Parameters, param{Type: "text", Text: notify.CleanParam(p)})
	}
	for i, b := range m.Buttons {
		body = append(body, component{Type: "button", SubType: "quick_reply", Index: fmt.Sprint(i),
			Parameters: []param{{Type: "payload", Payload: b.Payload}}})
	}
	payload, err := json.Marshal(map[string]any{
		"messaging_product": "whatsapp",
		"to":                strings.TrimPrefix(m.To, "+"),
		"type":              "template",
		"template": map[string]any{
			"name":       m.Template,
			"language":   map[string]string{"code": m.Language},
			"components": body,
		},
	})
	if err != nil {
		return "", err
	}
	u := strings.TrimRight(c.BaseURL, "/") + "/" + url.PathEscape(c.APIVersion) + "/" + url.PathEscape(c.PhoneNumberID) + "/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("whatsapp cloud: request failed: %w", redact(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error struct {
				Code    int    `json:"code"`
				Type    string `json:"type"`
				Subcode int    `json:"error_subcode"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		return "", fmt.Errorf("whatsapp cloud: status %d code %d/%d %s", resp.StatusCode, e.Error.Code, e.Error.Subcode, e.Error.Type)
	}
	var ok struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &ok); err != nil || len(ok.Messages) == 0 || ok.Messages[0].ID == "" {
		return "", errors.New("whatsapp cloud: response without message id")
	}
	return ok.Messages[0].ID, nil
}

// ParseWebhook verifies X-Hub-Signature-256 and parses Meta's format.
func (c *Client) ParseWebhook(r *http.Request) ([]notify.InboundEvent, error) {
	body, err := notify.ReadVerifiedMetaBody(r, c.AppSecret)
	if err != nil {
		return nil, err
	}
	return notify.ParseMetaPayload(body)
}

// redact drops the URL from transport errors (it holds the phone number id).
func redact(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

var _ notify.Messenger = (*Client)(nil)
