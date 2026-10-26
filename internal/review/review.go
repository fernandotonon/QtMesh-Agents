package review

import (
	"fmt"
	"regexp"
	"strings"
)

// Severity levels for findings.
const (
	SeverityInfo  = "info"
	SeverityWarn  = "warn"
	SeverityError = "error"
)

// MaxDiffLines is the changed-line count above which a diff is flagged as large.
const MaxDiffLines = 1000

// Finding is a review note produced during Phase 3 validation.
type Finding struct {
	Severity string
	Path     string
	Message  string
}

// Report summarizes a change review.
type Report struct {
	TaskID   string
	Approved bool
	Findings []Finding
	Summary  string
}

var secretRe = regexp.MustCompile(`(?i)(AKIA[0-9A-Z]{16}|-----BEGIN [A-Z ]*PRIVATE KEY-----|(api[_-]?key|secret|token|password)\s*[:=]\s*["'][^"']{8,}["'])`)

// ReviewDiff statically checks a unified diff and returns a report.
// A report is approved only when it has no error-severity findings; approval
// here is advisory and never replaces human sign-off before merge.
func ReviewDiff(taskID, diff string) Report {
	r := Report{TaskID: taskID}
	if strings.TrimSpace(diff) == "" {
		r.Findings = append(r.Findings, Finding{Severity: SeverityError, Message: "diff is empty"})
		r.Summary = summarize(r)
		return r
	}

	var path string
	changed := 0
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ "):
			path = strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/")
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			changed++
			added := line[1:]
			if strings.HasPrefix(added, "<<<<<<< ") || strings.HasPrefix(added, ">>>>>>> ") || added == "=======" {
				r.Findings = append(r.Findings, Finding{SeverityError, path, "merge conflict marker"})
			}
			if secretRe.MatchString(added) {
				r.Findings = append(r.Findings, Finding{SeverityError, path, "possible hard-coded secret"})
			}
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			changed++
		}
	}
	if changed > MaxDiffLines {
		r.Findings = append(r.Findings, Finding{Severity: SeverityWarn, Message: fmt.Sprintf("large diff: %d changed lines", changed)})
	}

	r.Approved = true
	for _, f := range r.Findings {
		if f.Severity == SeverityError {
			r.Approved = false
		}
	}
	r.Summary = summarize(r)
	return r
}

func summarize(r Report) string {
	if r.Approved {
		return fmt.Sprintf("ok: %d finding(s), no blocking issues", len(r.Findings))
	}
	return fmt.Sprintf("blocked: %d finding(s) include errors", len(r.Findings))
}
