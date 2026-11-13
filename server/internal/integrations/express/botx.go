package express

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// This file is the BotX API client: token mint, direct notifications, and the
// verification of the JWT CTS puts on every request it sends the bot. Wire
// formats follow the BotX API the official pybotx SDK speaks:
//
//   - GET  {cts}/api/v2/botx/bots/{bot_id}/token?signature=<HMAC>  → token
//   - POST {cts}/api/v4/botx/notifications/direct                  → send
//   - inbound requests carry "Authorization: Bearer <JWT>", HS256-signed with
//     the bot's secret_key, audience = bot_id.

const (
	// maxBodyRunes is the BotX limit on a notification body. Longer replies are
	// split into several notifications.
	maxBodyRunes = 4096

	defaultHTTPTimeout = 15 * time.Second
	// tokenTTL bounds how long a minted bot token is reused. CTS tokens live
	// longer; re-minting early costs one request, and a 401 drops the cache.
	tokenTTL = 30 * time.Minute
)

var (
	// ErrCredentialsRejected means CTS answered the token mint with an auth
	// failure: wrong bot_id/secret_key pair for this CTS.
	ErrCredentialsRejected = errors.New("express: CTS rejected the bot_id / secret_key")
	// ErrCTSUnreachable means the CTS could not be reached or answered with
	// something that is not a BotX response.
	ErrCTSUnreachable = errors.New("express: could not reach CTS")
	// ErrUnauthorizedRequest is returned by VerifyRequest for a missing or
	// invalid JWT on an inbound request.
	ErrUnauthorizedRequest = errors.New("express: request is not signed by CTS for this bot")
)

// botxAPI is one bot's view of its CTS. Safe for concurrent use.
type botxAPI struct {
	base   string
	creds  credentials
	client *http.Client
	tokens *tokenCache
}

func newBotxAPI(creds credentials, client *http.Client, tokens *tokenCache) *botxAPI {
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	if tokens == nil {
		tokens = newTokenCache()
	}
	return &botxAPI{base: strings.TrimRight(creds.CTSURL, "/"), creds: creds, client: client, tokens: tokens}
}

// signature is the BotX token-mint signature: upper-case hex of
// HMAC-SHA256(key = secret_key, message = bot_id).
func signature(botID, secretKey string) string {
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(botID))
	return strings.ToUpper(hex.EncodeToString(mac.Sum(nil)))
}

type botxEnvelope struct {
	Status string          `json:"status"`
	Result json.RawMessage `json:"result"`
	Reason string          `json:"reason,omitempty"`
}

// mintToken asks CTS for a fresh bot token. It is also the live credential
// check the install path runs.
func (a *botxAPI) mintToken(ctx context.Context) (string, error) {
	u := fmt.Sprintf("%s/api/v2/botx/bots/%s/token?signature=%s",
		a.base, url.PathEscape(a.creds.BotID), url.QueryEscape(signature(a.creds.BotID, a.creds.SecretKey)))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrCTSUnreachable, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden ||
		resp.StatusCode == http.StatusNotFound:
		return "", fmt.Errorf("%w: HTTP %d", ErrCredentialsRejected, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("%w: token mint HTTP %d", ErrCTSUnreachable, resp.StatusCode)
	}
	var env botxEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return "", fmt.Errorf("%w: token mint returned non-JSON", ErrCTSUnreachable)
	}
	var token string
	if env.Status != "ok" || json.Unmarshal(env.Result, &token) != nil || token == "" {
		return "", fmt.Errorf("%w: token mint status %q", ErrCredentialsRejected, env.Status)
	}
	return token, nil
}

func (a *botxAPI) token(ctx context.Context) (string, error) {
	if t, ok := a.tokens.get(a.cacheKey()); ok {
		return t, nil
	}
	t, err := a.mintToken(ctx)
	if err != nil {
		return "", err
	}
	a.tokens.put(a.cacheKey(), t)
	return t, nil
}

func (a *botxAPI) cacheKey() string { return a.base + "|" + a.creds.BotID }

// directNotification is the body of POST /api/v4/botx/notifications/direct.
type directNotification struct {
	GroupChatID  string            `json:"group_chat_id"`
	Notification notificationBody  `json:"notification"`
	Opts         *notificationOpts `json:"opts,omitempty"`
}

type notificationBody struct {
	Status   string         `json:"status"`
	Body     string         `json:"body"`
	Metadata map[string]any `json:"metadata"`
	Opts     map[string]any `json:"opts"`
	Keyboard []any          `json:"keyboard"`
	Bubble   []any          `json:"bubble"`
	Mentions []any          `json:"mentions"`
}

type notificationOpts struct {
	// StealthMode / NotificationOpts are left default; the struct exists so a
	// future silent-send option does not change the request shape.
	RawMentions bool `json:"raw_mentions,omitempty"`
}

// sendText posts text into a chat, split into BotX-sized notifications. It
// returns the sync_id of every notification CTS accepted.
func (a *botxAPI) sendText(ctx context.Context, chatID, text string) ([]string, error) {
	var ids []string
	for _, part := range splitBody(text, maxBodyRunes) {
		id, err := a.sendOne(ctx, chatID, part, true)
		if err != nil {
			return ids, err
		}
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (a *botxAPI) sendOne(ctx context.Context, chatID, body string, retryOnAuth bool) (string, error) {
	token, err := a.token(ctx)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(directNotification{
		GroupChatID: chatID,
		Notification: notificationBody{
			Status: "ok", Body: body,
			Metadata: map[string]any{}, Opts: map[string]any{},
			Keyboard: []any{}, Bubble: []any{}, Mentions: []any{},
		},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+"/api/v4/botx/notifications/direct", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := a.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrCTSUnreachable, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized && retryOnAuth {
		// Expired or revoked token: mint a new one once.
		a.tokens.drop(a.cacheKey())
		return a.sendOne(ctx, chatID, body, false)
	}
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("express: send notification HTTP %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	var env botxEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", nil
	}
	var result struct {
		SyncID string `json:"sync_id"`
	}
	_ = json.Unmarshal(env.Result, &result)
	return result.SyncID, nil
}

// splitBody cuts text into parts of at most limit runes, preferring line
// breaks, then spaces, so a long answer stays readable across notifications.
func splitBody(text string, limit int) []string {
	runes := []rune(text)
	if len(runes) <= limit {
		return []string{text}
	}
	var parts []string
	for len(runes) > limit {
		cut := limit
		for i := limit; i > limit/2; i-- {
			if runes[i-1] == '\n' {
				cut = i
				break
			}
		}
		if cut == limit {
			for i := limit; i > limit/2; i-- {
				if runes[i-1] == ' ' {
					cut = i
					break
				}
			}
		}
		parts = append(parts, strings.TrimRight(string(runes[:cut]), " \n"))
		runes = runes[cut:]
	}
	if rest := strings.TrimSpace(string(runes)); rest != "" {
		parts = append(parts, rest)
	}
	return parts
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// verifyRequestJWT checks the Authorization header CTS puts on every request
// to the bot: an HS256 JWT signed with the bot's secret_key whose audience
// names the bot. exp/nbf are enforced with a small leeway for clock skew.
func verifyRequestJWT(authHeader, botID, secretKey string, now time.Time) error {
	raw, ok := strings.CutPrefix(strings.TrimSpace(authHeader), "Bearer ")
	if !ok || strings.TrimSpace(raw) == "" {
		return ErrUnauthorizedRequest
	}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithAudience(botID),
		jwt.WithLeeway(time.Minute),
		jwt.WithTimeFunc(func() time.Time { return now }),
	)
	if _, err := parser.Parse(strings.TrimSpace(raw), func(*jwt.Token) (any, error) {
		return []byte(secretKey), nil
	}); err != nil {
		return fmt.Errorf("%w: %v", ErrUnauthorizedRequest, err)
	}
	return nil
}

// tokenCache keeps minted bot tokens per (CTS, bot).
type tokenCache struct {
	mu  sync.Mutex
	m   map[string]cachedToken
	now func() time.Time
}

type cachedToken struct {
	value   string
	expires time.Time
}

func newTokenCache() *tokenCache {
	return &tokenCache{m: map[string]cachedToken{}, now: time.Now}
}

func (c *tokenCache) get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.m[key]
	if !ok || !c.now().Before(t.expires) {
		return "", false
	}
	return t.value, true
}

func (c *tokenCache) put(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key] = cachedToken{value: value, expires: c.now().Add(tokenTTL)}
}

func (c *tokenCache) drop(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, key)
}
