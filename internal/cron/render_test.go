package cron

import (
	"strings"
	"testing"
	"time"
)

func TestRenderCrontabPreservesExternalAndRewritesManaged(t *testing.T) {
	t.Parallel()

	existing := strings.Join([]string{
		"SHELL=/bin/zsh",
		"# shipyard:ABC123 Nightly Backup",
		"0 * * * * /old/backup",
		"MAILTO=user@example.com",
		"",
	}, "\n")

	jobs := []Job{
		{
			ID:        "ZX90Q1",
			Name:      "Fresh Backup",
			Schedule:  "*/15 * * * *",
			Command:   "/usr/local/bin/backup",
			Enabled:   true,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
	}

	rendered := renderCrontab(existing, jobs, "/usr/local/bin/shipyard")

	if !strings.Contains(rendered, "SHELL=/bin/zsh") || !strings.Contains(rendered, "MAILTO=user@example.com") {
		t.Fatalf("rendered crontab did not preserve external entries: %q", rendered)
	}
	if strings.Contains(rendered, "ABC123") || strings.Contains(rendered, "/old/backup") {
		t.Fatalf("rendered crontab retained old managed entries: %q", rendered)
	}
	if !strings.Contains(rendered, "# shipyard:ZX90Q1 Fresh Backup") {
		t.Fatalf("rendered crontab missing new managed header: %q", rendered)
	}
	if !strings.Contains(rendered, "*/15 * * * * /usr/local/bin/shipyard cron run ZX90Q1") {
		t.Fatalf("rendered crontab missing wrapped command: %q", rendered)
	}
	if strings.Contains(rendered, "/usr/local/bin/backup") {
		t.Fatalf("rendered crontab leaked raw user command: %q", rendered)
	}
}

func TestRenderManagedEntriesQuotesBinaryWithSpaces(t *testing.T) {
	t.Parallel()

	jobs := []Job{{
		ID:       "AB12CD",
		Name:     "Quoted",
		Schedule: "0 * * * *",
		Command:  "/bin/echo hi",
		Enabled:  true,
	}}

	rendered := renderManagedEntries(jobs, "/Users/leo dev/bin/shipyard")
	want := "0 * * * * '/Users/leo dev/bin/shipyard' cron run AB12CD"
	if !strings.Contains(rendered, want) {
		t.Fatalf("rendered = %q, want substring %q", rendered, want)
	}
}

func TestRenderManagedEntriesSkipsDisabled(t *testing.T) {
	t.Parallel()

	jobs := []Job{{
		ID:       "AB12CD",
		Name:     "Disabled",
		Schedule: "0 * * * *",
		Command:  "/bin/echo hi",
		Enabled:  false,
	}}

	if got := renderManagedEntries(jobs, "/usr/local/bin/shipyard"); got != "" {
		t.Fatalf("renderManagedEntries with disabled job = %q, want empty", got)
	}
}
