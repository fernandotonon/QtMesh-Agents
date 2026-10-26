package review

import (
	"strings"
	"testing"
)

func TestReviewDiffEmpty(t *testing.T) {
	r := ReviewDiff("t1", "  \n")
	if r.Approved || len(r.Findings) != 1 {
		t.Fatalf("empty diff should be blocked: %+v", r)
	}
}

func TestReviewDiffClean(t *testing.T) {
	d := "+++ b/a.go\n+package a\n-old\n"
	r := ReviewDiff("t1", d)
	if !r.Approved || len(r.Findings) != 0 {
		t.Fatalf("clean diff: %+v", r)
	}
}

func TestReviewDiffConflictAndSecret(t *testing.T) {
	d := "+++ b/a.go\n+<<<<<<< HEAD\n+password = \"hunter2hunter2\"\n"
	r := ReviewDiff("t1", d)
	if r.Approved || len(r.Findings) != 2 {
		t.Fatalf("expected 2 blocking findings: %+v", r)
	}
	for _, f := range r.Findings {
		if f.Path != "a.go" {
			t.Fatalf("path = %q", f.Path)
		}
	}
}

func TestReviewDiffLargeIsWarning(t *testing.T) {
	d := "+++ b/a.txt\n" + strings.Repeat("+x\n", MaxDiffLines+1)
	r := ReviewDiff("t1", d)
	if !r.Approved || len(r.Findings) != 1 || r.Findings[0].Severity != SeverityWarn {
		t.Fatalf("large diff: %+v", r)
	}
}
