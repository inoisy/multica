// Package express is the eXpress (BotX) integration for the channel-agnostic
// engine. eXpress is a corporate messenger whose bots talk to the messenger
// through a CTS server (the on-premise eXpress node) over the BotX API.
//
// Install model is bring-your-own-bot, like Telegram and DingTalk: the CTS
// administrator registers a bot, then a workspace admin pastes its CTS host,
// bot_id and secret_key into Multica. The installation is keyed by bot_id
// (config->>'app_id').
//
// Unlike the long-connection adapters, BotX is push-only: CTS delivers every
// event to the bot over HTTP (POST /command, GET /status, POST
// /notification/callback) and signs it with a JWT made from the bot's secret
// key. The inbound side therefore lives in webhook.go, a public HTTP handler
// that hands each verified command straight to the engine's inbound handler.
// The per-installation Channel the Supervisor builds owns no receive loop.
//
// Outbound replies go back as direct notifications
// (POST /api/v4/botx/notifications/direct) with a bot token minted from the
// bot's secret key (botx.go); delivery happens once per finished run, like
// DingTalk (outbound.go).
package express

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
)

// TypeExpress is the channel discriminator for the eXpress adapter. Defined
// with the adapter, not in the channel core, so registering the platform never
// edits the core.
const TypeExpress channel.Type = "express"

// installConfig is the JSON stored in channel_installation.config.
//
// app_id holds the bot_id (a UUID issued by CTS). It is the routing key of the
// generic (channel_type, config->>'app_id') index, so one eXpress bot maps to
// one agent across all workspaces.
//
// cts_url is the CTS base URL the bot was registered on; outbound calls and the
// token mint go there. secret_key_encrypted is base64 secretbox ciphertext,
// never plaintext.
type installConfig struct {
	AppID              string `json:"app_id"`
	CTSURL             string `json:"cts_url"`
	BotName            string `json:"bot_name,omitempty"`
	SecretKeyEncrypted string `json:"secret_key_encrypted"`
}

// credentials is the decoded, decrypted form the transports run on.
type credentials struct {
	BotID     string
	CTSURL    string
	SecretKey string
}

// Decrypter turns stored ciphertext into plaintext. The wiring injects a
// secretbox-backed implementation; tests pass nil (stored bytes are plaintext).
type Decrypter func(ciphertext []byte) (plaintext []byte, err error)

func decodeCredentials(raw json.RawMessage, decrypt Decrypter) (credentials, error) {
	if len(raw) == 0 {
		return credentials{}, errors.New("express: empty installation config")
	}
	var cfg installConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return credentials{}, fmt.Errorf("decode express installation config: %w", err)
	}
	secret, err := decryptSecret(cfg.SecretKeyEncrypted, decrypt)
	if err != nil {
		return credentials{}, fmt.Errorf("decrypt secret key: %w", err)
	}
	if cfg.AppID == "" || cfg.CTSURL == "" || secret == "" {
		return credentials{}, errors.New("express: installation config is incomplete")
	}
	return credentials{BotID: cfg.AppID, CTSURL: cfg.CTSURL, SecretKey: secret}, nil
}

// PublicConfig is the non-secret subset of an installation config, safe to
// return from the management API.
type PublicConfig struct {
	BotID   string
	CTSURL  string
	BotName string
}

// DecodePublicConfig extracts display-safe fields; a decode miss yields a zero
// value so the management list still renders the row.
func DecodePublicConfig(raw json.RawMessage) PublicConfig {
	var cfg installConfig
	_ = json.Unmarshal(raw, &cfg)
	return PublicConfig{BotID: cfg.AppID, CTSURL: cfg.CTSURL, BotName: cfg.BotName}
}

func decryptSecret(enc string, decrypt Decrypter) (string, error) {
	if enc == "" {
		return "", nil
	}
	ciphertext, err := base64.StdEncoding.DecodeString(stripWhitespace(enc))
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}
	if decrypt == nil {
		return string(ciphertext), nil
	}
	plaintext, err := decrypt(ciphertext)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func stripWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '\r':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ErrInvalidCTSURL is returned when the pasted CTS address is not a usable
// http(s) origin.
var ErrInvalidCTSURL = errors.New("express: CTS address must look like https://cts.example.com")

// ErrInvalidBotID is returned when the pasted bot_id is not a UUID.
var ErrInvalidBotID = errors.New("express: bot_id must be the UUID CTS issued for the bot")

// ErrInvalidSecretKey is returned when the secret key is empty.
var ErrInvalidSecretKey = errors.New("express: secret_key is required")

// normalizeCTSURL accepts "cts.example.com" or "https://cts.example.com/" and
// returns the bare origin "https://cts.example.com". A path is refused: BotX
// endpoints live at the CTS root.
func normalizeCTSURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", ErrInvalidCTSURL
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", ErrInvalidCTSURL
	}
	if p := strings.Trim(u.Path, "/"); p != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", ErrInvalidCTSURL
	}
	return u.Scheme + "://" + u.Host, nil
}

// normalizeBotID lower-cases and validates a UUID-shaped bot id.
func normalizeBotID(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if !isUUID(s) {
		return "", ErrInvalidBotID
	}
	return s, nil
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
				return false
			}
		}
	}
	return true
}
