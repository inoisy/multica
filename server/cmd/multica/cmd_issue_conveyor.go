package main

import "github.com/spf13/cobra"

func init() { issueCmd.AddCommand(newIssueConveyorCommand()) }

// newIssueConveyorCommand keeps the hidden `multica issue conveyor` group for
// one release: legacy routes are gone, and only the old name of the
// acceptance command remains, for daemons and scripts that still call it.
func newIssueConveyorCommand() *cobra.Command {
	conv := &cobra.Command{
		Use:    "conveyor",
		Hidden: true,
		Short:  "Legacy issue routes (removed; use project Workflows)",
	}
	conv.AddCommand(newIssueConveyorAcceptCommand())
	return conv
}
