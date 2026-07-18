package issueops

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// ga-7r884: LabelRegex was parsed from --label-regex and stored on
// IssueFilter/WorkFilter, but neither BuildIssueFilterClauses nor
// BuildReadyWorkWhere ever read it — same dead-code shape ga-hqchm found in
// LabelPattern (#4882), except LabelRegex can't be pushed into the WHERE
// clause at all (bd does not use SQL REGEXP functions, engdocs/ICU-POLICY.md),
// so the fix is this Go-side post-fetch filter instead of a SQL clause.

func TestCompileLabelRegex(t *testing.T) {
	t.Parallel()

	t.Run("valid pattern compiles", func(t *testing.T) {
		t.Parallel()
		re, err := compileLabelRegex("tech-(debt|legacy)")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !re.MatchString("tech-debt") {
			t.Error("expected compiled regex to match \"tech-debt\"")
		}
	})

	t.Run("invalid pattern returns wrapped error", func(t *testing.T) {
		t.Parallel()
		_, err := compileLabelRegex("tech-(debt")
		if err == nil {
			t.Fatal("expected error for unbalanced group, got nil")
		}
		if got, want := err.Error(), `invalid --label-regex pattern "tech-(debt"`; !containsSubstring(got, want) {
			t.Errorf("error = %q, want substring %q", got, want)
		}
	})

	t.Run("empty pattern compiles (matches everything)", func(t *testing.T) {
		t.Parallel()
		re, err := compileLabelRegex("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !re.MatchString("anything") {
			t.Error("empty pattern should match any string")
		}
	})
}

func TestLabelsMatchRegex(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		labels []string
		re     string
		want   bool
	}{
		{"single label matches", []string{"tech-debt"}, "tech-.*", true},
		{"no labels", nil, "tech-.*", false},
		{"no matching label", []string{"frontend", "urgent"}, "^tech-", false},
		{"one of several labels matches", []string{"frontend", "tech-legacy"}, "^tech-", true},
		{"anchored pattern excludes partial match", []string{"biotech-debt"}, "^tech-", false},
		{"alternation matches second branch", []string{"tech-legacy"}, "tech-(debt|legacy)", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			re, err := compileLabelRegex(tc.re)
			if err != nil {
				t.Fatalf("compileLabelRegex(%q): %v", tc.re, err)
			}
			if got := labelsMatchRegex(tc.labels, re); got != tc.want {
				t.Errorf("labelsMatchRegex(%v, %q) = %v, want %v", tc.labels, tc.re, got, tc.want)
			}
		})
	}
}

func TestFilterIssuesByLabelRegex(t *testing.T) {
	t.Parallel()

	issues := []*types.Issue{
		{ID: "a", Labels: []string{"tech-debt"}},
		{ID: "b", Labels: []string{"frontend"}},
		{ID: "c", Labels: []string{"tech-legacy", "urgent"}},
		{ID: "d", Labels: nil},
	}

	re, err := compileLabelRegex("^tech-")
	if err != nil {
		t.Fatalf("compileLabelRegex: %v", err)
	}

	got := filterIssuesByLabelRegex(issues, re)

	gotIDs := make([]string, len(got))
	for i, issue := range got {
		gotIDs[i] = issue.ID
	}
	want := []string{"a", "c"}
	if len(gotIDs) != len(want) {
		t.Fatalf("filterIssuesByLabelRegex ids = %v, want %v", gotIDs, want)
	}
	for i := range want {
		if gotIDs[i] != want[i] {
			t.Errorf("filterIssuesByLabelRegex ids = %v, want %v", gotIDs, want)
			break
		}
	}

	// ga-hqchm-class regression guard: a non-matching pattern must return an
	// empty slice, not silently pass every issue through (the original bug's
	// exact failure mode for LabelPattern).
	noneRe, err := compileLabelRegex("nonexistent-.*")
	if err != nil {
		t.Fatalf("compileLabelRegex: %v", err)
	}
	if got := filterIssuesByLabelRegex(issues, noneRe); len(got) != 0 {
		t.Errorf("expected empty result for non-matching pattern, got %d issues: %v", len(got), got)
	}

	// Must not mutate the input slice's backing array.
	if len(issues) != 4 {
		t.Errorf("filterIssuesByLabelRegex mutated input slice length: %d, want 4", len(issues))
	}
}

func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || indexOf(s, substr) >= 0)
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
