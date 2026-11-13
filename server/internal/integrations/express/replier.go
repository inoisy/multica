package express

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Verdict replies the engine asks for: binding prompt, offline notice, /new and
// /clear confirmations, /issue outcomes. eXpress is a Russian-speaking
// corporate messenger, so the copy is Russian.
const (
	msgAgentOffline     = "⚠️ Агент сейчас не в сети. Сообщение получено — ответ придёт, когда агент вернётся."
	msgAgentArchived    = "⚠️ Агент в архиве и не может ответить. Обратитесь к администратору workspace."
	msgBindingGroupHint = "Сначала напишите мне в личный чат и привяжите аккаунт Multica."
	msgFreshPending     = "✅ Контекст очищен. Следующее сообщение агент прочтёт без прошлой переписки."
	msgChatStarted      = "✅ Начат новый чат Multica. Следующее сообщение попадёт в него."
	msgIssueUsage       = "Укажите заголовок задачи:\n\n/issue <заголовок>\n[описание] (необязательно)"
	msgIssueNotMember   = "Вы не участник этого workspace Multica, поэтому задачу завести нельзя. Попросите администратора пригласить вас и повторите команду."
	msgIssueDisabled    = "Бот отключён от Multica. Попросите администратора workspace подключить его снова."
)

type bindingMinter interface {
	Mint(ctx context.Context, workspaceID, installationID pgtype.UUID, expressUserID string) (BindingToken, error)
}

// OutboundReplier posts the engine's verdict notices into the chat.
type OutboundReplier struct {
	binding     bindingMinter
	decrypt     Decrypter
	appURL      string
	bindingPath string
	client      *http.Client
	logger      *slog.Logger
}

// OutboundReplierConfig wires the replier.
type OutboundReplierConfig struct {
	Binding     bindingMinter
	Decrypt     Decrypter
	AppURL      string
	BindingPath string // default "/express/bind"
	HTTPClient  *http.Client
	Logger      *slog.Logger
}

var _ engine.OutboundReplier = (*OutboundReplier)(nil)

// NewOutboundReplier builds the verdict replier.
func NewOutboundReplier(cfg OutboundReplierConfig) *OutboundReplier {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	path := cfg.BindingPath
	if path == "" {
		path = "/express/bind"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return &OutboundReplier{
		binding: cfg.Binding, decrypt: cfg.Decrypt,
		appURL: strings.TrimRight(cfg.AppURL, "/"), bindingPath: path,
		client: cfg.HTTPClient, logger: logger,
	}
}

// Reply renders one engine outcome as a chat message. Failures are logged.
func (r *OutboundReplier) Reply(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage, res engine.Result) {
	var text string
	switch res.Outcome {
	case engine.OutcomeNeedsBinding:
		if err := r.sendBindingPrompt(ctx, inst, msg, res); err != nil {
			r.logger.WarnContext(ctx, "express replier: binding prompt failed",
				"installation_id", util.UUIDToString(inst.ID), "error", err)
		}
		return
	case engine.OutcomeAgentOffline:
		text = msgAgentOffline
	case engine.OutcomeAgentArchived:
		text = msgAgentArchived
	case engine.OutcomeFreshPending:
		text = msgFreshPending
	case engine.OutcomeChatStarted:
		text = msgChatStarted
	case engine.OutcomeIssueUsage:
		text = msgIssueUsage
	case engine.OutcomeIngested:
		if res.IssueID.Valid {
			text = issueCreatedText(res)
			if res.IssueDuplicate {
				text = issueDuplicateText(res)
			}
		}
	case engine.OutcomeDropped:
		text = droppedReplyText(res, msg)
	}
	if text == "" {
		return
	}
	if err := r.post(ctx, inst, msg, text); err != nil {
		r.logger.WarnContext(ctx, "express replier: notice failed",
			"installation_id", util.UUIDToString(inst.ID), "outcome", res.Outcome, "error", err)
	}
}

func (r *OutboundReplier) sendBindingPrompt(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage, res engine.Result) error {
	if msg.Source.ChatType == channel.ChatTypeGroup {
		return r.post(ctx, inst, msg, msgBindingGroupHint)
	}
	sender := res.Sender
	if sender == "" {
		sender = msg.Source.SenderID
	}
	if sender == "" {
		return errors.New("missing sender id")
	}
	if r.binding == nil || r.appURL == "" {
		return errors.New("binding service or app url not configured")
	}
	token, err := r.binding.Mint(ctx, inst.WorkspaceID, inst.ID, sender)
	if err != nil {
		return fmt.Errorf("mint binding token: %w", err)
	}
	link := r.appURL + r.bindingPath + "?token=" + url.QueryEscape(token.Raw)
	return r.post(ctx, inst, msg, "👋 Чтобы общаться со мной, привяжите аккаунт eXpress к Multica:\n"+link+"\n(Ссылка действует 15 минут.)")
}

func (r *OutboundReplier) post(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage, text string) error {
	row, ok := inst.Platform.(db.ChannelInstallation)
	if !ok {
		return errors.New("installation platform row unavailable")
	}
	creds, err := decodeCredentials(row.Config, r.decrypt)
	if err != nil {
		return fmt.Errorf("decode credentials: %w", err)
	}
	_, err = newBotxAPI(creds, r.client, sharedTokens).sendText(ctx, msg.Source.ChatID, text)
	return err
}

func issueCreatedText(res engine.Result) string {
	id := issueResultIdentifier(res)
	if title := strings.TrimSpace(res.IssueTitle); title != "" {
		return "✅ Создана задача " + id + " — " + title
	}
	return "✅ Создана задача " + id
}

func issueDuplicateText(res engine.Result) string {
	id := issueResultIdentifier(res)
	if title := strings.TrimSpace(res.IssueTitle); title != "" {
		return "⚠️ Не создана — уже есть открытая задача " + id + ": " + title
	}
	return "⚠️ Не создана — уже есть открытая задача " + id
}

func issueResultIdentifier(res engine.Result) string {
	if res.IssueIdentifier != "" {
		return res.IssueIdentifier
	}
	if res.IssueNumber > 0 {
		return fmt.Sprintf("#%d", res.IssueNumber)
	}
	return util.UUIDToString(res.IssueID)
}

func isAddressedIssueCommand(msg channel.InboundMessage) bool {
	if !msg.AddressedToBot {
		return false
	}
	source := msg.CommandText
	if source == "" {
		source = msg.Text
	}
	_, ok := engine.ParseIssueCommand(source)
	return ok
}

func droppedReplyText(res engine.Result, msg channel.InboundMessage) string {
	if !isAddressedIssueCommand(msg) {
		return ""
	}
	switch res.DropReason {
	case engine.DropReasonNonWorkspaceMember:
		return msgIssueNotMember
	case engine.DropReasonRevokedInstallation:
		return msgIssueDisabled
	default:
		return ""
	}
}
