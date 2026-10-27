package orchestrator

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/fernandotonon/QtMesh-Agents/internal/storage"
)

// ProjectRef is a registered workspace the conductor can target.
type ProjectRef struct {
	ID   string
	Name string
	Path string
}

func refsFromStorage(list []storage.Project) []ProjectRef {
	out := make([]ProjectRef, 0, len(list))
	for _, p := range list {
		out = append(out, ProjectRef{ID: p.ID, Name: p.Name, Path: p.Path})
	}
	return out
}

// RefsFromStorage exports refsFromStorage for IPC/daemon callers.
func RefsFromStorage(list []storage.Project) []ProjectRef {
	return refsFromStorage(list)
}

// MatchProjects finds projects mentioned in text (name, id, or path basename).
// Matching requires a word/token boundary so short ids like "a" do not match inside "please".
func MatchProjects(text string, projects []ProjectRef) []ProjectRef {
	lower := strings.ToLower(text)
	var matched []ProjectRef
	seen := map[string]bool{}
	for _, p := range projects {
		candidates := []string{
			strings.ToLower(p.Name),
			strings.ToLower(p.ID),
			strings.ToLower(filepath.Base(p.Path)),
		}
		for _, c := range candidates {
			if c == "" || len(c) < 2 {
				continue
			}
			if mentionsToken(lower, c) && !seen[p.ID] {
				matched = append(matched, p)
				seen[p.ID] = true
				break
			}
		}
	}
	return matched
}

func mentionsToken(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	// Escape and allow flexible whitespace for multi-word names.
	parts := strings.Fields(needle)
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	pattern := `(?i)(^|[^a-z0-9_+-])` + strings.Join(parts, `\s+`) + `([^a-z0-9_+-]|$)`
	re, err := regexp.Compile(pattern)
	if err != nil {
		return strings.Contains(haystack, needle)
	}
	return re.MatchString(haystack)
}

func formatProjectList(projects []ProjectRef) string {
	if len(projects) == 0 {
		return "(no projects registered — use qma project add <path>)"
	}
	var b strings.Builder
	for _, p := range projects {
		b.WriteString("- ")
		b.WriteString(p.Name)
		b.WriteString(" [")
		b.WriteString(p.ID)
		b.WriteString("] → ")
		b.WriteString(p.Path)
		b.WriteByte('\n')
	}
	return b.String()
}
