// Package config loads and validates environment configuration (SPEC §12).
//
// Safety switches fail closed: CALLS_ENABLED and TELECOM_COMPLIANCE_ACK are
// false unless set to an explicit true value, and an unparseable value is
// treated as false with a warning.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // timezone data on hosts without it (Windows, slim images)

	"github.com/EklavyaGoyal17/haalchaal/internal/crypto"
	"github.com/EklavyaGoyal17/haalchaal/internal/domain"
	"github.com/EklavyaGoyal17/haalchaal/internal/logging"
)

// Env is the deployment environment.
type Env string

const (
	EnvDev     Env = "dev"
	EnvStaging Env = "staging"
	EnvProd    Env = "prod"
)

// Config holds every setting from SPEC §12.
type Config struct {
	AppEnv      Env
	HTTPAddr    string
	DatabaseURL string

	Keyring          *crypto.Keyring // nil in dev when ENCRYPTION_KEYS is unset
	AdminUsers       string          // email:bcrypt_hash,...
	AdminAlertPhones []string

	CallsEnabled         bool
	DevAllowlist         []string
	TelecomComplianceAck bool

	VoiceProvider      string
	VoiceAPIKey        string
	VoiceWebhookSecret string
	VoiceAgentID       string
	VoiceFromNumber    string

	WhatsAppProvider      string
	WhatsAppToken         string
	WhatsAppPhoneNumberID string
	WhatsAppAppSecret     string
	WhatsAppVerifyToken   string
	WhatsAppAPIVersion    string

	LLMProvider string
	LLMAPIKey   string
	LLMModel    string

	RetryOffsets     []time.Duration
	AlertAckTimeout  time.Duration
	UrgentAckTimeout time.Duration
	FollowUpTTL      time.Duration

	RetentionTranscriptDays int
	RetentionAuditDays      int

	DefaultTimezone *time.Location

	WorkerConcurrency int
	PublicBaseURL     string // where admins reach the admin pages, for links in admin messages
	TrustedProxies    []*net.IPNet
}

// Load reads configuration through getenv (os.Getenv in production, a map in
// tests). It returns warnings for values that were coerced to safe defaults,
// and an error listing every invalid setting.
func Load(getenv func(string) string) (Config, []string, error) {
	l := loader{getenv: getenv}
	c := Config{
		AppEnv:      Env(l.str("APP_ENV", "dev")),
		HTTPAddr:    l.str("HTTP_ADDR", ":8080"),
		DatabaseURL: l.str("DATABASE_URL", ""),

		Keyring:          l.keyring("ENCRYPTION_KEYS", "ENCRYPTION_ACTIVE_KID"),
		AdminUsers:       l.str("ADMIN_USERS", ""),
		AdminAlertPhones: l.phones("ADMIN_ALERT_PHONES"),

		CallsEnabled:         l.failClosedBool("CALLS_ENABLED"),
		DevAllowlist:         l.phones("DEV_ALLOWLIST"),
		TelecomComplianceAck: l.failClosedBool("TELECOM_COMPLIANCE_ACK"),

		VoiceProvider:      l.str("VOICE_PROVIDER", "fake"),
		VoiceAPIKey:        l.str("VOICE_API_KEY", ""),
		VoiceWebhookSecret: l.str("VOICE_WEBHOOK_SECRET", ""),
		VoiceAgentID:       l.str("VOICE_AGENT_ID", ""),
		VoiceFromNumber:    l.str("VOICE_FROM_NUMBER", ""),

		WhatsAppProvider:      l.str("WHATSAPP_PROVIDER", "fake"),
		WhatsAppToken:         l.str("WHATSAPP_TOKEN", ""),
		WhatsAppPhoneNumberID: l.str("WHATSAPP_PHONE_NUMBER_ID", ""),
		WhatsAppAppSecret:     l.str("WHATSAPP_APP_SECRET", ""),
		WhatsAppVerifyToken:   l.str("WHATSAPP_VERIFY_TOKEN", ""),
		WhatsAppAPIVersion:    l.str("WHATSAPP_API_VERSION", ""),

		LLMProvider: l.str("LLM_PROVIDER", "fake"),
		LLMAPIKey:   l.str("LLM_API_KEY", ""),
		LLMModel:    l.str("LLM_MODEL", ""),

		RetryOffsets:     l.durations("RETRY_OFFSETS", "15m,45m"),
		AlertAckTimeout:  l.duration("ALERT_ACK_TIMEOUT", "10m"),
		UrgentAckTimeout: l.duration("URGENT_ACK_TIMEOUT", "60m"),
		FollowUpTTL:      l.duration("FOLLOW_UP_TTL", "168h"),

		RetentionTranscriptDays: l.positiveInt("RETENTION_TRANSCRIPT_DAYS", 365),
		RetentionAuditDays:      l.positiveInt("RETENTION_AUDIT_DAYS", 400),

		DefaultTimezone: l.location("DEFAULT_TIMEZONE", "Asia/Kolkata"),

		WorkerConcurrency: l.positiveInt("WORKER_CONCURRENCY", 4),
		PublicBaseURL:     l.str("PUBLIC_BASE_URL", ""),
		TrustedProxies:    l.cidrs("TRUSTED_PROXIES"),
	}

	switch c.AppEnv {
	case EnvDev, EnvStaging, EnvProd:
	default:
		l.errorf("APP_ENV must be dev, staging or prod")
	}
	if len(c.RetryOffsets) > 2 {
		l.errorf("RETRY_OFFSETS allows at most 2 gaps (3 attempts per day)")
	}
	if c.WorkerConcurrency > 64 {
		l.errorf("WORKER_CONCURRENCY must be at most 64")
	}
	if c.RetentionAuditDays > 0 && c.RetentionAuditDays < 365 {
		l.errorf("RETENTION_AUDIT_DAYS must be at least 365")
	}
	if c.VoiceFromNumber != "" && !domain.ValidE164(c.VoiceFromNumber) {
		l.errorf("VOICE_FROM_NUMBER must be E.164")
	}
	if c.AppEnv == EnvProd {
		for _, kv := range [][2]string{{"VOICE_PROVIDER", c.VoiceProvider}, {"WHATSAPP_PROVIDER", c.WhatsAppProvider}, {"LLM_PROVIDER", c.LLMProvider}} {
			if kv[1] == "fake" {
				l.errorf("%s cannot be fake in prod", kv[0])
			}
		}
	}
	if c.AppEnv != EnvDev {
		for _, k := range []string{"DATABASE_URL", "ENCRYPTION_KEYS", "ENCRYPTION_ACTIVE_KID", "ADMIN_USERS"} {
			if strings.TrimSpace(getenv(k)) == "" {
				l.errorf("%s is required outside dev", k)
			}
		}
	}

	return c, l.warnings, errors.Join(l.errs...)
}

// RequireDatabase returns an error if DATABASE_URL is not set.
func (c Config) RequireDatabase() error {
	if c.DatabaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	return nil
}

// LogValue lets a Config be logged safely: no secrets, phone numbers masked.
func (c Config) LogValue() slog.Value {
	masked := func(ps []string) []string {
		out := make([]string, len(ps))
		for i, p := range ps {
			out[i] = logging.MaskPhone(p)
		}
		return out
	}
	return slog.GroupValue(
		slog.String("app_env", string(c.AppEnv)),
		slog.String("http_addr", c.HTTPAddr),
		slog.Bool("database_url_set", c.DatabaseURL != ""),
		slog.Bool("encryption_keys_set", c.Keyring != nil),
		slog.Bool("calls_enabled", c.CallsEnabled),
		slog.Bool("telecom_compliance_ack", c.TelecomComplianceAck),
		slog.Any("dev_allowlist", masked(c.DevAllowlist)),
		slog.Any("admin_alert_phones", masked(c.AdminAlertPhones)),
		slog.String("voice_provider", c.VoiceProvider),
		slog.String("whatsapp_provider", c.WhatsAppProvider),
		slog.String("llm_provider", c.LLMProvider),
		slog.String("default_timezone", c.DefaultTimezone.String()),
	)
}

type loader struct {
	getenv   func(string) string
	errs     []error
	warnings []string
}

func (l *loader) errorf(format string, args ...any) {
	l.errs = append(l.errs, fmt.Errorf(format, args...))
}

func (l *loader) str(key, def string) string {
	if v := strings.TrimSpace(l.getenv(key)); v != "" {
		return v
	}
	return def
}

// failClosedBool is true only for an explicit true value. Anything it cannot
// parse is false, with a warning, so a typo never enables calling.
func (l *loader) failClosedBool(key string) bool {
	v := strings.TrimSpace(l.getenv(key))
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.warnings = append(l.warnings, fmt.Sprintf("%s has an unrecognised value; treating it as false", key))
		return false
	}
	return b
}

// keyring parses the encryption keys when they are set. Missing keys are only
// an error outside dev, which Load checks separately.
func (l *loader) keyring(keysKey, activeKey string) *crypto.Keyring {
	keys := strings.TrimSpace(l.getenv(keysKey))
	active := strings.TrimSpace(l.getenv(activeKey))
	if keys == "" && active == "" {
		return nil
	}
	kr, err := crypto.ParseKeyring(keys, active)
	if err != nil {
		l.errs = append(l.errs, err)
		return nil
	}
	return kr
}

func (l *loader) phones(key string) []string {
	var out []string
	for _, p := range splitList(l.getenv(key)) {
		if !domain.ValidE164(p) {
			l.errorf("%s contains a number that is not E.164 (%s)", key, logging.MaskPhone(p))
			continue
		}
		out = append(out, p)
	}
	return out
}

func (l *loader) duration(key, def string) time.Duration {
	v := l.str(key, def)
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		l.errorf("%s must be a positive duration like 10m", key)
		return 0
	}
	return d
}

func (l *loader) durations(key, def string) []time.Duration {
	var out []time.Duration
	for _, v := range splitList(l.str(key, def)) {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			l.errorf("%s must be positive durations like 15m,45m", key)
			return nil
		}
		out = append(out, d)
	}
	return out
}

func (l *loader) positiveInt(key string, def int) int {
	v := strings.TrimSpace(l.getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		l.errorf("%s must be a positive whole number", key)
		return 0
	}
	return n
}

func (l *loader) location(key, def string) *time.Location {
	v := l.str(key, def)
	loc, err := time.LoadLocation(v)
	if err != nil {
		l.errorf("%s is not a known timezone", key)
		return time.UTC
	}
	return loc
}

// cidrs parses a list of CIDRs or bare IPs (load balancers whose
// X-Forwarded-For may be trusted).
func (l *loader) cidrs(key string) []*net.IPNet {
	var out []*net.IPNet
	for _, v := range splitList(l.getenv(key)) {
		if !strings.Contains(v, "/") {
			if ip := net.ParseIP(v); ip != nil && ip.To4() != nil {
				v += "/32"
			} else {
				v += "/128"
			}
		}
		_, n, err := net.ParseCIDR(v)
		if err != nil {
			l.errorf("%s has an invalid entry", key)
			continue
		}
		out = append(out, n)
	}
	return out
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
