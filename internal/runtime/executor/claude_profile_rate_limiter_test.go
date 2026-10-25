package executor

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestClaudeProfileRateLimiterWindowAndDisable(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 30, 0, time.UTC)
	limiter := &claudeProfileRateLimiter{now: func() time.Time { return now }, windows: make(map[string]claudeProfileRateWindow)}
	if _, ok := limiter.allow("profile", 0); !ok {
		t.Fatal("zero limit should be disabled")
	}
	if _, ok := limiter.allow("profile", -1); !ok {
		t.Fatal("negative limit should be disabled")
	}
	if _, ok := limiter.allow("profile", 2); !ok {
		t.Fatal("first request rejected")
	}
	if _, ok := limiter.allow("profile", 2); !ok {
		t.Fatal("second request rejected")
	}
	retryAfter, ok := limiter.allow("profile", 2)
	if ok || retryAfter != 30*time.Second {
		t.Fatalf("third request = (%v, %v), want (30s, false)", retryAfter, ok)
	}
	now = now.Add(time.Minute)
	if _, ok := limiter.allow("profile", 2); !ok {
		t.Fatal("request should pass after window rollover")
	}
}

func TestClaudeProfileRateLimiterConcurrentCount(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 30, 0, time.UTC)
	limiter := &claudeProfileRateLimiter{now: func() time.Time { return now }, windows: make(map[string]claudeProfileRateWindow)}
	const limit = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := limiter.allow("profile", limit); ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != limit {
		t.Fatalf("allowed %d concurrent requests, want %d", allowed, limit)
	}
}

func TestClaudeProfileRateLimitErrorCarriesStructuredResponse(t *testing.T) {
	errLimit := newClaudeProfileRateLimitError(17 * time.Second)
	var terminated *cliproxyexecutor.RequestTerminatedError
	if !errors.As(errLimit, &terminated) || terminated == nil {
		t.Fatalf("error does not carry terminated response: %T", errLimit)
	}
	if terminated.HTTPStatus != http.StatusTooManyRequests || terminated.Header.Get("Retry-After") != "17" {
		t.Fatalf("terminated response = %#v", terminated)
	}
	if body := string(terminated.Body); body == "" || body[0] != '{' {
		t.Fatalf("unexpected structured body %q", body)
	}
	if scoped, ok := errLimit.(interface{ IsRequestScoped() bool }); !ok || !scoped.IsRequestScoped() {
		t.Fatal("limiter error must be request scoped")
	}
}

func TestWithClaudeProfileRateLimitUsesAuthInstance(t *testing.T) {
	apiKey := "key"
	auth := &cliproxyauth.Auth{ID: "auth-one", Attributes: map[string]string{"api_key": apiKey}}
	cfg := &config.Config{ClaudeKey: []config.ClaudeKey{{APIKey: apiKey, Cloak: &config.CloakConfig{RequestsPerMinute: 3}}}}
	ctx := withClaudeProfileRateLimit(context.Background(), cfg, auth, apiKey)
	value, ok := claudeProfileRateLimitFromContext(ctx)
	if !ok || value.limit != 3 || value.key == "" {
		t.Fatalf("rate-limit context = %#v, %v", value, ok)
	}
}

func TestWithClaudeProfileRateLimitPrefersAuthOverride(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID:         "auth-one",
		Attributes: map[string]string{"api_key": "key"},
		Metadata:   map[string]any{"requests_per_minute": 7},
	}
	cfg := &config.Config{ClaudeKey: []config.ClaudeKey{{APIKey: "key", Cloak: &config.CloakConfig{RequestsPerMinute: 3}}}}
	ctx := withClaudeProfileRateLimit(context.Background(), cfg, auth, "key")
	value, ok := claudeProfileRateLimitFromContext(ctx)
	if !ok || value.limit != 7 {
		t.Fatalf("rate-limit context = %#v, %v; want auth limit 7", value, ok)
	}
}

func TestWithClaudeProfileRateLimitAuthZeroDisablesConfiguredFallback(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID:         "auth-one",
		Attributes: map[string]string{"api_key": "key"},
		Metadata:   map[string]any{"requests_per_minute": 0},
	}
	cfg := &config.Config{ClaudeKey: []config.ClaudeKey{{APIKey: "key", Cloak: &config.CloakConfig{RequestsPerMinute: 3}}}}
	if _, ok := claudeProfileRateLimitFromContext(withClaudeProfileRateLimit(context.Background(), cfg, auth, "key")); ok {
		t.Fatal("explicit auth zero should disable the config fallback")
	}
}
