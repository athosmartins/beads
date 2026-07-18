package issueops

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/types"
)

func deferredParentProbeRegex(issueTable string) string {
	return `SELECT 1 FROM ` + issueTable + `\s+WHERE defer_until IS NOT NULL\s+AND defer_until > UTC_TIMESTAMP\(\)\s+LIMIT 1`
}

func deferredChildrenQueryRegex(depTable, issueTable string) string {
	targetCol := "depends_on_issue_id"
	if issueTable == "wisps" {
		targetCol = "depends_on_wisp_id"
	}
	return `SELECT dep\.issue_id\s+FROM ` + depTable + ` dep\s+JOIN ` + issueTable + ` parent ON parent\.id = dep\.` + targetCol + `\s+WHERE dep\.type = 'parent-child'\s+AND parent\.defer_until IS NOT NULL\s+AND parent\.defer_until > UTC_TIMESTAMP\(\)`
}

func beginMockTx(t *testing.T) (*sql.DB, sqlmock.Sqlmock, *sql.Tx) {
	t.Helper()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })

	return db, mock, tx
}

func TestBuildSQLInClause(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		ids              []string
		wantPlaceholders string
		wantArgs         []interface{}
	}{
		{
			name:             "single ID",
			ids:              []string{"42"},
			wantPlaceholders: "?",
			wantArgs:         []interface{}{"42"},
		},
		{
			name:             "multiple IDs",
			ids:              []string{"1", "2", "3"},
			wantPlaceholders: "?,?,?",
			wantArgs:         []interface{}{"1", "2", "3"},
		},
		{
			name:             "empty slice",
			ids:              []string{},
			wantPlaceholders: "",
			wantArgs:         []interface{}{},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotPlaceholders, gotArgs := buildSQLInClause(tt.ids)

			if gotPlaceholders != tt.wantPlaceholders {
				t.Errorf("placeholders = %q, want %q", gotPlaceholders, tt.wantPlaceholders)
			}

			if len(gotArgs) != len(tt.wantArgs) {
				t.Fatalf("args length = %d, want %d", len(gotArgs), len(tt.wantArgs))
			}

			for i := range gotArgs {
				if gotArgs[i] != tt.wantArgs[i] {
					t.Errorf("args[%d] = %v, want %v", i, gotArgs[i], tt.wantArgs[i])
				}
			}
		})
	}
}

func TestGetReadyWorkInTx_PropagatesDeferredParentChildError(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	childErr := errors.New("dolt transient dependency read failure")
	mock.ExpectQuery(deferredParentProbeRegex("issues")).WillReturnError(childErr)

	_, err := GetReadyWorkInTx(
		context.Background(),
		tx,
		types.WorkFilter{},
	)
	if err == nil {
		t.Fatal("expected deferred parent child error")
	}
	if !errors.Is(err, childErr) {
		t.Fatalf("expected wrapped deferred parent child error, got %v", err)
	}
	if !strings.Contains(err.Error(), "compute deferred parent children") {
		t.Fatalf("expected deferred parent child context, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// ga-7r884: LabelRegex can't be pushed into whereSQL (bd does not use SQL
// REGEXP functions), so GetReadyWorkInTx applies it in Go after fetching.
// buildReadyWorkPredicates must suppress the SQL LIMIT whenever LabelRegex is
// set — otherwise the LIMIT would cap the pre-regex candidate set and
// GetReadyWorkInTx's post-filter could silently drop real matches beyond the
// first filter.Limit unfiltered rows (the same "wrong result, no error"
// shape ga-hqchm hit with LabelPattern, just via a different mechanism).
// IncludeDeferred:true and ParentID:nil keep this a pure predicate-building
// call with no SQL round-trips, so no sqlmock expectations are needed beyond
// the transaction sqlmock.New() itself always requires.
func TestBuildReadyWorkPredicates_SuppressesLimitSQLWhenLabelRegexSet(t *testing.T) {
	t.Parallel()

	_, _, tx := beginMockTx(t)

	withRegex, err := buildReadyWorkPredicates(context.Background(), tx, types.WorkFilter{
		IncludeDeferred: true,
		Limit:           10,
		LabelRegex:      "tech-.*",
	}, IssuesFilterTables)
	if err != nil {
		t.Fatalf("buildReadyWorkPredicates (LabelRegex set): %v", err)
	}
	if withRegex.limitSQL != "" {
		t.Errorf("limitSQL = %q, want empty when LabelRegex is set (regex filter runs post-fetch in Go)", withRegex.limitSQL)
	}

	withoutRegex, err := buildReadyWorkPredicates(context.Background(), tx, types.WorkFilter{
		IncludeDeferred: true,
		Limit:           10,
	}, IssuesFilterTables)
	if err != nil {
		t.Fatalf("buildReadyWorkPredicates (no LabelRegex): %v", err)
	}
	if want := "LIMIT 10"; withoutRegex.limitSQL != want {
		t.Errorf("limitSQL = %q, want %q when LabelRegex is unset (existing SQL-pushdown behavior preserved)", withoutRegex.limitSQL, want)
	}
}

func TestLoadStatusByIDInTxPrefersWispOnCollision(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery("SELECT id, status FROM issues").
		WithArgs("dup-id").
		WillReturnRows(sqlmock.NewRows([]string{"id", "status"}).AddRow("dup-id", types.StatusOpen))
	mock.ExpectQuery("SELECT id, status FROM wisps").
		WithArgs("dup-id").
		WillReturnRows(sqlmock.NewRows([]string{"id", "status"}).AddRow("dup-id", types.StatusClosed))

	got, err := loadStatusByIDInTx(context.Background(), tx, []string{"dup-id"})
	if err != nil {
		t.Fatalf("loadStatusByIDInTx error = %v, want no error on cross-table dup", err)
	}
	if got["dup-id"] != types.StatusClosed {
		t.Errorf("status = %v, want %v (wisp canonical preferred over issues)", got["dup-id"], types.StatusClosed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestMergeReadyWispsPrefersWispOnCollision(t *testing.T) {
	t.Parallel()

	issuesCopy := &types.Issue{ID: "dup-id", Status: types.StatusOpen, Title: "issues copy"}
	wispCopy := &types.Issue{ID: "dup-id", Status: types.StatusClosed, Title: "wisp canonical"}

	got := mergeReadyWisps(
		[]*types.Issue{issuesCopy},
		[]*types.Issue{wispCopy},
		types.WorkFilter{},
	)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (deduped)", len(got))
	}
	if got[0].Title != "wisp canonical" {
		t.Errorf("title = %q, want %q (wisp preferred over issues copy)", got[0].Title, "wisp canonical")
	}
}

func TestGetChildrenOfDeferredParentsInTx_ReturnsChildrenFromBothDependencyTables(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(deferredParentProbeRegex("issues")).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(deferredChildrenQueryRegex("dependencies", "issues")).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id"}).AddRow("child-from-dependencies-issues"))
	mock.ExpectQuery(deferredChildrenQueryRegex("dependencies", "wisps")).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id"}).AddRow("child-from-dependencies-wisps"))
	mock.ExpectQuery(deferredChildrenQueryRegex("wisp_dependencies", "issues")).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id"}).AddRow("child-from-wisp-dependencies-issues"))
	mock.ExpectQuery(deferredChildrenQueryRegex("wisp_dependencies", "wisps")).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id"}).AddRow("child-from-wisp-dependencies-wisps"))

	got, err := getChildrenOfDeferredParentsInTx(context.Background(), tx)
	if err != nil {
		t.Fatalf("getChildrenOfDeferredParentsInTx: %v", err)
	}
	want := []string{
		"child-from-dependencies-issues",
		"child-from-dependencies-wisps",
		"child-from-wisp-dependencies-issues",
		"child-from-wisp-dependencies-wisps",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("children = %v, want %v", got, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestGetChildrenOfDeferredParentsInTx_NoDeferredParentsExitsAfterProbe(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(deferredParentProbeRegex("issues")).
		WillReturnRows(sqlmock.NewRows([]string{"1"}))
	mock.ExpectQuery(deferredParentProbeRegex("wisps")).
		WillReturnRows(sqlmock.NewRows([]string{"1"}))

	got, err := getChildrenOfDeferredParentsInTx(context.Background(), tx)
	if err != nil {
		t.Fatalf("getChildrenOfDeferredParentsInTx: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("children = %v, want empty", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestGetChildrenOfDeferredParentsInTx_IgnoresMissingWispDependenciesTable(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(deferredParentProbeRegex("issues")).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(deferredChildrenQueryRegex("dependencies", "issues")).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id"}).AddRow("child-from-dependencies-issues"))
	mock.ExpectQuery(deferredChildrenQueryRegex("dependencies", "wisps")).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id"}).AddRow("child-from-dependencies-wisps"))
	mock.ExpectQuery(deferredChildrenQueryRegex("wisp_dependencies", "issues")).
		WillReturnError(errors.New("table wisp_dependencies does not exist"))

	got, err := getChildrenOfDeferredParentsInTx(context.Background(), tx)
	if err != nil {
		t.Fatalf("getChildrenOfDeferredParentsInTx: %v", err)
	}
	want := []string{"child-from-dependencies-issues", "child-from-dependencies-wisps"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("children = %v, want %v", got, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
