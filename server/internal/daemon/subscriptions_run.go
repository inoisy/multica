package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/subscriptions"
)

// subscriptionSelector is created on first use so tests that build a bare
// Daemon get one without wiring.
func (d *Daemon) subscriptionSelector() *subscriptions.Selector {
	d.subsOnce.Do(func() {
		if d.subs == nil {
			s := subscriptions.NewSelector()
			if d.cfg.SubscriptionProbeInterval > 0 {
				s.Interval = d.cfg.SubscriptionProbeInterval
			}
			if d.cfg.SubscriptionClaudeProbeInterval > 0 {
				s.ClaudeInterval = d.cfg.SubscriptionClaudeProbeInterval
			}
			d.subs = s
		}
	})
	return d.subs
}

// subscriptionRun is the subscription a run spends and how to launch it.
type subscriptionRun struct {
	sub    subscriptions.Subscription
	launch subscriptions.Launch
}

// prepareSubscriptionRun chooses the subscription for a claimed task whose
// runtime spends a pool, checks the VLESS exit for Claude/Codex accounts and
// points the run at the credential. An error fails the run before the CLI
// starts; a "usage limit" text makes the server treat it as a quota failure
// and move the run to the next model.
func (d *Daemon) prepareSubscriptionRun(ctx context.Context, task Task, codexHome string, log *slog.Logger) (*subscriptionRun, error) {
	pool := task.SubscriptionPool
	if pool == nil || len(pool.Subscriptions) == 0 {
		return nil, nil
	}
	choice, ok := d.subscriptionSelector().Choose(*pool, true)
	if !ok {
		return nil, nil
	}
	log.Info("subscription chosen",
		"subscription", choice.Subscription.Key,
		"spend_order", pool.SpendOrder,
		"candidates", describeMarks(choice.Marks),
		"blocked", choice.Blocked != "")
	if choice.Blocked != "" {
		return nil, &subscriptions.BlockedError{Text: choice.Blocked, Until: choice.BlockedUntil}
	}
	launch, err := subscriptions.Prepare(choice.Subscription, codexHome)
	if err != nil {
		return nil, err
	}
	if launch.NeedsVLESS {
		if err := subscriptions.RequireVLESS(ctx, d.cfg.RequireVLESS); err != nil {
			return nil, err
		}
	}
	return &subscriptionRun{sub: choice.Subscription, launch: launch}, nil
}

func describeMarks(marks []subscriptions.Mark) string {
	parts := make([]string, 0, len(marks))
	for _, m := range marks {
		load := "?"
		if m.State.Known() {
			var ws []string
			for _, w := range m.State.Windows {
				flag := ""
				if w.Limit {
					flag = "!"
				}
				ws = append(ws, fmt.Sprintf("%s %.0f%s", w.Key, w.Used, flag))
			}
			load = strings.Join(ws, "/")
		}
		parts = append(parts, fmt.Sprintf("%s[%s %s]", m.Sub.Key, m.State.Credential, load))
	}
	return strings.Join(parts, " ")
}

// subscriptionProbeTick is how often the probe loop wakes; each subscription is
// measured on its own interval (Claude less often).
const subscriptionProbeTick = time.Minute

// subscriptionProbeLoop measures every subscription the daemon's workspaces
// spend and reports the load and the credential state to the server.
func (d *Daemon) subscriptionProbeLoop(ctx context.Context) {
	ticker := time.NewTicker(subscriptionProbeTick)
	defer ticker.Stop()
	for {
		d.probeSubscriptionsOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *Daemon) workspaceIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.workspaces))
	for id := range d.workspaces {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (d *Daemon) probeSubscriptionsOnce(ctx context.Context) {
	sel := d.subscriptionSelector()
	for _, ws := range d.workspaceIDs() {
		subs, err := d.client.GetWorkspaceSubscriptions(ctx, ws)
		if err != nil || len(subs) == 0 {
			continue
		}
		var reports []subscriptions.Report
		for _, sub := range subs {
			if sel.Due(sub) {
				reports = append(reports, sel.ProbeNow(ctx, sub))
			}
		}
		d.sendSubscriptionReports(ctx, ws, reports)
	}
}

// probeAfterRun measures the subscription a run just spent.
func (d *Daemon) probeAfterRun(workspaceID string, sub subscriptions.Subscription) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rep := d.subscriptionSelector().ProbeNow(ctx, sub)
	d.sendSubscriptionReports(ctx, workspaceID, []subscriptions.Report{rep})
}

func (d *Daemon) sendSubscriptionReports(ctx context.Context, workspaceID string, reports []subscriptions.Report) {
	if len(reports) == 0 {
		return
	}
	body := map[string]any{"daemon_id": d.cfg.DaemonID, "device_name": d.cfg.DeviceName}
	items := make([]map[string]any, 0, len(reports))
	for _, r := range reports {
		item := map[string]any{"key": r.Key, "credential_state": string(r.Credential)}
		if r.Err != nil {
			item["error"] = r.Err.Error()
		}
		windows := make([]map[string]any, 0, len(r.State.Windows))
		for _, w := range r.State.Windows {
			win := map[string]any{"window": w.Key, "used_percent": w.Used, "limit_reached": w.Limit}
			if w.ResetAt != nil {
				win["resets_at"] = w.ResetAt.UTC().Format(time.RFC3339)
			}
			windows = append(windows, win)
		}
		item["windows"] = windows
		items = append(items, item)
	}
	body["reports"] = items
	if err := d.client.ReportSubscriptionUsage(ctx, workspaceID, body); err != nil {
		d.logger.Warn("subscription usage report failed", "workspace_id", workspaceID, "error", err)
	}
}

// subscriptionLogins guards against running two sign-ins for one key at once.
var subscriptionLogins sync.Map

// handleSubscriptionLogin signs a subscription in on this host: a Codex
// device login, or a pasted Claude token / API key written atomically to the
// credential file. The token is never logged.
func (d *Daemon) handleSubscriptionLogin(ctx context.Context, runtimeID string, p *PendingSubscriptionLogin) {
	if _, busy := subscriptionLogins.LoadOrStore(p.Key, true); busy {
		d.reportSubscriptionLogin(ctx, runtimeID, p.ID, "failed", nil, nil, "вход в эту подписку уже идёт")
		return
	}
	defer subscriptionLogins.Delete(p.Key)
	log := d.logger.With("subscription", p.Key, "login_id", p.ID, "auth", p.Auth)
	sub := subscriptions.Subscription{Key: p.Key, Provider: p.Provider, Auth: p.Auth, CredentialPath: p.CredentialPath}
	var probe subscriptions.Report
	switch p.Auth {
	case "codex_auth":
		codexPath := "codex"
		if entry, ok := d.agents()["codex"]; ok && entry.Path != "" {
			codexPath = entry.Path
		}
		if err := subscriptions.RequireVLESS(ctx, d.cfg.RequireVLESS); err != nil {
			d.reportSubscriptionLogin(ctx, runtimeID, p.ID, "failed", nil, nil, err.Error())
			return
		}
		loginCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		defer cancel()
		err := subscriptions.CodexDeviceLogin(loginCtx, codexPath, p.CredentialPath, func(pr subscriptions.LoginProgress) {
			url, code := pr.URL, pr.Code
			d.reportSubscriptionLogin(ctx, runtimeID, p.ID, "waiting", &url, &code, "откройте ссылку и введите код")
		})
		if err != nil {
			log.Warn("subscription sign-in failed", "error", err)
			d.reportSubscriptionLogin(ctx, runtimeID, p.ID, "failed", nil, nil, err.Error())
			return
		}
		probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		probe = d.subscriptionSelector().ProbeNow(probeCtx, sub)
		cancel()
	case "claude_oauth", "api_key":
		value, err := subscriptions.ValidateSecret(p.Auth, p.Token)
		if err != nil {
			d.reportSubscriptionLogin(ctx, runtimeID, p.ID, "failed", nil, nil, err.Error())
			return
		}
		rep, err := d.replaceCredential(ctx, sub, value)
		if err != nil {
			log.Warn("subscription credential not replaced", "error", err)
			d.reportSubscriptionLogin(ctx, runtimeID, p.ID, "failed", nil, nil, err.Error())
			return
		}
		probe = rep
	default:
		d.reportSubscriptionLogin(ctx, runtimeID, p.ID, "failed", nil, nil, "этот тип учётки не входит через multica")
		return
	}
	log.Info("subscription signed in", "credential", probe.Credential, "load_known", probe.State.Known())
	d.sendLoginReport(ctx, probe)
	d.reportSubscriptionLogin(ctx, runtimeID, p.ID, "done", nil, nil, "вошли")
}

// replaceCredential checks a pasted token or key before it replaces the
// credential file, the way gh and docker refuse a bad token at login: the
// value goes to a sibling file, the provider is probed with it, and only a
// value the provider did not refuse is renamed over the credential. A probe
// that could not reach the provider does not block the sign-in.
func (d *Daemon) replaceCredential(ctx context.Context, sub subscriptions.Subscription, value string) (subscriptions.Report, error) {
	target := subscriptions.ExpandPath(sub.CredentialPath)
	candidate := target + ".login-check"
	if err := subscriptions.WriteCredential(candidate, []byte(value+"\n")); err != nil {
		return subscriptions.Report{}, fmt.Errorf("не удалось записать учётку: %w", err)
	}
	defer os.Remove(candidate)
	sel := d.subscriptionSelector()
	check := sub
	check.CredentialPath = candidate
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	st, err := sel.Prober.Probe(probeCtx, check)
	cancel()
	if st.Credential == subscriptions.CredentialRevoked {
		return subscriptions.Report{}, errors.New("провайдер не принял токен, прежняя учётка не тронута")
	}
	if err := os.Rename(candidate, target); err != nil {
		return subscriptions.Report{}, fmt.Errorf("не удалось записать учётку: %w", err)
	}
	return sel.Record(sub, st, err), nil
}

// sendLoginReport puts a sign-in's probe on the subscription card at once
// instead of waiting for the probe loop, which measures Claude every 30
// minutes. One workspace that spends the subscription is enough: the card
// is the instance's.
func (d *Daemon) sendLoginReport(ctx context.Context, rep subscriptions.Report) {
	for _, ws := range d.workspaceIDs() {
		subs, err := d.client.GetWorkspaceSubscriptions(ctx, ws)
		if err != nil {
			continue
		}
		for _, s := range subs {
			if s.Key == rep.Key {
				d.sendSubscriptionReports(ctx, ws, []subscriptions.Report{rep})
				return
			}
		}
	}
}

func (d *Daemon) reportSubscriptionLogin(ctx context.Context, runtimeID, loginID, state string, url, code *string, message string) {
	body := map[string]any{"state": state, "message": message}
	if url != nil {
		body["url"] = *url
	}
	if code != nil {
		body["code"] = *code
	}
	if err := d.client.ReportSubscriptionLogin(ctx, runtimeID, loginID, body); err != nil {
		d.logger.Warn("subscription sign-in report failed", "login_id", loginID, "error", err)
	}
}

// linkCodexAgentsMD links the host's shared agent rules into a task
// CODEX_HOME as AGENTS.md, unless the home already has one. The host
// wrappers used to do this; with subscriptions launched by the daemon it
// belongs here.
func linkCodexAgentsMD(codexHome, rules string, log *slog.Logger) {
	if rules == "" {
		return
	}
	if fi, err := os.Stat(rules); err != nil || fi.Size() == 0 {
		return
	}
	target := filepath.Join(codexHome, "AGENTS.md")
	if _, err := os.Lstat(target); err == nil {
		return
	}
	if err := os.Symlink(rules, target); err != nil {
		log.Warn("link shared AGENTS.md into CODEX_HOME failed", "error", err)
	}
}
