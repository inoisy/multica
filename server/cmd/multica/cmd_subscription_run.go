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
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/daemon"
	"github.com/multica-ai/multica/server/internal/daemon/subscriptions"
)

// Manual runs: an instance admin starts claude or codex by hand on one
// account, on the host that holds its credential. The run is entered in the
// account's journal; the command's arguments never leave this machine.

var subscriptionRunCmd = &cobra.Command{
	Use:   "run <key> [-- <claude|codex arguments>]",
	Short: "Run claude or codex by hand on one account (instance admin, on the daemon host)",
	Long: `Runs the account's own CLI (claude, or codex for Codex accounts) with its credential,
on this host. Arguments after -- go to the CLI as they are; stdin, stdout, the
terminal and the exit code are the CLI's.

Only an instance admin signed in with 'multica login' may run it. The run is
recorded in the account's journal (who, which machine, which CLI, when, exit
code) without arguments. An account over its limit for agents is still allowed
for a person: you get a warning.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runSubscriptionRun,
}

var subscriptionRunsCmd = &cobra.Command{
	Use:   "runs <key>",
	Short: "Show the journal of manual runs on an account (instance admin)",
	Args:  exactArgs(1),
	RunE:  runSubscriptionRuns,
}

func init() {
	subscriptionCmd.AddCommand(subscriptionRunCmd, subscriptionRunsCmd)
	f := subscriptionRunsCmd.Flags()
	f.Int("limit", 20, "How many runs to show (1..200)")
	f.String("before", "", "Only runs started before this time (RFC 3339); the next page after a full one")
	f.String("output", "table", "Output format: table or json")
}

// manualRunRecord is one journal entry as the server returns it.
type manualRunRecord struct {
	ID               string  `json:"id"`
	UserName         string  `json:"user_name"`
	DaemonID         string  `json:"daemon_id"`
	Command          string  `json:"command"`
	StartedAt        string  `json:"started_at"`
	FinishedAt       *string `json:"finished_at"`
	ExitCode         *int    `json:"exit_code"`
	Unfinished       bool    `json:"unfinished"`
	AgentCapExceeded bool    `json:"agent_cap_exceeded"`
}

// manualRunError turns a refusal into a sentence. A 404 or 405 that is not the
// server's own JSON error comes from a server that has no journal yet.
func manualRunError(action string, err error) error {
	var httpErr *cli.HTTPError
	if errors.As(err, &httpErr) && (httpErr.StatusCode == http.StatusNotFound || httpErr.StatusCode == http.StatusMethodNotAllowed) {
		var payload struct {
			Error string `json:"error"`
		}
		if json.Unmarshal([]byte(httpErr.Body), &payload) != nil || strings.TrimSpace(payload.Error) == "" {
			return fmt.Errorf("%s: this server does not have the manual-run journal yet; update the server first", action)
		}
	}
	return subscriptionError(action, err)
}

// newManualRunClient is the API client for manual runs and their journal. The
// server accepts them only from an instance admin in person, so the request
// carries the admin's own login from 'multica login', never a daemon or task
// token.
func newManualRunClient(cmd *cobra.Command, command string) (*cli.APIClient, error) {
	if err := requireHumanLocalCommand(command); err != nil {
		return nil, err
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, err
	}
	if client.Token == "" {
		return nil, errors.New("not signed in: run 'multica login' as an instance admin")
	}
	if strings.HasPrefix(client.Token, "mat_") {
		return nil, errors.New("this needs your own login, not a task token: run 'multica login' and unset MULTICA_TOKEN")
	}
	client.AgentID, client.TaskID = "", ""
	return client, nil
}

func runSubscriptionRuns(cmd *cobra.Command, args []string) error {
	limit, _ := cmd.Flags().GetInt("limit")
	if limit < 1 || limit > 200 {
		return errors.New("--limit must be from 1 to 200")
	}
	query := url.Values{"limit": {strconv.Itoa(limit)}}
	if before, _ := cmd.Flags().GetString("before"); before != "" {
		query.Set("before", before)
	}
	client, err := newManualRunClient(cmd, "multica subscription runs")
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var resp struct {
		Key        string            `json:"key"`
		Runs       []manualRunRecord `json:"runs"`
		NextBefore string            `json:"next_before,omitempty"`
	}
	if err := client.GetJSON(ctx, subscriptionPath(args[0])+"/manual-runs?"+query.Encode(), &resp); err != nil {
		return manualRunError("manual runs", err)
	}
	if outputJSON(cmd) {
		return cli.PrintJSON(os.Stdout, resp)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "STARTED\tWHO\tMACHINE\tCLI\tEXIT\tFINISHED\tNOTE")
	for _, r := range resp.Runs {
		started := r.StartedAt
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", formatLocal(&started), r.UserName, r.DaemonID,
			r.Command, formatManualExit(r), formatLocal(r.FinishedAt), manualRunNote(r))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if resp.NextBefore != "" {
		fmt.Fprintf(os.Stderr, "More: multica subscription runs %s --before %s\n", args[0], resp.NextBefore)
	}
	return nil
}

func formatManualExit(r manualRunRecord) string {
	if r.ExitCode == nil {
		return "—"
	}
	return strconv.Itoa(*r.ExitCode)
}

func manualRunNote(r manualRunRecord) string {
	var notes []string
	if r.Unfinished {
		notes = append(notes, "not finished")
	}
	if r.AgentCapExceeded {
		notes = append(notes, "over the agent limit")
	}
	return strings.Join(notes, ", ")
}

// manualRunDeps are the parts of a manual run that touch the machine, so a
// test can replace them.
type manualRunDeps struct {
	client       *cli.APIClient
	daemonID     string
	requireVLESS string
	baseEnv      []string
	stdin        io.Reader
	stdout       io.Writer
	stderr       io.Writer
	lookPath     func(string) (string, error)
	probe        func(context.Context, subscriptions.Subscription) subscriptions.Report
}

// manualRunSignals are the signals that stop the child. At a terminal Ctrl-C
// already reaches the child through the foreground process group, and
// forwarding it again would turn claude's "cancel this turn" into a quit.
func manualRunSignals(stdinIsTerminal bool) []os.Signal {
	sigs := []os.Signal{syscall.SIGTERM, syscall.SIGHUP}
	if !stdinIsTerminal {
		sigs = append(sigs, os.Interrupt)
	}
	return sigs
}

func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// manualRunArgs returns the arguments meant for the CLI: everything after --.
func manualRunArgs(cmd *cobra.Command, args []string) (string, []string, error) {
	dash := cmd.ArgsLenAtDash()
	if dash < 0 && len(args) == 1 {
		return args[0], nil, nil
	}
	if dash != 1 {
		return "", nil, errors.New("pass the CLI's arguments after --, e.g. multica subscription run <key> -- --version")
	}
	return args[0], args[1:], nil
}

func runSubscriptionRun(cmd *cobra.Command, args []string) error {
	key, passthrough, err := manualRunArgs(cmd, args)
	if err != nil {
		return err
	}
	client, err := newManualRunClient(cmd, "multica subscription run")
	if err != nil {
		return err
	}
	daemonID, err := daemon.EnsureDaemonID(resolveProfile(cmd))
	if err != nil {
		return fmt.Errorf("daemon id of this host: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), manualRunSignals(stdinIsTerminal())...)
	code, err := executeManualRun(ctx, manualRunDeps{
		client: client, daemonID: daemonID, requireVLESS: strings.TrimSpace(os.Getenv("MULTICA_REQUIRE_VLESS")),
		baseEnv: os.Environ(), stdin: os.Stdin, stdout: os.Stdout, stderr: cmd.ErrOrStderr(),
		lookPath: exec.LookPath,
		probe:    subscriptions.NewSelector().ProbeNow,
	}, key, passthrough)
	stop()
	if err != nil {
		return err
	}
	if code != 0 {
		os.Exit(code)
	}
	return nil
}

// executeManualRun runs the account's CLI and keeps the journal. It returns the
// exit code for this process. The journal is opened before the CLI starts, so a
// server that cannot record the run stops it; closing is best effort.
func executeManualRun(ctx context.Context, d manualRunDeps, key string, args []string) (int, error) {
	var sub subscriptions.Subscription
	callCtx, cancel := cli.APIContext(ctx)
	err := d.client.GetJSON(callCtx, subscriptionPath(key), &sub)
	cancel()
	if err != nil {
		return 0, subscriptionError("subscription "+key, err)
	}
	command := sub.Family
	if command != "claude" && command != "codex" {
		return 0, fmt.Errorf("subscription %s: no CLI to run for family %q", key, sub.Family)
	}
	if sub.Disabled {
		fmt.Fprintf(d.stderr, "Warning: %s is switched off for agents; running by hand anyway.\n", key)
	}

	codexHome := ""
	if command == "codex" {
		if codexHome, err = os.MkdirTemp("", "multica-manual-codex-"); err != nil {
			return 0, err
		}
		defer os.RemoveAll(codexHome)
	}
	launch, err := subscriptions.Prepare(sub, codexHome)
	if err != nil {
		return 0, err
	}
	if codexHome != "" {
		launch.Env["CODEX_HOME"] = codexHome
	}
	if launch.NeedsVLESS {
		if err := subscriptions.RequireVLESS(ctx, d.requireVLESS); err != nil {
			return 0, err
		}
	}
	path, err := d.lookPath(command)
	if err != nil {
		return 0, fmt.Errorf("%s was not found on this host: %w", command, err)
	}
	filtered := launch.FilterArgs(args)
	if len(filtered) != len(args) {
		fmt.Fprintf(d.stderr, "Note: %s sets the model and tool list itself; --model and --exclude-tools were not passed.\n", key)
	}

	var opened manualRunRecord
	callCtx, cancel = cli.APIContext(ctx)
	err = d.client.PostJSON(callCtx, subscriptionPath(key)+"/manual-runs", map[string]any{"daemon_id": d.daemonID, "command": command}, &opened)
	cancel()
	if err != nil {
		return 0, manualRunError("record the run", err)
	}
	if opened.AgentCapExceeded {
		fmt.Fprintf(d.stderr, "Warning: %s is over its limit for agents. Agents will not use it; your run is allowed and recorded.\n", key)
	}

	code, runErr := subscriptions.RunManual(ctx, subscriptions.ManualCommand{
		Path: path, Args: filtered, Env: launch.Environ(d.baseEnv),
		Stdin: d.stdin, Stdout: d.stdout, Stderr: d.stderr,
	})
	cancelled := ctx.Err() != nil

	// The run is over; the calls below must not be cut short by the same
	// Ctrl-C that stopped it.
	tail := context.WithoutCancel(ctx)
	d.closeManualRun(tail, key, opened.ID, code)
	if runErr == nil {
		d.reportQuota(tail, sub)
	}
	if runErr != nil {
		return 0, fmt.Errorf("start %s: %w", command, runErr)
	}
	switch {
	case code >= 0:
		return code, nil
	case cancelled:
		return 130, nil
	default:
		return 1, nil
	}
}

func (d manualRunDeps) closeManualRun(ctx context.Context, key, runID string, code int) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := d.client.PostJSON(ctx, subscriptionPath(key)+"/manual-runs/"+url.PathEscape(runID)+"/finish", map[string]any{"exit_code": code}, nil)
	if err != nil {
		fmt.Fprintf(d.stderr, "Warning: could not close the journal entry (it stays \"not finished\"): %v\n", err)
	}
}

// reportQuota measures the account again and records the result through the
// same endpoint the usage snapshots use, so the card shows what the run spent.
func (d manualRunDeps) reportQuota(ctx context.Context, sub subscriptions.Subscription) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rep := d.probe(ctx, sub)
	if rep.Err != nil || len(rep.State.Windows) == 0 {
		return
	}
	snapshots := make([]map[string]any, 0, len(rep.State.Windows))
	for _, w := range rep.State.Windows {
		s := map[string]any{"window": w.Key, "used_percent": w.Used, "limit_reached": w.Limit, "source": "manual-run"}
		if w.ResetAt != nil {
			s["resets_at"] = w.ResetAt.UTC().Format(time.RFC3339)
		}
		snapshots = append(snapshots, s)
	}
	if err := d.client.PostJSON(ctx, subscriptionPath(sub.Key)+"/usage", map[string]any{"snapshots": snapshots}, nil); err != nil {
		fmt.Fprintf(d.stderr, "Warning: could not update the quota reading: %v\n", err)
	}
}
