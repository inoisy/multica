package express

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
)

// botxCommand is the body CTS POSTs to the bot's /command endpoint (BotX
// protocol v4). Only the fields the adapter reads are declared.
type botxCommand struct {
	SyncID       string       `json:"sync_id"`
	SourceSyncID *string      `json:"source_sync_id"`
	BotID        string       `json:"bot_id"`
	ProtoVersion int          `json:"proto_version"`
	Command      botxCmdBody  `json:"command"`
	From         botxSender   `json:"from"`
	Entities     []botxEntity `json:"entities"`
	Attachments  []any        `json:"attachments"`
	AsyncFiles   []any        `json:"async_files"`
}

type botxCmdBody struct {
	Body        string `json:"body"`
	CommandType string `json:"command_type"` // "user" | "system"
}

type botxSender struct {
	UserHUID    *string `json:"user_huid"`
	GroupChatID *string `json:"group_chat_id"`
	ChatType    string  `json:"chat_type"` // "chat" | "group_chat" | "channel"
	Username    *string `json:"username"`
	ADLogin     *string `json:"ad_login"`
	ADDomain    *string `json:"ad_domain"`
	Host        string  `json:"host"`
}

type botxEntity struct {
	Type string          `json:"type"` // "mention" | "reply" | "forward"
	Data json.RawMessage `json:"data"`
}

type botxMention struct {
	MentionType string `json:"mention_type"` // "contact" | "user" | "chat" | "channel" | "all"
	MentionID   string `json:"mention_id"`
	MentionData *struct {
		UserHUID string `json:"user_huid"`
		Name     string `json:"name"`
	} `json:"mention_data"`
}

type botxReply struct {
	SourceSyncID string `json:"source_sync_id"`
	Sender       string `json:"sender"`
	Body         string `json:"body"`
}

// expressRawEvent carries the eXpress-specific fields the cross-platform
// envelope does not; read back only inside this package's resolvers.
type expressRawEvent struct {
	BotID      string `json:"bot_id"`
	EventType  string `json:"event_type"`
	SenderName string `json:"sender_name,omitempty"`
}

// mentionToken matches the placeholder eXpress puts in a message body for each
// mention entity: "@{mention:<mention_id>}".
var mentionToken = regexp.MustCompile(`@\{mention:([0-9a-fA-F-]+)\}`)

const msgUnsupportedType = "Пока понимаю только текст — напишите сообщение словами."

// inboundKind classifies what the webhook should do with a command.
type inboundKind int

const (
	inboundIgnore      inboundKind = iota // system event, bot's own message, channel post
	inboundText                           // a member's text message
	inboundUnsupported                    // a member's file/voice without text
)

// inboundFromCommand translates one BotX command into the engine's normalized
// message. botID is the installation's bot (the request was verified for it).
func inboundFromCommand(cmd botxCommand, botID string) (channel.InboundMessage, inboundKind) {
	if cmd.Command.CommandType != "user" || cmd.From.UserHUID == nil || cmd.From.GroupChatID == nil {
		return channel.InboundMessage{}, inboundIgnore
	}
	senderID := strings.ToLower(*cmd.From.UserHUID)
	if senderID == "" || senderID == botID {
		return channel.InboundMessage{}, inboundIgnore
	}
	var chatType channel.ChatType
	switch cmd.From.ChatType {
	case "chat":
		chatType = channel.ChatTypeP2P
	case "group_chat":
		chatType = channel.ChatTypeGroup
	default:
		// Channels are broadcast-only; a bot there has no one to answer.
		return channel.InboundMessage{}, inboundIgnore
	}

	mentions := map[string]botxMention{}
	mentioned := false
	repliedToBot := false
	var quoted *botxReply
	for _, e := range cmd.Entities {
		switch e.Type {
		case "mention":
			var m botxMention
			if json.Unmarshal(e.Data, &m) != nil {
				continue
			}
			mentions[strings.ToLower(m.MentionID)] = m
			if m.MentionData != nil && strings.EqualFold(m.MentionData.UserHUID, botID) {
				mentioned = true
			}
		case "reply":
			var r botxReply
			if json.Unmarshal(e.Data, &r) != nil {
				continue
			}
			if strings.EqualFold(r.Sender, botID) {
				repliedToBot = true
			}
			quoted = &r
		}
	}
	addressed := chatType == channel.ChatTypeP2P || mentioned || repliedToBot

	cleaned := strings.TrimSpace(renderMentions(cmd.Command.Body, mentions, botID))
	kind := inboundText
	if cleaned == "" {
		if len(cmd.Attachments) > 0 || len(cmd.AsyncFiles) > 0 {
			kind = inboundUnsupported
		} else {
			return channel.InboundMessage{}, inboundIgnore
		}
	}

	// CommandText is the member's own instruction; Text may gain the quoted
	// message in front (selected context), the same split Telegram keeps so
	// a quoted "/issue" never fires and a quote never becomes a Chat title.
	agentText := cleaned
	hasSelectedContext := false
	if quoted != nil && addressed && !repliedToBot && strings.TrimSpace(quoted.Body) != "" {
		agentText = "<quoted_message>\n" + strings.TrimSpace(renderMentions(quoted.Body, mentions, botID)) +
			"\n</quoted_message>\n\n" + cleaned
		hasSelectedContext = true
	}

	senderName := ""
	if cmd.From.Username != nil {
		senderName = *cmd.From.Username
	}
	raw, _ := json.Marshal(expressRawEvent{BotID: botID, EventType: "message", SenderName: senderName})

	var reply *channel.ReplyCtx
	if quoted != nil && quoted.SourceSyncID != "" {
		reply = &channel.ReplyCtx{MessageID: quoted.SourceSyncID}
	}
	msgType := channel.MsgTypeText
	if kind == inboundUnsupported {
		msgType = channel.MsgTypeUnknown
	}
	return channel.InboundMessage{
		EventID:            cmd.SyncID,
		MessageID:          cmd.SyncID, // sync_id is globally unique in CTS
		Type:               msgType,
		Text:               agentText,
		CommandText:        cleaned,
		HasSelectedContext: hasSelectedContext,
		ReplyTo:            reply,
		AddressedToBot:     addressed,
		Source: channel.Source{
			ChannelType:    TypeExpress,
			ChatID:         strings.ToLower(*cmd.From.GroupChatID),
			ChatType:       chatType,
			SenderID:       senderID,
			SenderStableID: senderID, // HUIDs are global within a CTS
		},
		Raw: raw,
	}, kind
}

// renderMentions replaces "@{mention:id}" placeholders: the bot's own mention
// is transport syntax and is dropped; other mentions read as "@Name".
func renderMentions(body string, mentions map[string]botxMention, botID string) string {
	return mentionToken.ReplaceAllStringFunc(body, func(tok string) string {
		id := strings.ToLower(mentionToken.FindStringSubmatch(tok)[1])
		m, ok := mentions[id]
		if !ok || m.MentionData == nil {
			return ""
		}
		if strings.EqualFold(m.MentionData.UserHUID, botID) {
			return ""
		}
		if m.MentionData.Name != "" {
			return "@" + m.MentionData.Name
		}
		return ""
	})
}
