package express

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Webhook is the public HTTP surface CTS calls. One base URL serves every
// eXpress installation; the bot_id in the request picks the installation and
// the JWT proves the request came from that bot's CTS:
//
//	POST {base}/command                → a member's message or a system event
//	GET  {base}/status                 → bot menu shown in the eXpress client
//	POST {base}/notification/callback  → delivery result of a sent notification
//
// The CTS administrator registers {base} — https://<multica>/api/express — as
// the bot's URL.
type Webhook struct {
	q       webhookQueries
	decrypt Decrypter
	handler channel.InboundHandler
	client  *http.Client
	tokens  *tokenCache
	logger  *slog.Logger
	now     func() time.Time
	// async runs the engine hand-off off the request path so CTS gets its 202
	// at once. Tests replace it with a synchronous call.
	async func(func())
}

type webhookQueries interface {
	GetChannelInstallationByAppID(ctx context.Context, arg db.GetChannelInstallationByAppIDParams) (db.ChannelInstallation, error)
}

// WebhookConfig wires the webhook to the engine and the database.
type WebhookConfig struct {
	Queries webhookQueries
	Decrypt Decrypter
	// Handler is the engine's shared inbound entry point (engine.Router.Handle).
	Handler    channel.InboundHandler
	HTTPClient *http.Client
	Logger     *slog.Logger
}

// NewWebhook builds the CTS-facing handler.
func NewWebhook(cfg WebhookConfig) *Webhook {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Webhook{
		q: cfg.Queries, decrypt: cfg.Decrypt, handler: cfg.Handler,
		client: cfg.HTTPClient, tokens: sharedTokens, logger: logger, now: time.Now,
		async: func(f func()) { go f() },
	}
}

// inboundTimeout bounds one message's trip through the engine once CTS has
// been answered.
const inboundTimeout = 60 * time.Second

const maxCommandBytes = 4 << 20

// Command handles POST {base}/command.
func (w *Webhook) Command(rw http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxCommandBytes))
	if err != nil {
		writeBotxError(rw, http.StatusBadRequest, "bad_request", "could not read body")
		return
	}
	var cmd botxCommand
	if err := json.Unmarshal(body, &cmd); err != nil || cmd.BotID == "" {
		writeBotxError(rw, http.StatusBadRequest, "bad_request", "not a BotX command")
		return
	}
	botID := strings.ToLower(cmd.BotID)
	inst, creds, ok := w.authorize(rw, r, botID)
	if !ok {
		return
	}
	msg, kind := inboundFromCommand(cmd, botID)
	writeAccepted(rw)
	switch kind {
	case inboundIgnore:
		return
	case inboundUnsupported:
		if msg.AddressedToBot {
			w.async(func() {
				ctx, cancel := context.WithTimeout(context.Background(), defaultHTTPTimeout)
				defer cancel()
				if _, err := newBotxAPI(creds, w.client, w.tokens).sendText(ctx, msg.Source.ChatID, msgUnsupportedType); err != nil {
					w.logger.WarnContext(ctx, "express: unsupported-type notice failed", "error", err)
				}
			})
		}
		return
	}
	if inst.Status != "active" {
		// Revoked bots still reach the engine: it records the drop and the
		// replier tells an /issue sender the bot was disconnected.
		w.logger.Info("express: message for a revoked installation", "bot_id", botID)
	}
	if w.handler == nil {
		w.logger.Error("express: inbound handler not configured")
		return
	}
	w.async(func() {
		ctx, cancel := context.WithTimeout(context.Background(), inboundTimeout)
		defer cancel()
		if err := w.handler(ctx, msg); err != nil {
			w.logger.ErrorContext(ctx, "express: engine rejected inbound message",
				"bot_id", botID, "sync_id", msg.MessageID, "error", err)
		}
	})
}

// Status handles GET {base}/status: the menu the eXpress client shows for the
// bot. The commands are the engine's shared controls.
func (w *Webhook) Status(rw http.ResponseWriter, r *http.Request) {
	botID := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("bot_id")))
	if botID == "" {
		writeBotxError(rw, http.StatusBadRequest, "bad_request", "bot_id is required")
		return
	}
	inst, _, ok := w.authorize(rw, r, botID)
	if !ok {
		return
	}
	enabled := inst.Status == "active"
	message := "Multica: напишите задачу или вопрос агенту"
	if !enabled {
		message = "Бот отключён от Multica"
	}
	writeJSON(rw, http.StatusOK, map[string]any{
		"status": "ok",
		"result": map[string]any{
			"enabled":        enabled,
			"status_message": message,
			"commands": []map[string]string{
				{"body": "/new", "name": "Новый чат", "description": "Начать новый чат с агентом"},
				{"body": "/clear", "name": "Очистить контекст", "description": "Следующее сообщение — без прошлой переписки"},
				{"body": "/issue", "name": "Создать задачу", "description": "/issue <заголовок> — задача на агента"},
			},
		},
	})
}

// NotificationCallback handles POST {base}/notification/callback: CTS
// reports the outcome of a notification sent with a sync_id. Delivery is
// best-effort, so a failure is only logged.
func (w *Webhook) NotificationCallback(rw http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var cb struct {
		SyncID string          `json:"sync_id"`
		Status string          `json:"status"`
		Reason string          `json:"reason"`
		Errors json.RawMessage `json:"errors"`
	}
	_ = json.Unmarshal(body, &cb)
	if cb.Status == "error" {
		w.logger.Warn("express: CTS reported a failed notification",
			"sync_id", cb.SyncID, "reason", cb.Reason, "errors", string(cb.Errors))
	}
	writeAccepted(rw)
}

// authorize loads the installation for botID and verifies the request JWT
// with that bot's secret. It writes the error response itself.
func (w *Webhook) authorize(rw http.ResponseWriter, r *http.Request, botID string) (db.ChannelInstallation, credentials, bool) {
	inst, err := w.q.GetChannelInstallationByAppID(r.Context(), db.GetChannelInstallationByAppIDParams{
		ChannelType: string(TypeExpress),
		AppID:       botID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeBotxError(rw, http.StatusServiceUnavailable, "bot_disabled", "bot is not connected to Multica")
			return db.ChannelInstallation{}, credentials{}, false
		}
		w.logger.ErrorContext(r.Context(), "express: installation lookup failed", "bot_id", botID, "error", err)
		writeBotxError(rw, http.StatusInternalServerError, "internal_error", "installation lookup failed")
		return db.ChannelInstallation{}, credentials{}, false
	}
	creds, err := decodeCredentials(inst.Config, w.decrypt)
	if err != nil {
		w.logger.ErrorContext(r.Context(), "express: decode credentials failed", "bot_id", botID, "error", err)
		writeBotxError(rw, http.StatusInternalServerError, "internal_error", "installation is misconfigured")
		return db.ChannelInstallation{}, credentials{}, false
	}
	if err := verifyRequestJWT(r.Header.Get("Authorization"), creds.BotID, creds.SecretKey, w.now()); err != nil {
		w.logger.WarnContext(r.Context(), "express: rejected unsigned request", "bot_id", botID, "error", err)
		writeBotxError(rw, http.StatusUnauthorized, "invalid_token", "request is not signed for this bot")
		return db.ChannelInstallation{}, credentials{}, false
	}
	return inst, creds, true
}

func writeAccepted(rw http.ResponseWriter) {
	writeJSON(rw, http.StatusAccepted, map[string]string{"result": "accepted"})
}

func writeBotxError(rw http.ResponseWriter, status int, reason, message string) {
	writeJSON(rw, status, map[string]any{
		"status":     "error",
		"reason":     reason,
		"errors":     []string{message},
		"error_data": map[string]string{"status_message": message},
	})
}

func writeJSON(rw http.ResponseWriter, status int, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(v)
}

// sharedTokens is the process-wide bot-token cache shared by the webhook,
// replier and outbound so one bot mints one token at a time.
var sharedTokens = newTokenCache()
