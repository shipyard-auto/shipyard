package cron

import (
	"fmt"
	"strings"
)

const shipyardPrefix = "# shipyard:"

func renderCrontab(existing string, jobs []Job, binaryPath string) string {
	external := stripManagedEntries(existing)
	managed := renderManagedEntries(jobs, binaryPath)

	var sections []string
	if strings.TrimSpace(external) != "" {
		sections = append(sections, strings.TrimRight(external, "\n"))
	}
	if strings.TrimSpace(managed) != "" {
		sections = append(sections, strings.TrimRight(managed, "\n"))
	}

	if len(sections) == 0 {
		return ""
	}

	return strings.Join(sections, "\n\n") + "\n"
}

func stripManagedEntries(existing string) string {
	lines := strings.Split(existing, "\n")
	if len(lines) == 0 {
		return ""
	}

	kept := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(line, shipyardPrefix) {
			if i+1 < len(lines) {
				next := lines[i+1]
				if strings.TrimSpace(next) != "" && !strings.HasPrefix(strings.TrimSpace(next), "#") {
					i++
				}
			}
			continue
		}
		kept = append(kept, line)
	}

	return strings.TrimRight(strings.Join(kept, "\n"), "\n")
}

func renderManagedEntries(jobs []Job, binaryPath string) string {
	lines := make([]string, 0, len(jobs)*2)
	quotedBinary := shellQuote(binaryPath)
	for _, job := range jobs {
		if !job.Enabled {
			continue
		}

		lines = append(lines, fmt.Sprintf("%s%s %s", shipyardPrefix, job.ID, sanitizeComment(job.Name)))
		lines = append(lines, fmt.Sprintf("%s %s cron run %s", job.Schedule, quotedBinary, job.ID))
	}

	return strings.Join(lines, "\n")
}

func sanitizeComment(text string) string {
	text = strings.ReplaceAll(text, "\n", " ")
	return strings.TrimSpace(text)
}

// shellQuote wraps s in single quotes when it contains characters that the
// shell would otherwise interpret. Cron passes the line to /bin/sh -c, so a
// binary path with spaces (e.g. "/Users/foo bar/bin/shipyard") must be quoted.
func shellQuote(s string) string {
	if s == "" {
		return s
	}
	if !strings.ContainsAny(s, " \t'\"\\$`*?~|;&<>(){}[]#!") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
