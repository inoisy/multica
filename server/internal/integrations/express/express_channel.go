package express

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
)

// expressChannel is one installation as the engine.Supervisor sees it. BotX is
// push-only — CTS calls the webhook — so there is no receive loop to run:
// Connect just holds until the Supervisor cancels it. Registering the Factory
// still matters: it keeps the installation a supervised, Send-capable Channel
// like every other platform.
type expressChannel struct {
	api    *botxAPI
	logger *slog.Logger
}

func (c *expressChannel) Type() channel.Type { return TypeExpress }

func (c *expressChannel) Capabilities() channel.Capability { return channel.CapText }

func (c *expressChannel) Connect(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (c *expressChannel) Disconnect(context.Context) error { return nil }

func (c *expressChannel) Send(ctx context.Context, out channel.OutboundMessage) (channel.SendResult, error) {
	ids, err := c.api.sendText(ctx, out.ChatID, out.Text)
	if err != nil {
		return channel.SendResult{MessageIDs: ids}, err
	}
	res := channel.SendResult{MessageIDs: ids}
	if len(ids) > 0 {
		res.MessageID = ids[len(ids)-1]
	}
	return res, nil
}

// ChannelDeps are the shared dependencies the Factory closes over.
type ChannelDeps struct {
	Decrypt    Decrypter
	HTTPClient *http.Client
	Logger     *slog.Logger
}

// RegisterExpress registers the per-installation Factory with the Supervisor's
// registry. Same contract as telegram.RegisterTelegram.
func RegisterExpress(reg *channel.Registry, deps ChannelDeps) {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	reg.Register(TypeExpress, func(cfg channel.Config) (channel.Channel, error) {
		creds, err := decodeCredentials(json.RawMessage(cfg.Raw), deps.Decrypt)
		if err != nil {
			return nil, fmt.Errorf("express: %w", err)
		}
		return &expressChannel{api: newBotxAPI(creds, deps.HTTPClient, sharedTokens), logger: logger}, nil
	})
}
