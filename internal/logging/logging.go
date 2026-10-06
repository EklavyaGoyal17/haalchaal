// Package logging sets up structured JSON logging and holds the shared
// phone-masking helper. Never log transcripts, medicine names, health details
// or full phone numbers; pass numbers through MaskPhone first.
package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"strings"
)

// New returns a JSON slog logger writing to w at the given level.
func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}

// MaskPhone hides all but the first three and last four characters of a phone
// number: "+919876541234" becomes "+91******1234". Anything too short to mask
// safely becomes "***".
func MaskPhone(s string) string {
	if s == "" {
		return ""
	}
	if len(s) < 8 {
		return "***"
	}
	return s[:3] + strings.Repeat("*", len(s)-7) + s[len(s)-4:]
}

type ctxKey struct{}

// WithRequestID returns a context carrying the request id.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// RequestID returns the request id from ctx, or "" if none is set.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// NewRequestID returns a random 16-hex-character id.
func NewRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}
