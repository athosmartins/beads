package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

var undeferCmd = &cobra.Command{
	Use:   "undefer [id...]",
	Short: "Undefer one or more issues (restore to open)",
	Long: `Undefer issues to restore them to open status.

This brings issues back from the icebox so they can be worked on again.
Issues will appear in 'bd ready' if they have no blockers.

Examples:
  bd undefer bd-abc        # Undefer a single issue
  bd undefer bd-abc bd-def # Undefer multiple issues`,
	Args:          cobra.MinimumNArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("undefer")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		CheckReadonly("undefer")

		if usesProxiedServer() {
			return runUndeferProxiedServer(rootCtx, args)
		}

		ctx := rootCtx

		_, err := utils.ResolvePartialIDs(ctx, store, args)
		if err != nil {
			return HandleError("%v", err)
		}

		undeferredIssues := []*types.Issue{}

		if store == nil {
			return HandleErrorWithHint("database not initialized", diagHint())
		}

		for _, id := range args {
			fullID, err := utils.ResolvePartialID(ctx, store, id)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", id, err)
				continue
			}

			issue, err := store.GetIssue(ctx, fullID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting %s: %v\n", fullID, err)
				continue
			}

			// Gate on defer_until, not status alone (ga-bq3w5). The ready-work
			// query hides ANY issue with a future defer_until regardless of
			// status ("(defer_until IS NULL OR defer_until <= UTC_TIMESTAMP())"),
			// so a status=open issue can still carry a live defer_until — e.g.
			// `bd update <id> --status open --defer <date>` sets both explicitly
			// in one call, and an explicit --status wins over --defer's own
			// status=deferred default. Gating solely on status left such an
			// issue permanently invisible to `bd ready` with nothing connecting
			// the two — `bd show` does print the stray defer_until, but nothing
			// in the CLI says it's the reason the issue never appears in ready —
			// and no command able to undo it: this one refused with a
			// technically-true, practically-misleading "is not deferred
			// (status: open)" and never touched the timestamp actually doing
			// the hiding. Mirrors the same
			// gate GH#3233 already gave `bd update --defer=""` (see update.go):
			// only flip status to open when it was actually "deferred" — other
			// statuses (blocked, in_progress, closed, …) shouldn't be clobbered
			// just because a stray defer_until needs clearing.
			wasDeferred := issue.Status == types.StatusDeferred
			if !wasDeferred && issue.DeferUntil == nil {
				fmt.Fprintf(os.Stderr, "%s is not deferred (status: %s)\n", fullID, string(issue.Status))
				continue
			}

			updates := map[string]interface{}{
				"defer_until": nil,
			}
			if wasDeferred {
				updates["status"] = string(types.StatusOpen)
			}

			if err := store.UpdateIssue(ctx, fullID, updates, actor); err != nil {
				fmt.Fprintf(os.Stderr, "Error undeferring %s: %v\n", fullID, err)
				continue
			}

			if jsonOutput {
				issue, _ := store.GetIssue(ctx, fullID)
				if issue != nil {
					undeferredIssues = append(undeferredIssues, issue)
				}
			} else if wasDeferred {
				fmt.Printf("%s Undeferred %s (now open)\n", ui.RenderPass("*"), fullID)
			} else {
				fmt.Printf("%s Cleared stale defer_until on %s (status unchanged: %s)\n", ui.RenderPass("*"), fullID, string(issue.Status))
			}
		}

		if len(args) > 0 {
			commandDidWrite.Store(true)
		}

		if jsonOutput && len(undeferredIssues) > 0 {
			return outputJSON(undeferredIssues)
		}

		return nil
	},
}

func init() {
	undeferCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(undeferCmd)
}
