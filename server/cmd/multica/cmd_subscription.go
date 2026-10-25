package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// Subscriptions record provider accounts; credentials remain on daemon hosts.
// Pools and workspace grants are mutated only by the server's pool service.

var subscriptionCmd = &cobra.Command{
	Use:   "subscription",
	Short: "Manage instance subscriptions (Claude, Codex, API keys)",
}

var subscriptionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List subscriptions with window usage, caps and the runtimes that spend them",
	Args:  exactArgs(0),
	RunE:  runSubscriptionList,
}

var subscriptionAddCmd = &cobra.Command{
	Use:   "add <provider>",
	Short: "Add a subscription (provider: claude, codex, zai, minimax, loodsen-ai, openrouter)",
	Args:  exactArgs(1),
	RunE:  runSubscriptionAdd,
}

var subscriptionLoginCmd = &cobra.Command{
	Use:   "login <key>",
	Short: "Sign a subscription in again: Codex prints a link and code, Claude and API keys take the token from stdin",
	Args:  exactArgs(1),
	RunE:  runSubscriptionLogin,
}

var subscriptionUpdateCmd = &cobra.Command{
	Use:   "update <key>",
	Short: "Change a subscription's agent caps, title or disable window",
	Args:  exactArgs(1),
	RunE:  runSubscriptionUpdate,
}

var subscriptionUsageCmd = &cobra.Command{
	Use:   "usage <key>",
	Short: "Show a subscription's usage snapshots",
	Args:  exactArgs(1),
	RunE:  runSubscriptionUsage,
}

var subscriptionRmCmd = &cobra.Command{
	Use:   "rm <key>",
	Short: "Remove a subscription (it is removed from every runtime first)",
	Args:  exactArgs(1),
	RunE:  runSubscriptionRm,
}

var runtimePoolCmd = &cobra.Command{
	Use:   "pool",
	Short: "Compatibility alias for pool; show/set accept a runtime name",
}

var runtimePoolCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Alias for pool create",
	Args:  exactArgs(1),
	RunE:  runRuntimePoolCreate,
}

var runtimePoolSetCmd = &cobra.Command{
	Use:   "set <runtime>",
	Short: "Add, remove or reorder a runtime's subscriptions, or change its spend order",
	Args:  exactArgs(1),
	RunE:  runRuntimePoolSet,
}

var runtimePoolShowCmd = &cobra.Command{
	Use:   "show <runtime>",
	Short: "Show a runtime's subscriptions, spend order and usage",
	Args:  exactArgs(1),
	RunE:  runRuntimePoolShow,
}

func init() {
	subscriptionCmd.AddCommand(subscriptionListCmd, subscriptionAddCmd, subscriptionLoginCmd,
		subscriptionUpdateCmd, subscriptionUsageCmd, subscriptionRmCmd)
	runtimeCmd.AddCommand(runtimePoolCmd)
	runtimePoolCmd.AddCommand(runtimePoolCreateCmd, runtimePoolSetCmd, runtimePoolShowCmd)
	aliases := newPoolCommand()
	for _, c := range append([]*cobra.Command(nil), aliases.Commands()...) {
		if c.Name() == "list" || c.Name() == "grant" {
			runtimePoolCmd.AddCommand(c)
		}
	}
	runtimePoolSetCmd.Flags().Int("revision", 0, "Expected pool revision (default: fetch current)")

	subscriptionListCmd.Flags().String("output", "table", "Output format: table or json")

	f := subscriptionAddCmd.Flags()
	f.String("title", "", "Human-readable title")
	f.String("key", "", "Stable key (derived from the title when omitted)")
	f.String("owner-note", "", "Whose subscription this is")
	f.String("auth", "", "claude_oauth, claude_login, codex_auth or api_key (default by provider)")
	f.String("credential-path", "", "Credential file on the daemon host (default ~/.config/<key>/...)")
	f.StringSlice("windows", nil, "Quota windows, e.g. 5h,week")
	f.StringArray("cap", nil, "Agent cap per window, e.g. --cap 5h=70 (repeatable)")
	f.Int("reserve", 0, "Deprecated: normalized by the server into agent caps")
	_ = f.MarkHidden("reserve")
	f.String("account-group", "", "Account shared with other subscriptions (one usage row)")
	f.String("settings-json", "", "Non-secret settings as a JSON object (API keys: {\"env\": {...}})")
	f.Bool("token-stdin", false, "Read the token (claude setup-token) or API key from stdin and sign in right away")
	f.Bool("login", false, "Codex: start the device sign-in right after adding")
	f.String("output", "json", "Output format: table or json")

	u := subscriptionUpdateCmd.Flags()
	u.String("title", "", "New title")
	u.String("owner-note", "", "New owner note")
	u.String("credential-path", "", "New credential file on the daemon host")
	u.StringSlice("windows", nil, "Quota windows, e.g. 5h,week")
	u.StringArray("cap", nil, "Agent cap per window, e.g. --cap 5h=70 (repeatable; other windows keep theirs)")
	u.Int("reserve", 0, "Deprecated: normalized by the server into agent caps")
	_ = u.MarkHidden("reserve")
	u.String("account-group", "", "Account shared with other subscriptions")
	u.String("settings-json", "", "Replace settings with this JSON object")
	u.String("disable-until", "", "Keep agents off this subscription until this time (RFC 3339, e.g. 2026-10-21T12:00Z)")
	u.Bool("enable", false, "Clear --disable-until")
	u.String("output", "json", "Output format: table or json")

	subscriptionLoginCmd.Flags().Bool("token-stdin", false, "Read the token or API key from stdin (never pass it as an argument)")
	subscriptionLoginCmd.Flags().Duration("wait", 15*time.Minute, "How long to wait for the sign-in to finish")
	subscriptionUsageCmd.Flags().String("since", "1d", "How far back: a duration like 7d or 12h, or an RFC 3339 time")
	subscriptionUsageCmd.Flags().String("output", "table", "Output format: table or json")

	p := runtimePoolCreateCmd.Flags()
	p.StringSlice("subs", nil, "Subscription keys in spend order, e.g. claude-2,claude-1")
	p.String("order", "first", "Spend order: first (first usable) or balanced (least-used week)")
	p.String("type", "", "claude or codex (default: the family of the first subscription)")
	p.String("description", "", "Optional description")
	p.Bool("all-workspaces", false, "Create the runtime in every workspace, including future ones")
	p.String("output", "json", "Output format: table or json")

	s := runtimePoolSetCmd.Flags()
	s.String("add", "", "Subscription key to add (at the end unless --first)")
	s.Bool("first", false, "With --add: put the subscription first")
	s.String("remove", "", "Subscription key to remove")
	s.String("order", "", "Spend order: first or balanced")
	s.String("output", "table", "Output format: table or json")

	runtimePoolShowCmd.Flags().String("output", "table", "Output format: table or json")
}

const subscriptionsPath = "/api/subscriptions"

// subscriptionError keeps the server's own sentence for refusals and
// validation errors: "only instance admins can change subscriptions" tells the
// user what to do, the generic 403 text does not.
func subscriptionError(action string, err error) error {
	var httpErr *cli.HTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusConflict:
			var payload struct {
				Error string `json:"error"`
			}
			if json.Unmarshal([]byte(httpErr.Body), &payload) == nil && strings.TrimSpace(payload.Error) != "" {
				return fmt.Errorf("%s: %s", action, payload.Error)
			}
		}
	}
	return fmt.Errorf("%s: %w", action, err)
}

func subscriptionPath(key string) string { return subscriptionsPath + "/" + url.PathEscape(key) }

// parseCaps turns ["5h=70", "week=90"] into {"5h": 70, "week": 90}.
func parseCaps(values []string) (map[string]int, error) {
	caps := map[string]int{}
	for _, v := range values {
		k, n, ok := strings.Cut(v, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid --cap %q: want window=percent, e.g. 5h=70", v)
		}
		pct, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(n), "%"))
		if err != nil || pct < 0 || pct > 100 {
			return nil, fmt.Errorf("invalid --cap %q: percent must be 0..100", v)
		}
		caps[k] = pct
	}
	return caps, nil
}

func parseSettingsJSON(raw string) (map[string]any, error) {
	settings := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return nil, fmt.Errorf("invalid --settings-json: %w", err)
	}
	return settings, nil
}

func runSubscriptionList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var resp struct {
		Subscriptions []map[string]any `json:"subscriptions"`
		CanManage     bool             `json:"can_manage"`
	}
	if err := client.GetJSON(ctx, subscriptionsPath, &resp); err != nil {
		return subscriptionError("list subscriptions", err)
	}
	if output, _ := cmd.Flags().GetString("output"); output == "json" {
		return cli.PrintJSON(os.Stdout, resp.Subscriptions)
	}
	printSubscriptionTable(resp.Subscriptions)
	return nil
}

func printSubscriptionTable(subs []map[string]any) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "KEY\tPROVIDER\tTITLE\tUSAGE\tCAPS\tRUNTIMES")
	for _, s := range subs {
		title := str(s["title"])
		if s["disabled"] == true {
			title += " (disabled until " + str(s["disabled_until"]) + ")"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", str(s["key"]), str(s["provider"]), title,
			formatUsage(s["usage"]), formatCaps(s["effective_caps"]), formatUsedBy(s["used_by"]))
	}
	_ = w.Flush()
}

func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func formatUsage(v any) string {
	items, _ := v.([]any)
	parts := []string{}
	for _, it := range items {
		m, _ := it.(map[string]any)
		pct, _ := m["used_percent"].(float64)
		part := fmt.Sprintf("%s %d%%", str(m["window"]), int(pct+0.5))
		if m["limit_reached"] == true {
			part += "!"
		}
		parts = append(parts, part)
	}
	sort.Strings(parts)
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " · ")
}

func numberValue(v any) float64 {
	switch n := v.(type) {
	case int:
		return float64(n)
	case float64:
		return n
	}
	return 100
}

func formatCaps(v any) string {
	m, _ := v.(map[string]any)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s ≤%v%% (people: %v%%)", k, m[k], 100-numberValue(m[k])))
	}
	if len(parts) == 0 {
		return "100% (people: 0%)"
	}
	return strings.Join(parts, " ")
}

func formatUsedBy(v any) string {
	items, _ := v.([]any)
	names := []string{}
	seen := map[string]bool{}
	for _, it := range items {
		m, _ := it.(map[string]any)
		name := fmt.Sprintf("%s#%v", str(m["display_name"]), m["position"])
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "—"
	}
	return strings.Join(names, ", ")
}

func runSubscriptionAdd(cmd *cobra.Command, args []string) error {
	tokenStdin, _ := cmd.Flags().GetBool("token-stdin")
	startLogin, _ := cmd.Flags().GetBool("login")
	var token string
	if tokenStdin {
		// Read before creating anything, so a bad paste leaves no half-made
		// subscription behind.
		t, err := readSecretFromStdin()
		if err != nil {
			return err
		}
		token = t
	}
	body := map[string]any{"provider": strings.TrimSpace(args[0])}
	for flag, field := range map[string]string{
		"title": "title", "key": "key", "owner-note": "owner_note", "auth": "auth",
		"credential-path": "credential_path", "account-group": "account_group",
	} {
		if cmd.Flags().Changed(flag) {
			v, _ := cmd.Flags().GetString(flag)
			body[field] = v
		}
	}
	if cmd.Flags().Changed("windows") {
		v, _ := cmd.Flags().GetStringSlice("windows")
		body["windows"] = v
	}
	if cmd.Flags().Changed("cap") {
		v, _ := cmd.Flags().GetStringArray("cap")
		caps, err := parseCaps(v)
		if err != nil {
			return err
		}
		body["caps"] = caps
	}
	if cmd.Flags().Changed("reserve") {
		v, _ := cmd.Flags().GetInt("reserve")
		if v < 0 || v > 100 {
			return errors.New("--reserve must be between 0 and 100")
		}
		fmt.Fprintln(cmd.ErrOrStderr(), "Warning: --reserve is deprecated; the server normalizes it into --cap=min(cap,100-reserve). Use --cap; people retain 100-cap percent.")
		body["reserve"] = v
	}
	if cmd.Flags().Changed("settings-json") {
		v, _ := cmd.Flags().GetString("settings-json")
		settings, err := parseSettingsJSON(v)
		if err != nil {
			return err
		}
		body["settings"] = settings
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var sub map[string]any
	if err := client.PostJSON(ctx, subscriptionsPath, body, &sub); err != nil {
		return subscriptionError("add subscription", err)
	}
	if output, _ := cmd.Flags().GetString("output"); output == "table" {
		printSubscriptionTable([]map[string]any{sub})
		fmt.Fprintf(os.Stderr, "Credential file on the daemon host: %s\n", str(sub["credential_path"]))
	} else if err := cli.PrintJSON(os.Stdout, sub); err != nil {
		return err
	}
	if token != "" || (startLogin && str(sub["auth"]) == "codex_auth") {
		return signIn(cmd, client, str(sub["key"]), token, 15*time.Minute)
	}
	if str(sub["auth"]) == "codex_auth" {
		fmt.Fprintf(os.Stderr, "Sign in: multica subscription login %s\n", str(sub["key"]))
	}
	return nil
}

// readSecretFromStdin reads one token or key from stdin. Tokens are never
// taken as arguments: those end up in shell history and process listings.
func readSecretFromStdin() (string, error) {
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 8192))
	if err != nil {
		return "", fmt.Errorf("read token from stdin: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", errors.New("stdin is empty: pipe the token, e.g. `claude setup-token | multica subscription login <key> --token-stdin`")
	}
	return token, nil
}

func runSubscriptionLogin(cmd *cobra.Command, args []string) error {
	var token string
	if tokenStdin, _ := cmd.Flags().GetBool("token-stdin"); tokenStdin {
		t, err := readSecretFromStdin()
		if err != nil {
			return err
		}
		token = t
	}
	wait, _ := cmd.Flags().GetDuration("wait")
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	return signIn(cmd, client, args[0], token, wait)
}

// loginPollInterval paces the sign-in status polling.
var loginPollInterval = 2 * time.Second

// signIn starts a sign-in and follows it: the daemon hosting the
// subscription writes the credential file; a Codex login prints the link and
// code to open in a browser.
func signIn(cmd *cobra.Command, client *cli.APIClient, key, token string, wait time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	body := map[string]any{}
	if token != "" {
		body["token"] = token
	}
	var started struct {
		Login map[string]any `json:"login"`
	}
	if err := client.PostJSON(ctx, subscriptionPath(key)+"/login", body, &started); err != nil {
		return subscriptionError("sign in", err)
	}
	shown := false
	for {
		var cur struct {
			Login map[string]any `json:"login"`
		}
		if err := client.GetJSON(ctx, subscriptionPath(key)+"/login", &cur); err != nil {
			return subscriptionError("sign in", err)
		}
		l := cur.Login
		if l != nil && str(l["id"]) == str(started.Login["id"]) {
			if !shown && str(l["url"]) != "" && str(l["code"]) != "" {
				fmt.Fprintf(os.Stdout, "Open %s and enter the code %s\n", str(l["url"]), str(l["code"]))
				shown = true
			}
			switch str(l["state"]) {
			case "done":
				fmt.Fprintf(os.Stdout, "Signed in: %s\n", key)
				return nil
			case "failed", "cancelled", "expired":
				return fmt.Errorf("sign in %s: %s (%s)", key, str(l["state"]), str(l["message"]))
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("sign in %s: still waiting after %s; check `multica subscription list`", key, wait)
		case <-time.After(loginPollInterval):
		}
	}
}

func runSubscriptionUpdate(cmd *cobra.Command, args []string) error {
	body := map[string]any{}
	for flag, field := range map[string]string{
		"title": "title", "owner-note": "owner_note", "credential-path": "credential_path",
		"account-group": "account_group", "disable-until": "disabled_until",
	} {
		if cmd.Flags().Changed(flag) {
			v, _ := cmd.Flags().GetString(flag)
			body[field] = v
		}
	}
	if enable, _ := cmd.Flags().GetBool("enable"); enable {
		if cmd.Flags().Changed("disable-until") {
			return errors.New("--enable and --disable-until are mutually exclusive")
		}
		body["disabled_until"] = ""
	}
	if cmd.Flags().Changed("windows") {
		v, _ := cmd.Flags().GetStringSlice("windows")
		body["windows"] = v
	}
	if cmd.Flags().Changed("cap") {
		v, _ := cmd.Flags().GetStringArray("cap")
		caps, err := parseCaps(v)
		if err != nil {
			return err
		}
		body["caps"] = caps
	}
	if cmd.Flags().Changed("reserve") {
		v, _ := cmd.Flags().GetInt("reserve")
		if v < 0 || v > 100 {
			return errors.New("--reserve must be between 0 and 100")
		}
		fmt.Fprintln(cmd.ErrOrStderr(), "Warning: --reserve is deprecated; the server normalizes it into --cap=min(cap,100-reserve). Use --cap; people retain 100-cap percent.")
		body["reserve"] = v
	}
	if cmd.Flags().Changed("settings-json") {
		v, _ := cmd.Flags().GetString("settings-json")
		settings, err := parseSettingsJSON(v)
		if err != nil {
			return err
		}
		body["settings"] = settings
	}
	if len(body) == 0 {
		return errors.New("nothing to update: pass --cap, --disable-until, --enable, --title, --owner-note, --credential-path, --windows, --account-group or --settings-json")
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var sub map[string]any
	if err := client.PatchJSON(ctx, subscriptionPath(args[0]), body, &sub); err != nil {
		return subscriptionError("update subscription", err)
	}
	if output, _ := cmd.Flags().GetString("output"); output == "table" {
		printSubscriptionTable([]map[string]any{sub})
		return nil
	}
	return cli.PrintJSON(os.Stdout, sub)
}

func runSubscriptionUsage(cmd *cobra.Command, args []string) error {
	since, _ := cmd.Flags().GetString("since")
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var resp struct {
		Key   string           `json:"key"`
		Since string           `json:"since"`
		Usage []map[string]any `json:"usage"`
	}
	if err := client.GetJSON(ctx, subscriptionPath(args[0])+"/usage?since="+url.QueryEscape(since), &resp); err != nil {
		return subscriptionError("subscription usage", err)
	}
	if output, _ := cmd.Flags().GetString("output"); output == "json" {
		return cli.PrintJSON(os.Stdout, resp.Usage)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "OBSERVED\tWINDOW\tUSED\tRESETS\tSOURCE")
	for _, u := range resp.Usage {
		pct, _ := u["used_percent"].(float64)
		used := fmt.Sprintf("%.0f%%", pct)
		if u["limit_reached"] == true {
			used += " (limit)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", str(u["observed_at"]), str(u["window"]), used, str(u["resets_at"]), str(u["source"]))
	}
	return w.Flush()
}

func runSubscriptionRm(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var resp struct {
		Deleted  string   `json:"deleted"`
		Detached []string `json:"detached_runtime_profile_ids"`
	}
	if err := client.DeleteJSONResponse(ctx, subscriptionPath(args[0]), &resp); err != nil {
		return subscriptionError("remove subscription", err)
	}
	fmt.Fprintf(os.Stdout, "Removed %s (detached from %d runtime(s))\n", args[0], len(resp.Detached))
	return nil
}

// ---------------------------------------------------------------------------
// runtime pool
// ---------------------------------------------------------------------------

// resolveRuntimeProfile finds a workspace runtime profile by id or by display
// name (case-insensitive), so `pool set "Claude · подписки"` works.
func resolveRuntimeProfile(ctx context.Context, client *cli.APIClient, workspaceID, ref string) (map[string]any, error) {
	var resp struct {
		RuntimeProfiles []map[string]any `json:"runtime_profiles"`
	}
	if err := client.GetJSON(ctx, runtimeProfilesPath(workspaceID), &resp); err != nil {
		return nil, fmt.Errorf("list runtime profiles: %w", err)
	}
	ref = strings.TrimSpace(ref)
	var byName []map[string]any
	for _, p := range resp.RuntimeProfiles {
		if str(p["id"]) == ref {
			return p, nil
		}
		if strings.EqualFold(str(p["display_name"]), ref) {
			byName = append(byName, p)
		}
	}
	switch len(byName) {
	case 1:
		return byName[0], nil
	case 0:
		return nil, fmt.Errorf("runtime %q not found in this workspace", ref)
	default:
		return nil, fmt.Errorf("runtime name %q is ambiguous; pass the profile id", ref)
	}
}

func runRuntimePoolCreate(cmd *cobra.Command, args []string) error { return runPoolCreate(cmd, args) }

// nextPoolKeys applies --add/--first/--remove to the current ordered list.
func nextPoolKeys(current []string, add string, first bool, remove string) ([]string, error) {
	out := make([]string, 0, len(current)+1)
	for _, k := range current {
		if k != remove && k != add {
			out = append(out, k)
		}
	}
	if remove != "" && len(out) == len(current) && add == "" {
		return nil, fmt.Errorf("subscription %s is not on this runtime", remove)
	}
	if add != "" {
		if first {
			out = append([]string{add}, out...)
		} else {
			out = append(out, add)
		}
	}
	return out, nil
}

func legacyPoolRef(cmd *cobra.Command, ref string) (string, error) {
	client, ctx, cancel, err := modelsClient(cmd)
	if err != nil {
		return "", err
	}
	defer cancel()
	ws, err := requireWorkspaceID(cmd)
	if err != nil {
		return "", err
	}
	profile, err := resolveRuntimeProfile(ctx, client, ws, ref)
	if err != nil {
		return "", err
	}
	id := str(profile["pool_id"])
	if id == "" {
		return "", errors.New("this runtime has no account pool")
	}
	return id, nil
}

func runRuntimePoolSet(cmd *cobra.Command, args []string) error {
	id, err := legacyPoolRef(cmd, args[0])
	if err != nil {
		return err
	}
	return runPoolSet(cmd, []string{id})
}

func runRuntimePoolShow(cmd *cobra.Command, args []string) error {
	id, err := legacyPoolRef(cmd, args[0])
	if err != nil {
		return err
	}
	return runPoolShow(cmd, []string{id})
}
