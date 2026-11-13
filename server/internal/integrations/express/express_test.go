package express

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	testBotID  = "8dada2c8-67a6-4434-9dec-570d244e78ee"
	testSecret = "secret-key"
	testUser   = "ab103983-6001-44e9-889e-d55feb295494"
	testChat   = "dea55ee4-7a9f-5da0-8c5d-f0a5c7a3a2b1"
)

func command(t *testing.T, chatType, body string, entities ...map[string]any) botxCommand {
	t.Helper()
	raw := map[string]any{
		"sync_id":       "a465f0f3-1354-491c-8f11-f400164295cb",
		"bot_id":        testBotID,
		"proto_version": 4,
		"command":       map[string]any{"body": body, "command_type": "user", "data": map[string]any{}},
		"from": map[string]any{
			"user_huid": testUser, "group_chat_id": testChat, "chat_type": chatType,
			"username": "Иван Петров", "host": "cts.example.com",
		},
		"entities":    entities,
		"attachments": []any{},
	}
	b, _ := json.Marshal(raw)
	var cmd botxCommand
	if err := json.Unmarshal(b, &cmd); err != nil {
		t.Fatal(err)
	}
	return cmd
}

func mention(id, huid, name string) map[string]any {
	return map[string]any{"type": "mention", "data": map[string]any{
		"mention_type": "contact", "mention_id": id,
		"mention_data": map[string]any{"user_huid": huid, "name": name},
	}}
}

func TestInboundPrivateMessageIsAddressed(t *testing.T) {
	msg, kind := inboundFromCommand(command(t, "chat", "  /issue Починить вход  "), testBotID)
	if kind != inboundText {
		t.Fatalf("kind = %v, want text", kind)
	}
	if !msg.AddressedToBot || msg.Source.ChatType != channel.ChatTypeP2P {
		t.Fatalf("private message must be addressed p2p: %+v", msg)
	}
	if msg.CommandText != "/issue Починить вход" || msg.Text != msg.CommandText {
		t.Fatalf("text = %q / %q", msg.Text, msg.CommandText)
	}
	if msg.Source.ChatID != testChat || msg.Source.SenderID != testUser || msg.MessageID == "" {
		t.Fatalf("source = %+v id=%q", msg.Source, msg.MessageID)
	}
	var raw expressRawEvent
	if err := json.Unmarshal(msg.Raw, &raw); err != nil || raw.BotID != testBotID {
		t.Fatalf("raw = %s", msg.Raw)
	}
}

func TestInboundGroupNeedsMention(t *testing.T) {
	_, kind := inboundFromCommand(command(t, "group_chat", "просто болтаем"), testBotID)
	msg, _ := inboundFromCommand(command(t, "group_chat", "просто болтаем"), testBotID)
	if kind != inboundText || msg.AddressedToBot {
		t.Fatalf("unmentioned group message must reach the engine unaddressed: %v %+v", kind, msg)
	}

	botMention := "11111111-1111-1111-1111-111111111111"
	other := "22222222-2222-2222-2222-222222222222"
	cmd := command(t, "group_chat",
		"@{mention:"+botMention+"} спроси @{mention:"+other+"} про релиз",
		mention(botMention, testBotID, "Multica"),
		mention(other, "33333333-3333-3333-3333-333333333333", "Анна"))
	msg, kind = inboundFromCommand(cmd, testBotID)
	if kind != inboundText || !msg.AddressedToBot {
		t.Fatalf("mentioned group message must be addressed: %+v", msg)
	}
	if msg.CommandText != "спроси @Анна про релиз" {
		t.Fatalf("mentions rendered as %q", msg.CommandText)
	}
}

func TestInboundReplyToBotIsAddressedAndQuoteIsContext(t *testing.T) {
	reply := map[string]any{"type": "reply", "data": map[string]any{
		"source_sync_id": "f1c2", "sender": testBotID, "body": "ответ бота",
	}}
	msg, _ := inboundFromCommand(command(t, "group_chat", "а подробнее?", reply), testBotID)
	if !msg.AddressedToBot || msg.HasSelectedContext {
		t.Fatalf("reply to bot: addressed, bot's own text is not selected context: %+v", msg)
	}

	botMention := "11111111-1111-1111-1111-111111111111"
	humanReply := map[string]any{"type": "reply", "data": map[string]any{
		"source_sync_id": "f1c3", "sender": "44444444-4444-4444-4444-444444444444", "body": "/issue старый текст",
	}}
	msg, _ = inboundFromCommand(command(t, "group_chat", "@{mention:"+botMention+"} оцени",
		humanReply, mention(botMention, testBotID, "Multica")), testBotID)
	if !msg.HasSelectedContext || !strings.Contains(msg.Text, "/issue старый текст") {
		t.Fatalf("quoted human message must be context: %q", msg.Text)
	}
	if msg.CommandText != "оцени" {
		t.Fatalf("command text must stay the member's own: %q", msg.CommandText)
	}
}

func TestInboundIgnoresSystemOwnAndChannel(t *testing.T) {
	sys := command(t, "chat", "system:chat_created")
	sys.Command.CommandType = "system"
	if _, kind := inboundFromCommand(sys, testBotID); kind != inboundIgnore {
		t.Fatal("system events are ignored")
	}
	own := command(t, "group_chat", "эхо")
	huid := testBotID
	own.From.UserHUID = &huid
	if _, kind := inboundFromCommand(own, testBotID); kind != inboundIgnore {
		t.Fatal("the bot's own messages are ignored")
	}
	if _, kind := inboundFromCommand(command(t, "channel", "пост"), testBotID); kind != inboundIgnore {
		t.Fatal("channel posts are ignored")
	}
	file := command(t, "chat", "")
	file.Attachments = []any{map[string]any{"type": "image"}}
	if _, kind := inboundFromCommand(file, testBotID); kind != inboundUnsupported {
		t.Fatal("a file without text is unsupported")
	}
}

func signJWT(t *testing.T, aud, secret string, exp time.Time) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"aud": []string{aud}, "iss": "cts.example.com", "exp": exp.Unix(), "nbf": time.Now().Add(-time.Minute).Unix(),
		"iat": time.Now().Unix(), "jti": "x",
	})
	s, err := tok.SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestVerifyRequestJWT(t *testing.T) {
	now := time.Now()
	good := signJWT(t, testBotID, testSecret, now.Add(time.Hour))
	if err := verifyRequestJWT("Bearer "+good, testBotID, testSecret, now); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	cases := map[string]string{
		"missing":      "",
		"wrong secret": "Bearer " + signJWT(t, testBotID, "other", now.Add(time.Hour)),
		"wrong bot":    "Bearer " + signJWT(t, "99999999-9999-9999-9999-999999999999", testSecret, now.Add(time.Hour)),
		"expired":      "Bearer " + signJWT(t, testBotID, testSecret, now.Add(-time.Hour)),
		"not bearer":   good,
	}
	for name, header := range cases {
		if err := verifyRequestJWT(header, testBotID, testSecret, now); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSignatureMatchesPybotx(t *testing.T) {
	// pybotx: base64.b16encode(hmac.new(secret, bot_id, sha256).digest())
	const want = "904E39D3BC549C71F4A4BDA66AFCDA6FC90D471A64889B45CC8D2288E56526AD"
	if got := signature("8dada2c8-67a6-4434-9dec-570d244e78ee", "secret"); got != want {
		t.Fatalf("signature = %q, want %q", got, want)
	}
}

func TestSplitBody(t *testing.T) {
	text := strings.Repeat("строка текста\n", 600) // ~8400 runes
	parts := splitBody(text, maxBodyRunes)
	if len(parts) < 3 {
		t.Fatalf("expected several parts, got %d", len(parts))
	}
	total := 0
	for _, p := range parts {
		if n := len([]rune(p)); n > maxBodyRunes {
			t.Fatalf("part of %d runes exceeds the limit", n)
		}
		if strings.HasPrefix(p, "\n") || strings.HasSuffix(p, "\n") {
			t.Fatalf("parts are trimmed at line breaks: %q…", p[:20])
		}
		total += strings.Count(p, "строка текста")
	}
	if total != 600 {
		t.Fatalf("lost lines: %d", total)
	}
	if got := splitBody("коротко", maxBodyRunes); len(got) != 1 || got[0] != "коротко" {
		t.Fatalf("short text = %v", got)
	}
}

// fakeCTS records notifications and mints tokens like a CTS.
type fakeCTS struct {
	mu     sync.Mutex
	sent   []directNotification
	auth   []string
	mints  int
	reject bool
}

func (f *fakeCTS) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/botx/bots/"+testBotID+"/token", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.mints++
		f.mu.Unlock()
		if f.reject || r.URL.Query().Get("signature") != signature(testBotID, testSecret) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"status":"error","reason":"bot_not_found"}`)
			return
		}
		_, _ = io.WriteString(w, `{"status":"ok","result":"bot-token"}`)
	})
	mux.HandleFunc("/api/v4/botx/notifications/direct", func(w http.ResponseWriter, r *http.Request) {
		var n directNotification
		_ = json.NewDecoder(r.Body).Decode(&n)
		f.mu.Lock()
		f.sent = append(f.sent, n)
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"status":"ok","result":{"sync_id":"s-1"}}`)
	})
	return mux
}

func TestBotxSendTextMintsAndPosts(t *testing.T) {
	cts := &fakeCTS{}
	srv := httptest.NewServer(cts.handler(t))
	defer srv.Close()
	api := newBotxAPI(credentials{BotID: testBotID, CTSURL: srv.URL, SecretKey: testSecret}, srv.Client(), newTokenCache())
	ids, err := api.sendText(context.Background(), testChat, "привет")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "s-1" {
		t.Fatalf("ids = %v", ids)
	}
	if _, err := api.sendText(context.Background(), testChat, "ещё"); err != nil {
		t.Fatal(err)
	}
	if cts.mints != 1 {
		t.Fatalf("token must be cached, minted %d times", cts.mints)
	}
	if cts.sent[0].GroupChatID != testChat || cts.sent[0].Notification.Body != "привет" || cts.auth[0] != "Bearer bot-token" {
		t.Fatalf("notification = %+v auth=%q", cts.sent[0], cts.auth[0])
	}

	bad := newBotxAPI(credentials{BotID: testBotID, CTSURL: srv.URL, SecretKey: "wrong"}, srv.Client(), newTokenCache())
	if _, err := bad.mintToken(context.Background()); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("wrong secret must be rejected, got %v", err)
	}
}

type fakeInstallations struct{ inst db.ChannelInstallation }

func (f fakeInstallations) GetChannelInstallationByAppID(_ context.Context, arg db.GetChannelInstallationByAppIDParams) (db.ChannelInstallation, error) {
	if arg.ChannelType != string(TypeExpress) || arg.AppID != testBotID {
		return db.ChannelInstallation{}, pgx.ErrNoRows
	}
	return f.inst, nil
}

func testInstallation(ctsURL string) db.ChannelInstallation {
	cfg, _ := json.Marshal(installConfig{
		AppID: testBotID, CTSURL: ctsURL,
		// nil Decrypter: stored bytes are plaintext (base64 of the secret).
		SecretKeyEncrypted: "c2VjcmV0LWtleQ==",
	})
	return db.ChannelInstallation{Status: "active", Config: cfg, ChannelType: string(TypeExpress)}
}

func TestWebhookCommandVerifiesAndHandsToEngine(t *testing.T) {
	var got []channel.InboundMessage
	w := NewWebhook(WebhookConfig{
		Queries: fakeInstallations{inst: testInstallation("https://cts.example.com")},
		Handler: func(_ context.Context, m channel.InboundMessage) error { got = append(got, m); return nil },
	})
	w.async = func(f func()) { f() }

	body, _ := json.Marshal(map[string]any{
		"sync_id": "a465f0f3-1354-491c-8f11-f400164295cb", "bot_id": testBotID, "proto_version": 4,
		"command":  map[string]any{"body": "привет", "command_type": "user"},
		"from":     map[string]any{"user_huid": testUser, "group_chat_id": testChat, "chat_type": "chat"},
		"entities": []any{},
	})

	// Unsigned: refused, engine untouched.
	rec := httptest.NewRecorder()
	w.Command(rec, httptest.NewRequest(http.MethodPost, "/api/express/command", strings.NewReader(string(body))))
	if rec.Code != http.StatusUnauthorized || len(got) != 0 {
		t.Fatalf("unsigned request: code %d, engine calls %d", rec.Code, len(got))
	}

	// Signed: 202 and one message for the engine.
	req := httptest.NewRequest(http.MethodPost, "/api/express/command", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+signJWT(t, testBotID, testSecret, time.Now().Add(time.Hour)))
	rec = httptest.NewRecorder()
	w.Command(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("signed request: code %d body %s", rec.Code, rec.Body)
	}
	if len(got) != 1 || got[0].Text != "привет" || !got[0].AddressedToBot {
		t.Fatalf("engine got %+v", got)
	}

	// Unknown bot: BotX "bot_disabled".
	other := strings.Replace(string(body), testBotID, "99999999-9999-9999-9999-999999999999", 1)
	rec = httptest.NewRecorder()
	w.Command(rec, httptest.NewRequest(http.MethodPost, "/api/express/command", strings.NewReader(other)))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "bot_disabled") {
		t.Fatalf("unknown bot: %d %s", rec.Code, rec.Body)
	}
}

func TestWebhookStatusListsCommands(t *testing.T) {
	w := NewWebhook(WebhookConfig{Queries: fakeInstallations{inst: testInstallation("https://cts.example.com")}})
	req := httptest.NewRequest(http.MethodGet, "/api/express/status?bot_id="+testBotID+"&user_huid="+testUser+"&chat_type=chat", nil)
	req.Header.Set("Authorization", "Bearer "+signJWT(t, testBotID, testSecret, time.Now().Add(time.Hour)))
	rec := httptest.NewRecorder()
	w.Status(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"/issue"`) || !strings.Contains(rec.Body.String(), `"enabled":true`) {
		t.Fatalf("status: %d %s", rec.Code, rec.Body)
	}
}

func TestNormalizeInstallInput(t *testing.T) {
	for in, want := range map[string]string{
		"ctslo.loodsen.ru":          "https://ctslo.loodsen.ru",
		"https://ctslo.loodsen.ru/": "https://ctslo.loodsen.ru",
		"http://10.0.0.5:8080":      "http://10.0.0.5:8080",
	} {
		if got, err := normalizeCTSURL(in); err != nil || got != want {
			t.Errorf("normalizeCTSURL(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "ftp://x", "https://cts/api"} {
		if _, err := normalizeCTSURL(bad); err == nil {
			t.Errorf("normalizeCTSURL(%q) accepted", bad)
		}
	}
	if got, err := normalizeBotID(" 8DADA2C8-67A6-4434-9DEC-570D244E78EE "); err != nil || got != testBotID {
		t.Errorf("normalizeBotID = %q, %v", got, err)
	}
	if _, err := normalizeBotID("not-a-uuid"); err == nil {
		t.Error("bad bot id accepted")
	}
}

func TestDeliveryChatIDPrefersBindingConfig(t *testing.T) {
	cfg, _ := json.Marshal(expressBindingConfig{ChatID: testChat})
	if got := deliveryChatID(db.ChannelTaskDelivery{ChannelChatID: "other", Config: cfg}); got != testChat {
		t.Fatalf("got %q", got)
	}
	if got := deliveryChatID(db.ChannelTaskDelivery{ChannelChatID: testChat}); got != testChat {
		t.Fatalf("fallback got %q", got)
	}
}
