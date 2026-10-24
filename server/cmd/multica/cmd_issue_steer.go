package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

func init() { issueCmd.AddCommand(newIssueSteerCommand()) }

func newIssueSteerCommand() *cobra.Command {
	cmd := &cobra.Command{
		SilenceUsage: true,
		Use:          "steer <issue> [<run-id>]",
		Short:        "Send an additional message to a running turn (human credentials only)",
		Long: "Add a comment to one exact running turn without restarting it. Human credentials are required; agent tokens cannot steer, including on behalf of a person.\n\n" +
			"Omit run-id to select the issue's only running turn. Full UUIDs and short run ID prefixes from `issue runs` are accepted. " +
			"Use exactly one of --content, --content-file, or --content-stdin.\n\n" +
			"A client request UUID is generated and printed to stderr before sending. To repeat an uncertain submission safely, " +
			"pass that UUID with --client-request-id and the same explicit run-id. A repeated request returns the original comment. " +
			"Use retry to redeliver a failed comment without creating another one. --wait waits for delivery, not for the agent to finish its work.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error { return runIssueSteer(cmd, args, false) },
	}
	cmd.Flags().String("content", "", "Message content")
	cmd.Flags().String("content-file", "", "Read content from a UTF-8 file inside the working directory")
	cmd.Flags().Bool("content-stdin", false, "Read content from stdin")
	cmd.Flags().Bool("allow-external-file", false, "Allow --content-file outside the working directory")
	cmd.MarkFlagsMutuallyExclusive("content", "content-file", "content-stdin")
	cmd.Flags().String("client-request-id", "", "Request UUID to reuse after an uncertain submission (default: generated UUID)")
	addSteerReceiptFlags(cmd)
	retry := &cobra.Command{
		Use:   "retry <issue> <run-id> <comment-id>",
		Short: "Retry delivery of an existing additional message (human credentials only)",
		Args:  cobra.ExactArgs(3),
		RunE:  func(cmd *cobra.Command, args []string) error { return runIssueSteer(cmd, args, true) },
	}
	addSteerReceiptFlags(retry)
	cmd.AddCommand(retry)
	return cmd
}

func addSteerReceiptFlags(cmd *cobra.Command) {
	cmd.Flags().String("output", "json", "Output format: json or table")
	cmd.Flags().Bool("wait", false, "Wait until the message is delivered; print the last receipt on failure or timeout")
	cmd.Flags().Duration("timeout", time.Minute, "Maximum delivery wait (with --wait), e.g. 30s or 2m")
}

type steerReceipt struct {
	CommentID       string `json:"comment_id"`
	TaskID          string `json:"task_id"`
	Status          string `json:"status"`
	ClientRequestID string `json:"client_request_id,omitempty"`
	FailureReason   string `json:"failure_reason,omitempty"`
	DeliveredAt     string `json:"delivered_at,omitempty"`
}

// Both create and retry expose the single-receipt fields. Comment reads also
// carry all receipts, so select by task ID instead of assuming the first one.
type steerResponse struct {
	ID            string         `json:"id"`
	TaskID        string         `json:"supplement_task_id"`
	Status        string         `json:"supplement_status"`
	FailureReason string         `json:"supplement_failure_reason"`
	DeliveredAt   string         `json:"supplement_delivered_at"`
	Supplements   []steerReceipt `json:"supplements"`
}

func (s steerResponse) receiptFor(base steerReceipt) (steerReceipt, bool) {
	if len(s.Supplements) > 0 {
		for _, receipt := range s.Supplements {
			if receipt.TaskID == base.TaskID && receipt.Status != "" {
				receipt.CommentID, receipt.ClientRequestID = base.CommentID, base.ClientRequestID
				return receipt, true
			}
		}
		return base, false
	}
	if s.TaskID == base.TaskID && s.Status != "" {
		base.Status, base.FailureReason, base.DeliveredAt = s.Status, s.FailureReason, s.DeliveredAt
		return base, true
	}
	return base, false
}

func runIssueSteer(cmd *cobra.Command, args []string, retry bool) error {
	output, _ := cmd.Flags().GetString("output")
	if output != "json" && output != "table" {
		return fmt.Errorf("--output must be json or table")
	}
	timeout, _ := cmd.Flags().GetDuration("timeout")
	if timeout <= 0 {
		return fmt.Errorf("--timeout must be positive")
	}
	var content, requestID string
	if retry {
		if !uuidRegexp.MatchString(args[2]) {
			return fmt.Errorf("comment-id must be a full UUID")
		}
	} else {
		var err error
		var provided bool
		content, provided, err = resolveTextFlag(cmd, "content")
		if err != nil {
			return err
		}
		if !provided || strings.TrimSpace(content) == "" {
			return fmt.Errorf("provide non-empty --content, --content-file, or --content-stdin")
		}
		requestID, _ = cmd.Flags().GetString("client-request-id")
		if requestID == "" {
			requestID = uuid.NewString()
		}
		if !uuidRegexp.MatchString(requestID) {
			return fmt.Errorf("--client-request-id must be a full UUID")
		}
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	issue, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}
	runInput := ""
	if len(args) > 1 {
		runInput = args[1]
	}
	taskID, err := resolveSteerRun(ctx, client, issue.ID, runInput)
	if err != nil {
		return err
	}
	path := "/api/issues/" + url.PathEscape(issue.ID) + "/tasks/" + url.PathEscape(taskID) + "/supplements"
	var response steerResponse
	receipt := steerReceipt{TaskID: taskID, ClientRequestID: requestID}
	var body any = map[string]string{"content": content, "client_request_id": requestID}
	if retry {
		receipt.CommentID = args[2]
		path += "/" + url.PathEscape(receipt.CommentID) + "/retry"
		body = map[string]string{}
	} else {
		fmt.Fprintf(cmd.ErrOrStderr(), "Run %s; client_request_id=%s (reuse both to repeat this submission safely)\n", taskID, requestID)
	}
	if err := client.PostJSON(ctx, path, body, &response); err != nil {
		return steerAPIError(err)
	}
	if !retry {
		receipt.CommentID = response.ID
	}
	var ok bool
	receipt, ok = response.receiptFor(receipt)
	if !ok || receipt.CommentID == "" {
		return fmt.Errorf("server returned no delivery receipt for run %s; do not resend with a new client_request_id", taskID)
	}
	var waitErr error
	if wait, _ := cmd.Flags().GetBool("wait"); wait {
		// The API command budget covers submission only. Delivery has its own
		// deadline, so --timeout can exceed the ordinary HTTP request timeout.
		waitCtx, waitCancel := context.WithTimeout(cmd.Context(), timeout)
		defer waitCancel()
		receipt, waitErr = waitForSteer(waitCtx, client, issue.ID, receipt, time.Second)
	}
	if output == "json" {
		if err := cli.PrintJSON(cmd.OutOrStdout(), receipt); err != nil {
			return err
		}
	} else {
		cli.PrintTable(cmd.OutOrStdout(), []string{"COMMENT", "RUN", "STATUS", "FAILURE", "DELIVERED"}, [][]string{{receipt.CommentID, receipt.TaskID, receipt.Status, receipt.FailureReason, receipt.DeliveredAt}})
	}
	return waitErr
}

func resolveSteerRun(ctx context.Context, client *cli.APIClient, issueID, input string) (string, error) {
	if input != "" {
		ref, err := resolveTaskRunID(ctx, client, issueID, input)
		return ref.ID, err
	}
	var runs []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := client.GetJSON(ctx, "/api/issues/"+url.PathEscape(issueID)+"/task-runs?active=true", &runs); err != nil {
		return "", fmt.Errorf("list running turns: %w", err)
	}
	var running []string
	for _, run := range runs {
		if run.Status == "running" {
			running = append(running, run.ID)
		}
	}
	switch len(running) {
	case 0:
		return "", fmt.Errorf("no running turn for this issue")
	case 1:
		return running[0], nil
	default:
		return "", fmt.Errorf("multiple running turns; specify run-id:\n%s", strings.Join(running, "\n"))
	}
}

func steerAPIError(err error) error {
	switch cli.ServerErrorCode(err) {
	case "task_supplement_turn_ended":
		return cli.WithUserMessage("The run has already ended.", err)
	case "task_supplement_unsupported":
		return cli.WithUserMessage("The runtime does not accept additional messages; restart the run to provide new instructions.", err)
	case "task_supplement_not_retryable":
		return cli.WithUserMessage("This message cannot be retried. Read its delivery receipt and the run's current state.", err)
	default:
		return err
	}
}

func waitForSteer(ctx context.Context, client *cli.APIClient, issueID string, receipt steerReceipt, interval time.Duration) (steerReceipt, error) {
	params := url.Values{"thread": {receipt.CommentID}, "tail": {"0"}}
	path := "/api/issues/" + url.PathEscape(issueID) + "/comments?" + params.Encode()
	for {
		switch receipt.Status {
		case "delivered":
			return receipt, nil
		case "failed":
			return receipt, fmt.Errorf("additional message delivery failed: %s (comment %s, run %s)", receipt.FailureReason, receipt.CommentID, receipt.TaskID)
		}
		var comments []steerResponse
		if err := client.GetJSON(ctx, path, &comments); err != nil {
			if ctx.Err() != nil {
				return receipt, steerWaitError(ctx.Err(), receipt)
			}
			return receipt, fmt.Errorf("read delivery receipt: %w", err)
		}
		for _, comment := range comments {
			if comment.ID == receipt.CommentID {
				if updated, ok := comment.receiptFor(receipt); ok {
					receipt = updated
				}
				break
			}
		}
		if receipt.Status == "delivered" || receipt.Status == "failed" {
			continue
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return receipt, steerWaitError(ctx.Err(), receipt)
		case <-timer.C:
		}
	}
}

func steerWaitError(err error, receipt steerReceipt) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("timed out waiting for delivery of comment %s to run %s (last status: %s); the message may still be delivered: %w", receipt.CommentID, receipt.TaskID, receipt.Status, err)
	}
	return fmt.Errorf("waiting for delivery stopped: %w", err)
}
