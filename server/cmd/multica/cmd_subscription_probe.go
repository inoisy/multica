package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var subscriptionProbeCmd = &cobra.Command{
	Use:   "probe <key>",
	Short: "Re-check the provider's limit now: the host measures the account and a free answer lifts its limit blocks",
	Long: `Asks the daemon on the host that spends the subscription to measure it right away and
store a fresh snapshot. If the provider answers without a limit, the limit blocks of this subscription
are lifted and the waiting runs start; otherwise the block stays and the new reset time is printed.
Needs workspace owner or admin rights, like 'runtime unavailable recheck'.`,
	Args: exactArgs(1),
	RunE: runSubscriptionProbe,
}

// probePollInterval paces the probe status polling.
var probePollInterval = time.Second

func init() {
	subscriptionCmd.AddCommand(subscriptionProbeCmd)
	subscriptionProbeCmd.Flags().Duration("wait", 90*time.Second, "How long to wait for the host's answer")
	subscriptionProbeCmd.Flags().String("output", "table", "Output format: table or json")
}

func runSubscriptionProbe(cmd *cobra.Command, args []string) error {
	wait, _ := cmd.Flags().GetDuration("wait")
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	return probeSubscription(cmd, client, args[0], wait)
}

type subscriptionProbeResult struct {
	ID       string  `json:"id"`
	Key      string  `json:"key"`
	State    string  `json:"state"`
	Message  string  `json:"message"`
	Lifted   int     `json:"lifted"`
	ResetsAt *string `json:"resets_at"`
}

func probeSubscription(cmd *cobra.Command, client *cli.APIClient, key string, wait time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	var started struct {
		Probe subscriptionProbeResult `json:"probe"`
	}
	if err := client.PostJSON(ctx, subscriptionPath(key)+"/probe", map[string]any{}, &started); err != nil {
		return subscriptionError("probe", err)
	}
	for {
		var cur struct {
			Probe subscriptionProbeResult `json:"probe"`
		}
		if err := client.GetJSON(ctx, subscriptionPath(key)+"/probe/"+started.Probe.ID, &cur); err != nil {
			return subscriptionError("probe", err)
		}
		p := cur.Probe
		switch p.State {
		case "done", "limited":
			return printSubscriptionProbe(cmd, key, p)
		case "failed", "expired":
			return fmt.Errorf("probe %s: %s (%s)", key, p.State, p.Message)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("probe %s: no answer after %s; check `multica subscription usage %s`", key, wait, key)
		case <-time.After(probePollInterval):
		}
	}
}

func printSubscriptionProbe(cmd *cobra.Command, key string, p subscriptionProbeResult) error {
	if format, _ := cmd.Flags().GetString("output"); format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(p)
	}
	if p.State == "done" {
		fmt.Fprintf(os.Stdout, "%s is free: lifted %d limit blocks\n", key, p.Lifted)
		return nil
	}
	if p.ResetsAt != nil {
		fmt.Fprintf(os.Stdout, "%s is still at its limit until %s\n", key, *p.ResetsAt)
	} else {
		fmt.Fprintf(os.Stdout, "%s is still at its limit\n", key)
	}
	return nil
}
