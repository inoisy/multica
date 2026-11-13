package express

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// outboundQueries is the slice of generated queries the outbound subscriber
// needs. *db.Queries satisfies it.
type outboundQueries interface {
	GetAgentTask(ctx context.Context, id pgtype.UUID) (db.AgentTaskQueue, error)
	TaskHasChannelIngestedMessages(ctx context.Context, taskID pgtype.UUID) (bool, error)
	GetChannelTaskDelivery(ctx context.Context, taskID pgtype.UUID) (db.ChannelTaskDelivery, error)
	GetChannelInstallation(ctx context.Context, arg db.GetChannelInstallationParams) (db.ChannelInstallation, error)
}

// Outbound delivers an agent's finished chat reply back to eXpress. On
// EventChatDone / EventTaskFailed it finds the eXpress delivery for the task
// and posts the reply (or failure notice) into the originating chat. Tasks
// delivered to other channels are ignored, so it shares the event bus with the
// other adapters. Same shape as dingtalk.Outbound, without reactions.
type Outbound struct {
	q       outboundQueries
	decrypt Decrypter
	client  *http.Client
	logger  *slog.Logger
	// send is the network call; tests replace it.
	send func(ctx context.Context, creds credentials, chatID, text string) error
}

// NewOutbound builds the outbound subscriber.
func NewOutbound(q outboundQueries, decrypt Decrypter, client *http.Client, logger *slog.Logger) *Outbound {
	if logger == nil {
		logger = slog.Default()
	}
	o := &Outbound{q: q, decrypt: decrypt, client: client, logger: logger}
	o.send = func(ctx context.Context, creds credentials, chatID, text string) error {
		_, err := newBotxAPI(creds, o.client, sharedTokens).sendText(ctx, chatID, text)
		return err
	}
	return o
}

// Register subscribes to chat-task terminal events.
func (o *Outbound) Register(bus *events.Bus) {
	bus.Subscribe(protocol.EventChatDone, o.handleEvent)
	bus.Subscribe(protocol.EventTaskFailed, o.handleEvent)
}

// outboundTimeout covers a token mint plus a few notifications of a long
// reply. Bus delivery is synchronous, so a stuck CTS must not wedge the
// publisher for long.
const outboundTimeout = 20 * time.Second

func (o *Outbound) handleEvent(e events.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), outboundTimeout)
	defer cancel()
	if err := o.processEvent(ctx, e); err != nil {
		o.logger.WarnContext(ctx, "express outbound: reply delivery failed",
			"error", err, "chat_session_id", e.ChatSessionID)
	}
}

func (o *Outbound) processEvent(ctx context.Context, e events.Event) error {
	taskID, sessionID, ok := taskAndSessionFromEvent(e)
	if !ok || !sessionID.Valid {
		return nil // issue / autopilot tasks carry no chat session
	}
	content := eventContent(e)
	if content == "" {
		return nil
	}
	delivery, err := o.q.GetChannelTaskDelivery(ctx, taskID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("lookup express task delivery: %w", err)
	}
	if delivery.ChannelType != string(TypeExpress) {
		return nil
	}
	task, err := o.q.GetAgentTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("load agent task: %w", err)
	}
	deliver, err := engine.TaskInputIsChannelIngested(ctx, o.q, task)
	if err != nil {
		return fmt.Errorf("classify task input origin: %w", err)
	}
	if !deliver {
		// The turn came from the web chat; it stays there.
		return nil
	}
	inst, err := o.q.GetChannelInstallation(ctx, db.GetChannelInstallationParams{
		ID: delivery.InstallationID, ChannelType: string(TypeExpress),
	})
	if err != nil {
		return fmt.Errorf("load express installation: %w", err)
	}
	if inst.Status != "active" {
		return nil // revoked between trigger and reply
	}
	creds, err := decodeCredentials(inst.Config, o.decrypt)
	if err != nil {
		return fmt.Errorf("decode express credentials: %w", err)
	}
	chatID := deliveryChatID(delivery)
	if chatID == "" {
		return errors.New("express delivery has no chat id")
	}
	if err := o.send(ctx, creds, chatID, content); err != nil {
		return fmt.Errorf("post express reply: %w", err)
	}
	return nil
}

// deliveryChatID prefers the chat id stored in the binding config (the
// adapter writes it on every binding) over the binding key.
func deliveryChatID(d db.ChannelTaskDelivery) string {
	var cfg expressBindingConfig
	if len(d.Config) > 0 && json.Unmarshal(d.Config, &cfg) == nil && cfg.ChatID != "" {
		return cfg.ChatID
	}
	return d.ChannelChatID
}

// eventContent extracts the deliverable text from an EventChatDone payload
// (typed, or its map form after a serialization round trip) or an
// EventTaskFailed payload. Empty means stay silent; a failure with an
// auto-retry pending stays silent too — the retry reports its own outcome.
func eventContent(e events.Event) string {
	switch p := e.Payload.(type) {
	case protocol.ChatDonePayload:
		return p.Content
	case map[string]any:
		if e.Type == protocol.EventTaskFailed {
			if retryPending, _ := p["retry_pending"].(bool); retryPending {
				return ""
			}
			if s, _ := p["error"].(string); s != "" {
				return "⚠️ " + s
			}
			return ""
		}
		if s, ok := p["content"].(string); ok {
			return s
		}
	}
	return ""
}

func taskAndSessionFromEvent(e events.Event) (taskID, sessionID pgtype.UUID, ok bool) {
	if e.TaskID != "" {
		_ = taskID.Scan(e.TaskID)
	}
	if e.ChatSessionID != "" {
		_ = sessionID.Scan(e.ChatSessionID)
	}
	switch p := e.Payload.(type) {
	case protocol.ChatDonePayload:
		if !taskID.Valid {
			_ = taskID.Scan(p.TaskID)
		}
		if !sessionID.Valid {
			_ = sessionID.Scan(p.ChatSessionID)
		}
	case map[string]any:
		if !taskID.Valid {
			if raw, _ := p["task_id"].(string); raw != "" {
				_ = taskID.Scan(raw)
			}
		}
		if !sessionID.Valid {
			if raw, _ := p["chat_session_id"].(string); raw != "" {
				_ = sessionID.Scan(raw)
			}
		}
	}
	return taskID, sessionID, taskID.Valid
}
