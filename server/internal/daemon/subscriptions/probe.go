package subscriptions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Endpoints are the provider quota endpoints; overridable for tests.
type Endpoints struct {
	Anthropic  string // POST {Anthropic}/v1/messages
	Codex      string // GET {Codex}/backend-api/wham/usage
	Zai        string // GET {Zai}/api/monitor/usage/quota/limit
	Minimax    string // GET {Minimax}/v1/api/openplatform/coding_plan/remains
	OpenRouter string // GET {OpenRouter}/api/v1/credits
}

// DefaultEndpoints are the production provider hosts.
var DefaultEndpoints = Endpoints{
	Anthropic:  "https://api.anthropic.com",
	Codex:      "https://chatgpt.com",
	Zai:        "https://api.z.ai",
	Minimax:    "https://api.minimax.io",
	OpenRouter: "https://openrouter.ai",
}

const (
	// DefaultClaudeProbeModel answers a one-token probe cheaply.
	DefaultClaudeProbeModel = "claude-haiku-5-5"
	// fableProbeModel: the separate Fable weekly window is only reported on
	// requests to Fable.
	fableProbeModel = "claude-fable-5-1"
)

// Prober measures a subscription's quota windows with its own credential.
type Prober struct {
	HTTP      *http.Client
	Endpoints Endpoints
	Now       func() time.Time
}

// NewProber returns a prober against the production endpoints.
func NewProber() *Prober {
	return &Prober{HTTP: &http.Client{Timeout: 15 * time.Second}, Endpoints: DefaultEndpoints, Now: time.Now}
}

// ExpandPath resolves ~ and ~user against the daemon user's home. A path for
// another user (~agents/...) resolves through that user's home directory.
func ExpandPath(p string) string {
	if !strings.HasPrefix(p, "~") {
		return p
	}
	rest := p[1:]
	slash := strings.IndexByte(rest, '/')
	user := rest
	tail := ""
	if slash >= 0 {
		user, tail = rest[:slash], rest[slash+1:]
	}
	if user == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return p
		}
		return filepath.Join(home, tail)
	}
	if home := userHome(user); home != "" {
		return filepath.Join(home, tail)
	}
	return p
}

// readCredential returns the trimmed credential and its state.
func readCredential(path string) (string, CredentialState) {
	if path == "" {
		return "", CredentialMissing
	}
	data, err := os.ReadFile(ExpandPath(path))
	if errors.Is(err, os.ErrNotExist) {
		return "", CredentialMissing
	}
	if err != nil {
		return "", CredentialUnreadable
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", CredentialMissing
	}
	return value, CredentialPresent
}

// CheckCredential reports whether the credential file is there and readable,
// without contacting the provider.
func CheckCredential(sub Subscription) CredentialState {
	_, st := readCredential(sub.CredentialPath)
	return st
}

// errNoProbe means the provider has no quota endpoint we know.
var errNoProbe = errors.New("no quota probe for this provider")

// Probe measures the subscription. A credential the provider refuses comes
// back as CredentialRevoked with no windows.
func (p *Prober) Probe(ctx context.Context, sub Subscription) (State, error) {
	value, cred := readCredential(sub.CredentialPath)
	st := State{Credential: cred, ObservedAt: p.Now()}
	if cred != CredentialPresent {
		return st, nil
	}
	var windows []Window
	var err error
	switch {
	case sub.Auth == "claude_oauth":
		windows, err = p.probeClaude(ctx, value, p.claudeModel(sub))
	case sub.Auth == "claude_login":
		token, ok := claudeLoginAccessToken(value, p.Now())
		if !ok {
			// Expired access token: Claude Code refreshes it on its next run.
			return st, nil
		}
		windows, err = p.probeClaude(ctx, token, p.claudeModel(sub))
	case sub.Auth == "codex_auth":
		windows, err = p.probeCodex(ctx, value)
	case sub.Provider == "zai":
		windows, err = p.probeZai(ctx, value)
	case sub.Provider == "minimax":
		windows, err = p.probeMinimax(ctx, value)
	case sub.Provider == "openrouter":
		windows, err = p.probeOpenRouter(ctx, value)
	default:
		return st, errNoProbe
	}
	var refused *refusedError
	if errors.As(err, &refused) {
		st.Credential = CredentialRevoked
		return st, nil
	}
	if err != nil {
		return st, err
	}
	st.Windows = windows
	return st, nil
}

func (p *Prober) claudeModel(sub Subscription) string {
	for _, w := range sub.Windows {
		if w == "fable" {
			return fableProbeModel
		}
	}
	if sub.Settings.ProbeModel != "" {
		return sub.Settings.ProbeModel
	}
	return DefaultClaudeProbeModel
}

type refusedError struct{ status int }

func (e *refusedError) Error() string { return fmt.Sprintf("credential refused (HTTP %d)", e.status) }

// do runs one request; 401/403 are a refused credential. Response bodies are
// never logged: they may echo request metadata.
func (p *Prober) do(req *http.Request) (*http.Response, []byte, error) {
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return resp, body, &refusedError{status: resp.StatusCode}
	}
	return resp, body, nil
}

func epoch(sec float64) *time.Time {
	if sec <= 0 {
		return nil
	}
	t := time.Unix(int64(sec), 0).UTC()
	return &t
}

// probeClaude sends a one-token request and reads the unified rate-limit
// headers (the usage endpoint answers 429 to setup tokens). A 429 still
// carries the headers. "rejected" is the only provider refusal: Anthropic
// sends allowed_warning from 75% of a week.
func (p *Prober) probeClaude(ctx context.Context, token, model string) ([]Window, error) {
	if !strings.HasPrefix(token, "sk-ant-oat") {
		return nil, &refusedError{status: http.StatusUnauthorized}
	}
	body, _ := json.Marshal(map[string]any{
		"model": model, "max_tokens": 1,
		"system":   "You are Claude Code, Anthropic's official CLI for Claude.",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Endpoints.Anthropic+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("content-type", "application/json")
	resp, _, err := p.do(req)
	if err != nil {
		return nil, err
	}
	h := func(k string) string { return resp.Header.Get("anthropic-ratelimit-unified-" + k) }
	var out []Window
	for _, w := range []struct{ key, hdr string }{{"5h", "5h"}, {"week", "7d"}, {"fable", "7d_oi"}} {
		raw := h(w.hdr + "-utilization")
		if raw == "" {
			continue
		}
		used, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			continue
		}
		reset, _ := strconv.ParseFloat(h(w.hdr+"-reset"), 64)
		out = append(out, Window{Key: w.key, Used: used * 100, ResetAt: epoch(reset), Limit: h(w.hdr+"-status") == "rejected"})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("claude probe: no rate-limit headers (HTTP %d)", resp.StatusCode)
	}
	return out, nil
}

// claudeLoginAccessToken reads Claude Code's .credentials.json; false when the
// access token is about to expire.
func claudeLoginAccessToken(raw string, now time.Time) (string, bool) {
	var creds struct {
		ClaudeAiOauth struct {
			AccessToken string  `json:"accessToken"`
			ExpiresAt   float64 `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal([]byte(raw), &creds) != nil || creds.ClaudeAiOauth.AccessToken == "" {
		return "", false
	}
	if creds.ClaudeAiOauth.ExpiresAt/1000 < float64(now.Add(time.Minute).Unix()) {
		return "", false
	}
	return creds.ClaudeAiOauth.AccessToken, true
}

func (p *Prober) probeCodex(ctx context.Context, raw string) ([]Window, error) {
	var auth struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal([]byte(raw), &auth) != nil || auth.Tokens.AccessToken == "" {
		return nil, &refusedError{status: http.StatusUnauthorized}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.Endpoints.Codex+"/backend-api/wham/usage", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+auth.Tokens.AccessToken)
	req.Header.Set("ChatGPT-Account-Id", auth.Tokens.AccountID)
	req.Header.Set("User-Agent", "codex_cli_rs")
	resp, body, err := p.do(req)
	var refused *refusedError
	if errors.As(err, &refused) && refused.status == http.StatusUnauthorized {
		// An expired access token: codex refreshes it on its next run, so the
		// account is not revoked — the load is just unknown for now.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("codex probe: HTTP %d", resp.StatusCode)
	}
	type codexWindow struct {
		UsedPercent float64 `json:"used_percent"`
		ResetAt     float64 `json:"reset_at"`
	}
	var d struct {
		RateLimit struct {
			Primary   codexWindow `json:"primary_window"`
			Secondary codexWindow `json:"secondary_window"`
			Limit     bool        `json:"limit_reached"`
		} `json:"rate_limit"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("codex probe: %w", err)
	}
	rl := d.RateLimit
	return []Window{
		{Key: "5h", Used: rl.Primary.UsedPercent, ResetAt: epoch(rl.Primary.ResetAt), Limit: rl.Limit},
		{Key: "week", Used: rl.Secondary.UsedPercent, ResetAt: epoch(rl.Secondary.ResetAt), Limit: rl.Limit},
	}, nil
}

func (p *Prober) getJSON(ctx context.Context, url, auth string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	resp, body, err := p.do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("quota probe: HTTP %d", resp.StatusCode)
	}
	return json.Unmarshal(body, out)
}

func (p *Prober) probeZai(ctx context.Context, key string) ([]Window, error) {
	var d struct {
		Data struct {
			Limits []struct {
				Unit          int     `json:"unit"`
				Percentage    float64 `json:"percentage"`
				NextResetTime float64 `json:"nextResetTime"`
			} `json:"limits"`
		} `json:"data"`
	}
	if err := p.getJSON(ctx, p.Endpoints.Zai+"/api/monitor/usage/quota/limit", key, &d); err != nil {
		return nil, err
	}
	names := map[int]string{3: "5h", 6: "week"}
	var out []Window
	for _, l := range d.Data.Limits {
		name, ok := names[l.Unit]
		if !ok {
			name = fmt.Sprintf("unit%d", l.Unit)
		}
		out = append(out, Window{Key: name, Used: l.Percentage, ResetAt: epoch(l.NextResetTime / 1000)})
	}
	return out, nil
}

func (p *Prober) probeMinimax(ctx context.Context, key string) ([]Window, error) {
	var d struct {
		ModelRemains []struct {
			ModelName                     string  `json:"model_name"`
			CurrentIntervalRemainingPct   float64 `json:"current_interval_remaining_percent"`
			CurrentWeeklyRemainingPercent float64 `json:"current_weekly_remaining_percent"`
			EndTime                       float64 `json:"end_time"`
			WeeklyEndTime                 float64 `json:"weekly_end_time"`
		} `json:"model_remains"`
	}
	if err := p.getJSON(ctx, p.Endpoints.Minimax+"/v1/api/openplatform/coding_plan/remains", "Bearer "+key, &d); err != nil {
		return nil, err
	}
	for _, m := range d.ModelRemains {
		if m.ModelName == "general" {
			return []Window{
				{Key: "5h", Used: 100 - m.CurrentIntervalRemainingPct, ResetAt: epoch(m.EndTime / 1000)},
				{Key: "week", Used: 100 - m.CurrentWeeklyRemainingPercent, ResetAt: epoch(m.WeeklyEndTime / 1000)},
			}, nil
		}
	}
	return nil, errors.New("minimax probe: no general model quota")
}

func (p *Prober) probeOpenRouter(ctx context.Context, key string) ([]Window, error) {
	var d struct {
		Data struct {
			TotalCredits float64 `json:"total_credits"`
			TotalUsage   float64 `json:"total_usage"`
		} `json:"data"`
	}
	if err := p.getJSON(ctx, p.Endpoints.OpenRouter+"/api/v1/credits", "Bearer "+key, &d); err != nil {
		return nil, err
	}
	if d.Data.TotalCredits <= 0 {
		return nil, nil
	}
	return []Window{{Key: "credits", Used: 100 * d.Data.TotalUsage / d.Data.TotalCredits}}, nil
}
