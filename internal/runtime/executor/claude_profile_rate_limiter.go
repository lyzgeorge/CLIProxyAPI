package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type claudeProfileRateLimitContextKey struct{}

type claudeProfileRateLimitContext struct {
	key   string
	limit int
}

type claudeProfileRateWindow struct {
	start time.Time
	count int
}

type claudeProfileRateLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	windows map[string]claudeProfileRateWindow
}

var globalClaudeProfileRateLimiter = &claudeProfileRateLimiter{
	now:     time.Now,
	windows: make(map[string]claudeProfileRateWindow),
}

func (l *claudeProfileRateLimiter) allow(key string, limit int) (time.Duration, bool) {
	if l == nil || limit <= 0 || key == "" {
		return 0, true
	}
	nowFunc := l.now
	if nowFunc == nil {
		nowFunc = time.Now
	}
	now := nowFunc()
	windowStart := now.Truncate(time.Minute)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.windows == nil {
		l.windows = make(map[string]claudeProfileRateWindow)
	}
	window := l.windows[key]
	if !window.start.Equal(windowStart) {
		window = claudeProfileRateWindow{start: windowStart}
	}
	if window.count >= limit {
		retryAfter := windowStart.Add(time.Minute).Sub(now)
		if retryAfter < time.Second {
			retryAfter = time.Second
		}
		l.windows[key] = window
		return retryAfter, false
	}
	window.count++
	l.windows[key] = window
	return 0, true
}

func withClaudeProfileRateLimit(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, apiKey string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	limit, configured := 0, false
	baseURL, prefix := "", ""
	if auth != nil {
		limit, configured = auth.RequestsPerMinuteOverride()
	}
	if !configured {
		entry := resolveClaudeKeyConfig(cfg, auth)
		if entry == nil || entry.Cloak == nil {
			return ctx
		}
		limit = entry.Cloak.RequestsPerMinute
		baseURL, prefix = entry.BaseURL, entry.Prefix
	}
	if limit <= 0 {
		return ctx
	}
	key := ""
	if auth != nil {
		key = auth.EnsureIndex()
		if key == "" {
			key = auth.ID
		}
	}
	if key == "" {
		sum := sha256.Sum256([]byte(apiKey + "\x00" + baseURL + "\x00" + prefix))
		key = hex.EncodeToString(sum[:])
	}
	return context.WithValue(ctx, claudeProfileRateLimitContextKey{}, claudeProfileRateLimitContext{
		key:   key,
		limit: limit,
	})
}

func claudeProfileRateLimitFromContext(ctx context.Context) (claudeProfileRateLimitContext, bool) {
	if ctx == nil {
		return claudeProfileRateLimitContext{}, false
	}
	value, ok := ctx.Value(claudeProfileRateLimitContextKey{}).(claudeProfileRateLimitContext)
	return value, ok && value.limit > 0 && value.key != ""
}

type claudeProfileRateLimitError struct {
	retryAfter time.Duration
	message    string
	response   *cliproxyexecutor.RequestTerminatedError
}

func (e *claudeProfileRateLimitError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func (e *claudeProfileRateLimitError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.response
}

func (e *claudeProfileRateLimitError) StatusCode() int          { return http.StatusTooManyRequests }
func (e *claudeProfileRateLimitError) IsRequestScoped() bool    { return true }
func (e *claudeProfileRateLimitError) IsCredentialScoped() bool { return false }
func (e *claudeProfileRateLimitError) RetryAfter() *time.Duration {
	if e == nil {
		return nil
	}
	return &e.retryAfter
}
func (e *claudeProfileRateLimitError) SafeResponseHeaders() http.Header {
	if e == nil {
		return nil
	}
	seconds := claudeRetryAfterSeconds(e.retryAfter)
	return http.Header{"Retry-After": []string{strconv.FormatInt(seconds, 10)}}
}

func claudeRetryAfterSeconds(value time.Duration) int64 {
	seconds := int64(value / time.Second)
	if value%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	return seconds
}

func newClaudeProfileRateLimitError(retryAfter time.Duration) error {
	body := []byte(`{"type":"error","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Claude profile request limit exceeded"}}`)
	seconds := claudeRetryAfterSeconds(retryAfter)
	header := http.Header{
		"Content-Type": []string{"application/json"},
		"Retry-After":  []string{strconv.FormatInt(seconds, 10)},
	}
	return &claudeProfileRateLimitError{
		retryAfter: retryAfter,
		message:    string(body),
		response:   &cliproxyexecutor.RequestTerminatedError{HTTPStatus: http.StatusTooManyRequests, Header: header, Body: body},
	}
}

func checkClaudeProfileRateLimit(ctx context.Context) error {
	settings, ok := claudeProfileRateLimitFromContext(ctx)
	if !ok {
		return nil
	}
	retryAfter, allowed := globalClaudeProfileRateLimiter.allow(settings.key, settings.limit)
	if !allowed {
		return newClaudeProfileRateLimitError(retryAfter)
	}
	return nil
}
