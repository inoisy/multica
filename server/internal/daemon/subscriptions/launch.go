package subscriptions

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

func userHome(name string) string {
	u, err := user.Lookup(name)
	if err != nil {
		return ""
	}
	return u.HomeDir
}

// envAllowed are the settings.env names an API-key subscription may set:
// endpoint, models, context window, timeouts. Keys and tokens only ever come
// from the credential file.
var envAllowed = regexp.MustCompile(`^(ANTHROPIC_(BASE_URL|MODEL|DEFAULT_[A-Z]+_MODEL|SMALL_FAST_MODEL)|CLAUDE_CODE_[A-Z_]+|API_TIMEOUT_MS|DISABLE_[A-Z_]+|MAX_THINKING_TOKENS)$`)

// envReset are cleared for every Claude subscription run so a daemon-wide or
// agent-level value cannot route the run onto another account.
var envReset = []string{
	"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"CLAUDE_CODE_AUTO_COMPACT_WINDOW", "CLAUDE_CODE_MAX_CONTEXT_TOKENS",
}

// Launch is how a run spends the chosen subscription.
type Launch struct {
	// Env is layered over the run's environment; an empty value clears a variable.
	Env map[string]string
	// DropModel: the provider picks the model (API-key subscriptions set it
	// through settings.env), so the agent's --model must not be passed.
	DropModel bool
	// NeedsVLESS: Claude and Codex accounts are reached only through the VLESS exit.
	NeedsVLESS bool
}

// FilterArgs is what a CLI run behind this launch may be given: for an
// API-key provider the model comes from settings.env, so --model and
// Claude-foreign flags are dropped.
func (l Launch) FilterArgs(args []string) []string {
	if l.DropModel {
		return ClaudeProviderArgs(args)
	}
	return args
}

// Environ layers the launch over base ("NAME=value" entries). An empty value
// removes the variable, so a key exported in the caller's shell cannot route
// the run onto another account.
func (l Launch) Environ(base []string) []string {
	out := make([]string, 0, len(base)+len(l.Env))
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if _, set := l.Env[name]; !set {
			out = append(out, kv)
		}
	}
	names := make([]string, 0, len(l.Env))
	for name, v := range l.Env {
		if v != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		out = append(out, name+"="+l.Env[name])
	}
	return out
}

// Prepare builds the launch for sub. codexHome is the task's CODEX_HOME, whose
// auth.json is pointed at the subscription's credential file.
func Prepare(sub Subscription, codexHome string) (Launch, error) {
	l := Launch{Env: map[string]string{}}
	switch sub.Family {
	case "codex":
		l.NeedsVLESS = true
		cred := ExpandPath(sub.CredentialPath)
		if fi, err := os.Stat(cred); err != nil || fi.Size() == 0 {
			return l, fmt.Errorf("нет учётки подписки %s в %s", sub.Key, sub.CredentialPath)
		}
		if codexHome == "" {
			return l, errors.New("codex run without a task CODEX_HOME")
		}
		if err := relink(filepath.Join(codexHome, "auth.json"), cred); err != nil {
			return l, fmt.Errorf("point task CODEX_HOME at subscription %s: %w", sub.Key, err)
		}
		return l, nil
	}
	for _, k := range envReset {
		l.Env[k] = ""
	}
	switch sub.Auth {
	case "claude_login":
		l.NeedsVLESS = true
	case "claude_oauth":
		l.NeedsVLESS = true
		token, st := readCredential(sub.CredentialPath)
		token = strings.Join(strings.Fields(token), "")
		if st != CredentialPresent || !strings.HasPrefix(token, "sk-ant-oat") {
			return l, fmt.Errorf("нет токена подписки %s в %s", sub.Key, sub.CredentialPath)
		}
		l.Env["CLAUDE_CODE_OAUTH_TOKEN"] = token
	case "api_key":
		key, st := readCredential(sub.CredentialPath)
		if st != CredentialPresent {
			return l, fmt.Errorf("нет ключа подписки %s в %s", sub.Key, sub.CredentialPath)
		}
		for k, v := range sub.Settings.Env {
			if envAllowed.MatchString(k) {
				l.Env[k] = v
			}
		}
		l.Env["ANTHROPIC_AUTH_TOKEN"] = key
		l.DropModel = true
	default:
		return l, fmt.Errorf("subscription %s: unsupported auth %q", sub.Key, sub.Auth)
	}
	return l, nil
}

// relink atomically points link at target, replacing a symlink or file.
func relink(link, target string) error {
	tmp := link + ".subscription"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, link)
}

func claudeToolName(name string) string {
	switch name {
	case "agent":
		return "Agent"
	case "ask_user_question":
		return "AskUserQuestion"
	}
	return name
}

// ClaudeProviderArgs rewrites an agent's custom args for Claude Code behind an
// API key: --model is dropped (the provider sets the model) and a foreign
// --exclude-tools becomes --disallowedTools with Claude tool names (a stray
// --exclude-tools aborted runs at start, 05.10.2026).
func ClaudeProviderArgs(args []string) []string {
	out := make([]string, 0, len(args))
	var excluded []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--model":
			i++
		case strings.HasPrefix(a, "--model="):
		case a == "--exclude-tools":
			for i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				excluded = append(excluded, claudeToolName(args[i]))
			}
		case strings.HasPrefix(a, "--exclude-tools="):
			for _, n := range strings.Split(strings.TrimPrefix(a, "--exclude-tools="), ",") {
				if n != "" {
					excluded = append(excluded, claudeToolName(n))
				}
			}
		default:
			out = append(out, a)
		}
	}
	if len(excluded) == 0 {
		return out
	}
	for i, a := range out {
		if a == "--disallowedTools" {
			merged := append([]string{}, out[:i+1]...)
			merged = append(merged, excluded...)
			return append(merged, out[i+1:]...)
		}
	}
	return append(append(out, "--disallowedTools"), excluded...)
}

// RequireVLESS runs the host's VLESS check (MULTICA_REQUIRE_VLESS) before a
// Claude or Codex account is used; an empty path skips the check.
func RequireVLESS(ctx context.Context, path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	out, err := exec.CommandContext(ctx, path).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("VLESS-выход не работает, запуск модели запрещён: %s", msg)
	}
	return nil
}

// WriteCredential replaces a credential file atomically: a temp file in the
// same directory, mode 600, fsync, rename. The directory is created 700.
func WriteCredential(path string, data []byte) error {
	target := ExpandPath(path)
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".subscription-login-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

// ValidateSecret checks a pasted token or key before it replaces a credential.
func ValidateSecret(auth, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 4096 || strings.ContainsAny(value, " \t\r\n") {
		return "", errors.New("пустое или неверное значение")
	}
	if auth == "claude_oauth" && !strings.HasPrefix(value, "sk-ant-oat") {
		return "", errors.New("это не токен claude setup-token (ожидается sk-ant-oat…)")
	}
	return value, nil
}

var (
	loginURLRe  = regexp.MustCompile(`https://\S+`)
	loginCodeRe = regexp.MustCompile(`\b[A-Z0-9]{4,5}-[A-Z0-9]{4,5}\b`)
)

// LoginProgress is reported while a device login runs.
type LoginProgress struct {
	URL  string
	Code string
}

// CodexDeviceLogin runs `codex login --device-auth` in a scratch CODEX_HOME,
// reports the verification URL and code once both are printed, and on success
// replaces the subscription's auth.json atomically. The working credential is
// never touched by an abandoned or failed login.
func CodexDeviceLogin(ctx context.Context, codexPath, credentialPath string, progress func(LoginProgress)) error {
	tmp, err := os.MkdirTemp("", "subscription-login-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	cmd := exec.CommandContext(ctx, codexPath, "login", "--device-auth")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+tmp)
	cmd.WaitDelay = 5 * time.Second
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		pw.Close()
		return fmt.Errorf("codex не запустился: %w", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var seen LoginProgress
		reported := false
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			line := sc.Text()
			if seen.URL == "" {
				if m := loginURLRe.FindString(line); m != "" {
					seen.URL = strings.TrimRight(m, ".,)")
				}
			}
			if seen.Code == "" {
				seen.Code = loginCodeRe.FindString(line)
			}
			if !reported && seen.URL != "" && seen.Code != "" {
				reported = true
				progress(seen)
			}
		}
	}()
	waitErr := cmd.Wait()
	pw.Close()
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if waitErr != nil {
		return fmt.Errorf("вход не завершён: %w", waitErr)
	}
	data, err := os.ReadFile(filepath.Join(tmp, "auth.json"))
	if err != nil || len(data) == 0 {
		return errors.New("вход не завершён: codex не записал auth.json")
	}
	return WriteCredential(credentialPath, data)
}
