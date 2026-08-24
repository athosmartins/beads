package dolt

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// failOnSecondEventInsertTx wraps a real *sql.Tx and fails the second
// "INSERT INTO events" statement with context.Canceled. In CreateIssueInTx's
// statement order the first such insert is always the issue-created event and
// the second is the first label's "Added label" event — so this deterministically
// reproduces a label-event write failing mid-transaction (ga-xcq1ph) without
// racing a wall-clock timeout against a real Dolt contention window.
type failOnSecondEventInsertTx struct {
	*sql.Tx
	eventInsertCount int
	injected         bool
}

func (f *failOnSecondEventInsertTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.Contains(query, "INSERT INTO events") {
		f.eventInsertCount++
		if f.eventInsertCount == 2 {
			f.injected = true
			return nil, context.Canceled
		}
	}
	return f.Tx.ExecContext(ctx, query, args...)
}

// TestCreateIssueRollsBackFullyOnLabelEventFailure is a regression test for
// ga-xcq1ph: a `bd create` with multiple labels, observed under real Dolt
// write contention, printed an error naming a generated issue ID ("failed to
// record label event ... context canceled / transaction has already been
// committed or rolled back") and that ID never persisted (confirmed by a
// later `bd show`). The open question was whether that is silent partial
// persistence (a real bug) or a fully-rolled-back transaction whose error
// text merely mentions the doomed ID (confusing, but safe).
//
// This test injects the exact failure shape at the exact point PersistLabels
// hits it (a label's "Added label" event insert returning context.Canceled)
// against a real Dolt transaction, then rolls back exactly as
// DoltStore.withWriteTx does on any callback error — the wrapper the
// production `bd create` path (cmd/bd/create.go -> writeOps -> ops.Create ->
// issueOperations.Create -> runIssueOperationTx -> withRetryTx -> withWriteTx)
// actually runs CreateIssuesInTxWithResult/CreateIssueInTxWithResult under —
// and checks whether the issue or its first label leaked through anyway.
// withRetryTx never retries this failure: context.Canceled matches none of
// isDoltAutocommitRollbackError/isSerializationError/isRetryableError, so it
// falls to backoff.Permanent and surfaces to the CLI as a single failed
// attempt, exactly as reported.
func TestCreateIssueRollsBackFullyOnLabelEventFailure(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	const issueID = "create-rollback-label-event"
	issue := &types.Issue{
		ID:        issueID,
		Title:     "phantom create under label-event failure",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Labels:    []string{"label-one", "label-two"},
	}

	realTx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = realTx.Rollback() }() // no-op if already rolled back below

	faultyTx := &failOnSecondEventInsertTx{Tx: realTx}

	// CreateOnly matches issueops.ExecuteCreate's own BatchCreateOptions —
	// the facade cmd/bd/create.go actually calls (via writeOps/ops.Create) on
	// the production `bd create` path — so this test exercises the same
	// insert-vs-upsert branch in InsertIssueIfNew that production hits.
	bc, err := issueops.NewBatchContext(ctx, faultyTx, storage.BatchCreateOptions{CreateOnly: true, SkipPrefixValidation: true})
	if err != nil {
		t.Fatalf("new batch context: %v", err)
	}

	_, createErr := issueops.CreateIssueInTxWithResult(ctx, faultyTx, bc, issue, "tester")
	if createErr == nil {
		t.Fatal("expected CreateIssueInTxWithResult to fail when a label event insert is canceled, got nil error")
	}
	if !strings.Contains(createErr.Error(), "failed to record label event") {
		t.Fatalf("error = %v, want it to mention the failed label event (matches the production error text in issueops/create.go)", createErr)
	}
	if !errors.Is(createErr, context.Canceled) {
		t.Fatalf("error = %v, want it to wrap context.Canceled", createErr)
	}
	if !faultyTx.injected {
		t.Fatal("fault injector never fired (eventInsertCount never reached 2) — test setup is broken and proves nothing")
	}

	// This is exactly what DoltStore.withWriteTx does on any callback error:
	// unconditionally roll back (internal/storage/dolt/store.go).
	if err := realTx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	// The claim under test: does the issue, or its first label (inserted
	// successfully before the second event insert failed), leak through
	// despite the rollback?
	var issueCount int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", issueID).Scan(&issueCount); err != nil {
		t.Fatalf("post-rollback issue count: %v", err)
	}
	if issueCount != 0 {
		t.Fatalf("issue %s persisted despite mid-transaction label-event failure and rollback: found %d rows (this would confirm ga-xcq1ph as a real data-loss bug)", issueID, issueCount)
	}
	var labelCount int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM labels WHERE issue_id = ?", issueID).Scan(&labelCount); err != nil {
		t.Fatalf("post-rollback label count: %v", err)
	}
	if labelCount != 0 {
		t.Fatalf("label rows for %s persisted despite rollback: found %d rows", issueID, labelCount)
	}
	var eventCount int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id = ?", issueID).Scan(&eventCount); err != nil {
		t.Fatalf("post-rollback event count: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("event rows for %s persisted despite rollback: found %d rows", issueID, eventCount)
	}
}
