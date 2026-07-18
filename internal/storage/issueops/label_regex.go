package issueops

import (
	"fmt"
	"regexp"

	"github.com/steveyegge/beads/internal/types"
)

// compileLabelRegex compiles pattern for use as a Go-side, post-fetch label
// filter. Unlike LabelPattern, which BuildIssueFilterClauses/
// BuildReadyWorkWhere push down as a SQL LIKE clause, LabelRegex can't be
// evaluated in the WHERE clause: bd does not use SQL REGEXP functions
// (engdocs/ICU-POLICY.md — that's the explicit justification for shipping
// without libicu). Callers fetch candidates first, bound by every other
// filter field (all SQL-pushed), and apply this afterward.
func compileLabelRegex(pattern string) (*regexp.Regexp, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid --label-regex pattern %q: %w", pattern, err)
	}
	return re, nil
}

// labelsMatchRegex reports whether any label matches re.
func labelsMatchRegex(labels []string, re *regexp.Regexp) bool {
	for _, label := range labels {
		if re.MatchString(label) {
			return true
		}
	}
	return false
}

// filterIssuesByLabelRegex returns the subset of issues with at least one
// label matching re. issues must already be hydrated (Labels populated).
func filterIssuesByLabelRegex(issues []*types.Issue, re *regexp.Regexp) []*types.Issue {
	filtered := make([]*types.Issue, 0, len(issues))
	for _, issue := range issues {
		if labelsMatchRegex(issue.Labels, re) {
			filtered = append(filtered, issue)
		}
	}
	return filtered
}

// filterIssuesWithCountsByLabelRegex is filterIssuesByLabelRegex for the
// counts-projection result type (SearchIssuesWithCountsInTx,
// GetReadyWorkWithCountsInTx — the `bd ... --json` paths, which hydrate rows
// through a separate scan/orchestration layer from the plain-Issue paths and
// so need their own copy of this post-fetch filter).
func filterIssuesWithCountsByLabelRegex(items []*types.IssueWithCounts, re *regexp.Regexp) []*types.IssueWithCounts {
	filtered := make([]*types.IssueWithCounts, 0, len(items))
	for _, item := range items {
		if item != nil && item.Issue != nil && labelsMatchRegex(item.Issue.Labels, re) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// truncateIssuesWithCounts applies filter.Limit in Go. Used after a
// LabelRegex post-fetch filter has run, since the SQL-level LIMIT that
// normally bounds these results is suppressed whenever LabelRegex is set
// (see buildReadyWorkPredicates and runFilterSearchQueryInTx) — otherwise it
// would cap the pre-regex candidate set and could drop real matches. When
// LabelRegex is unset this is a no-op: the SQL LIMIT already bounded items.
func truncateIssuesWithCounts(items []*types.IssueWithCounts, limit int) []*types.IssueWithCounts {
	if limit > 0 && len(items) > limit {
		return items[:limit]
	}
	return items
}
