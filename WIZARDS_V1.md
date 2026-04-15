# Shipyard TUI Refactor — Interactive Wizards for `cron` and `logs`

> **Audience:** engineering agent (Codex) implementing this refactor.
> **Goal:** evolve Shipyard from a flag-only CLI into a CLI with **beautiful, professional, keyboard-driven interactive wizards** for the `cron` and `logs` subsystems, without breaking any existing command or behavior.
> **Mode:** follow this document step by step, in order. Do not skip steps. Do not merge steps. Each step lists its own acceptance criteria.

---

## 1. Scope & Non-Goals

### In scope

- Add an interactive TUI layer using the Charm stack (Bubble Tea, Bubbles, Lip Gloss).
- Introduce two **interactive control panels**:
  - `shipyard cron config` — full cron CRUD + enable/disable/run through a wizard.
  - `shipyard logs config` — logs exploration + retention configuration through a wizard.
- Every wizard screen must render a **dedicated empty state** when applicable.
- Visual polish: consistent theme, headers, footers, keyboard hints, smooth transitions.
- Automated tests covering wizard state transitions and business-logic integration.

### Out of scope

- Any change to the cron/logs **business logic** (`internal/cron/service.go`, `internal/cron/validate.go`, `internal/logs/service.go`). Reuse as-is.
- Any change to existing flag-based subcommands (`cron add`, `cron list`, `logs show`, etc.). They must keep working identically.
- New business features (Telegram, agents, AI providers) — those come later.

### Hard constraints

- **Do not break** existing commands or flag behavior.
- **Do not modify** public types/functions in `internal/cron` and `internal/logs` unless strictly required (if required, document why in the PR body).
- **Do not introduce** hidden state. All wizard actions must flow through existing `cron.Service` and `logs.Service`.
- **TTY detection:** wizards only run if stdin is a real terminal. In non-TTY contexts (pipes, CI, scripts), print a short message and exit with code `2`.

---

## 2. Dependencies

Add the following Go modules:

```
github.com/charmbracelet/bubbletea        v1.x
github.com/charmbracelet/bubbles          v0.x
github.com/charmbracelet/lipgloss         v1.x
golang.org/x/term                         latest
```

For tests:

```
github.com/charmbracelet/x/exp/teatest    latest
```

Run `go mod tidy` after adding. Commit `go.mod` and `go.sum` together.

**Acceptance:** `GOTOOLCHAIN=go1.26.2 go build ./cmd/shipyard` succeeds. `go test ./...` still passes against pre-existing tests.

---

## 3. Package Layout

Introduce a new TUI tree under `internal/ui/`. Do **not** delete or rename existing files in `internal/ui/`. They remain the non-interactive rendering layer.

```
internal/ui/
├── splash.go                  # keep as-is
├── terminal.go                # keep as-is
└── tui/                       # NEW — interactive layer
    ├── theme/
    │   └── theme.go           # colors, styles, glyphs, sizing tokens
    ├── components/
    │   ├── header.go          # title bar with breadcrumb
    │   ├── footer.go          # keyboard hints bar
    │   ├── menu.go            # vertical list with arrow navigation
    │   ├── form.go            # multi-step form scaffolding
    │   ├── input.go           # single-line text input wrapper
    │   ├── checklist.go       # checkbox list for multi-select
    │   ├── confirm.go         # yes/no confirmation
    │   ├── spinner.go         # inline spinner w/ label
    │   ├── table.go           # styled table (wraps bubbles/table)
    │   ├── viewer.go          # scrollable output viewer
    │   └── empty.go           # standardized empty-state renderer
    ├── tty/
    │   └── detect.go          # IsInteractive() / RequireTTY() helpers
    ├── cronwiz/               # cron wizard (this subsystem)
    │   ├── root.go            # entrypoint model, routes to screens
    │   ├── menu.go            # main menu screen
    │   ├── add.go             # add-job wizard
    │   ├── update.go          # update-job wizard
    │   ├── list.go            # browse-jobs screen
    │   ├── enable.go          # enable-jobs screen
    │   ├── disable.go         # disable-jobs screen
    │   ├── run.go             # run-now screen
    │   ├── delete.go          # delete screen
    │   └── shared.go          # shared helpers (schedule presets etc.)
    └── logwiz/                # logs wizard
        ├── root.go
        ├── menu.go
        ├── sources.go
        ├── show.go
        ├── tail.go
        ├── retention.go
        └── prune.go
```

**Acceptance:** `go build ./...` still succeeds with an empty scaffold in each new file (use `package xxx` + a `// TODO` for now).

---

## 4. Design System (`internal/ui/tui/theme/theme.go`)

Define the visual tokens. All wizards must consume this package — no hard-coded ANSI codes or hex values anywhere else in the TUI tree.

### Palette

Map these semantic roles to Lip Gloss colors (use adaptive colors with light/dark variants):

| Role | Purpose |
|---|---|
| `Primary` | Shipyard brand (deep blue) |
| `Accent` | Highlights, focus borders (cyan) |
| `Success` | Enabled state, completed action (green) |
| `Warning` | Non-blocking warnings (amber) |
| `Danger` | Destructive actions, error messages (red) |
| `Muted` | Secondary text, hints (gray) |
| `Surface` | Card / panel background |
| `SurfaceAlt`| Striped rows, dim backgrounds |
| `Text` | Default foreground |
| `TextInverse` | Text on filled backgrounds |

### Styles (pre-built `lipgloss.Style` values)

- `TitleStyle` — bold, primary color, padding 0 1
- `SubtitleStyle` — muted, italic
- `BreadcrumbStyle` — muted with `›` separator glyph
- `PanelStyle` — rounded border in primary color, padding 1 2
- `FocusedPanelStyle` — same as panel, border in accent
- `MenuItemStyle`, `MenuItemSelectedStyle` — selected version has left indicator `▍` in accent + bold text
- `InputStyle`, `InputFocusedStyle`
- `CheckboxChecked` = `● `, `CheckboxUnchecked` = `○ `
- `LabelStyle` — muted small caps
- `ValueStyle` — bold
- `ErrorStyle` — danger color, prefix `✖ `
- `HintStyle` — muted, prefix `› `
- `SuccessStyle` — success, prefix `✓ `
- `KeyHintStyle` — renders `[key] label` with key in accent, label in muted

### Glyphs (unicode, all consistent)

| Token | Glyph |
|---|---|
| `GlyphBullet` | `•` |
| `GlyphArrow` | `›` |
| `GlyphCheck` | `✓` |
| `GlyphCross` | `✖` |
| `GlyphSelected` | `▍` |
| `GlyphBoxChecked` | `●` |
| `GlyphBoxUnchecked` | `○` |
| `GlyphSpinnerFrames` | `⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏` |

### Layout tokens

- `ContentWidth` = clamp(terminal width - 4, min 60, max 100)
- `MenuIndent` = 2
- `GapSmall` = 1 line, `GapMedium` = 2 lines

### Respect `NO_COLOR`

`theme.go` must check `os.Getenv("NO_COLOR")` and, when set, return unstyled versions of all styles. Wire this through `lipgloss.SetColorProfile` at startup.

**Acceptance:** `go test ./internal/ui/tui/theme/...` passes a basic sanity test asserting each style returns a non-nil value and that `NO_COLOR=1` disables color output.

---

## 5. Shared Components (`internal/ui/tui/components/`)

Each component is a Bubble Tea sub-model with `Init()`, `Update(msg)`, `View()`. All must:

- Accept a theme reference (do not import theme inside `Update` loops; keep allocation out of hot paths).
- Emit **domain messages** (custom `tea.Msg` types), not sideeffects. Screens compose components and translate their messages into service calls.
- Support `Resize(width, height int)` so layouts adapt to window resize.

Required components and their public surface:

### `Header`
Props: title, breadcrumb []string. Renders top bar with Shipyard mark (reuse ascii from `splash.go` in a compact single-line variant), title, and breadcrumb.

### `Footer`
Props: hints `[]{Key, Label}`. Renders bottom bar with keyboard hints. Always include `[esc] back` and `[q] quit` unless the screen is the root.

### `Menu`
Props: items `[]MenuItem{Title, Description, Disabled, Badge string}`. Emits `MenuSelectedMsg{Index, Key string}` on Enter. Arrow keys navigate. Disabled items are skipped.

### `Form`
A multi-step wizard container. Props: steps `[]FormStep` where each step contains a label, a field component (input, select, toggle), optional validator `func(value string) error`. Renders progress indicator `Step 2 of 5`. Emits `FormCompletedMsg{Values map[string]any}` on final submit, or `FormCancelledMsg` on esc/cancel.

### `Input`
Single-line text input with placeholder, optional max length, optional inline validator executed on each change. Shows validation error beneath the field when invalid.

### `Checklist`
Props: items `[]ChecklistItem{ID, Title, Subtitle, Checked}`. Space toggles, `a` toggles all, Enter confirms. Emits `ChecklistConfirmedMsg{SelectedIDs []string}`.

### `Confirm`
Props: prompt, dangerous bool. Two buttons: Confirm / Cancel. Default focus is Cancel when `dangerous=true`. Emits `ConfirmMsg{Accepted bool}`.

### `Spinner`
Wrapping `bubbles/spinner` with label and theme colors.

### `Table`
Wrapping `bubbles/table`. Colors header cells using theme. Supports row selection via Enter.

### `Viewer`
Scrollable read-only text pane (wrap `bubbles/viewport`). Used for showing command output, event streams.

### `Empty`
Renders a centered empty state with glyph, title, description, and an optional call-to-action hint. Props: `{Icon, Title, Description, Hint}`.
Standard empty-state template:

```
                    ⎯ ⎯ ⎯
                     ⛵
            No Shipyard cron jobs yet.

     Create one with the Add action on the main menu.
```

**Acceptance:** each component compiles and has a unit test constructing it, sending representative `tea.Msg` values, and asserting the output state (not just that it did not panic).

---

## 6. TTY Detection (`internal/ui/tui/tty/detect.go`)

```go
func IsInteractive(fd uintptr) bool
func RequireTTY(stdout, stderr io.Writer) error
```

`IsInteractive` wraps `term.IsTerminal`. `RequireTTY` writes a friendly message to `stderr` and returns a sentinel error when not interactive. CLI commands use this at the top of their `RunE`.

Sample non-interactive message:

```
This command requires an interactive terminal.
Use the non-interactive commands instead, for example:
  shipyard cron add --name ... --schedule ... --command ...
```

**Acceptance:** unit test verifies `RequireTTY` returns error when given a non-terminal `uintptr` and writes the expected message.

---

## 7. Wizard Framework (`internal/ui/tui/cronwiz/root.go` / `logwiz/root.go`)

Each root model follows this pattern:

```go
type Root struct {
    theme     theme.Theme
    header    components.Header
    footer    components.Footer
    screen    Screen        // interface: Init/Update/View/Resize
    service   CronService   // injected — for tests
    logger    logs.Service  // optional
    width     int
    height    int
    quitting  bool
}
```

Define `Screen` as an interface so screens are swappable and testable:

```go
type Screen interface {
    Init() tea.Cmd
    Update(msg tea.Msg) (Screen, tea.Cmd)
    View() string
    Title() string
    Breadcrumb() []string
    Footer() []components.KeyHint
}
```

The root model:

- Handles `tea.WindowSizeMsg` globally and propagates to the active screen.
- Handles global keys: `ctrl+c`, `q` (quit), `esc` (screen-level back — delegated to the active screen which can veto).
- Wraps rendering: `header + screen.View() + footer`, with vertical centering and consistent padding from theme.

`NewRoot(service CronService, logger LogsService) *Root` is the constructor used by the CLI layer.

**Acceptance:** root model compiles, handles resize, routes navigation messages between screens, and has a unit test verifying a back-navigation flow (main menu → add → esc → main menu).

---

## 8. Cron Wizard Flows (`shipyard cron config`)

### 8.1 Entrypoint

Wire a new subcommand in `internal/cli/cron.go`:

```go
cmd.AddCommand(newCronConfigCmd())
```

`newCronConfigCmd` calls `tty.RequireTTY(...)`, constructs a `cron.Service`, builds `cronwiz.NewRoot(service)`, and runs the Bubble Tea program.

### 8.2 Main Menu screen (`menu.go`)

Title: `Cron Control Panel`
Breadcrumb: `cron ›`

Items (always in this order):

1. `Add new cron job` — icon `+`
2. `Browse jobs` — icon `≡`, badge = total count; if zero, badge is `empty` muted
3. `Update a job` — disabled if no jobs exist
4. `Enable jobs` — disabled if there are no disabled jobs; badge = disabled count
5. `Disable jobs` — disabled if there are no enabled jobs; badge = enabled count
6. `Run a job now` — disabled if no jobs exist
7. `Delete a job` — disabled if no jobs exist
8. `Exit`

**Empty state (no jobs at all):** show a centered `Empty` component above the menu, with text “No cron jobs yet — start by adding one.” All items except `Add new cron job` and `Exit` appear disabled.

Footer hints: `[↑↓] navigate  [⏎] select  [q] quit`.

### 8.3 Add-job flow (`add.go`)

Uses `Form` with these steps:

**Step 1/5 — Name** (required)
- Label: `Job name`
- Hint: `A short human-readable label for this job.`
- Validator: non-empty, single line (no `\n`).

**Step 2/5 — Description** (optional)
- Label: `Description (optional)`
- Hint: `Press Enter to skip.`
- No validation.

**Step 3/5 — Schedule** (required)
- A two-stage sub-screen:
  - First a `Menu` of presets:
    - `Every minute` → `* * * * *`
    - `Every 5 minutes` → `*/5 * * * *`
    - `Every hour` → `0 * * * *`
    - `Every day at 00:00` → `0 0 * * *`
    - `Every Monday at 09:00` → `0 9 * * 1`
    - `Custom expression…`
  - If `Custom…` is selected, show an `Input` field that calls `cron.validateSchedule` on every change. The validation error, if any, renders beneath the field in `ErrorStyle`. The Next button is disabled while invalid.
- Below the field, always render a **human summary** line of the expression (e.g., `Runs every 15 minutes`). For V1 the summary may be a simple lookup against known presets; if it doesn’t match a known preset, fall back to echoing the raw expression with `(custom)` after it. Do not ship a cron-to-English generator in this iteration.

**Step 4/5 — Command** (required)
- Label: `Command`
- Hint: `The shell command to run. No multi-line values.`
- Validator: non-empty, no `\n`.
- If the command starts with `sudo ` or contains `rm -rf `, render a `WARNING` line beneath the field in `WarningStyle`. Do not block submission.

**Step 5/5 — Enable immediately?**
- Toggle `yes` / `no`. Default `yes`.

**Review step**
- Render a summary panel of all collected values using `LabelStyle` / `ValueStyle`.
- Two buttons: `Confirm` / `Back to edit`.

**Submission**
- On confirm, show a `Spinner` with label `Creating cron job…`
- Call `service.Add(input)`.
- On success: replace with a success panel showing the generated job ID, `[enter] return to menu` footer hint.
- On error: show `ErrorStyle` message, offer `[r] retry` and `[esc] cancel` footer hints.

**Empty-state note:** not applicable to this screen — adding is the way out of the global empty state.

### 8.4 Browse-jobs screen (`list.go`)

Renders a `Table` with columns: `ID`, `Name`, `Schedule`, `Enabled`, `Command` (truncated to 40 chars).

Row keys:
- `⏎` — open details panel (`cron show` equivalent) in a bottom split or overlay.
- `e` — edit selected (push `update.go` with id pre-filled).
- `r` — run selected (push `run.go` with id pre-filled).
- `d` — delete selected (push `delete.go` with id pre-filled).
- `space` — toggle enabled/disabled in place. Uses `service.Enable` / `service.Disable`. Show an inline spinner in the row while the call is in flight.
- `esc` — back to main menu.

**Empty state:** `Empty{Icon: "⎋", Title: "No cron jobs to browse.", Description: "Add one from the main menu.", Hint: "[esc] back"}`.

### 8.5 Update-job flow (`update.go`)

- If no job id is pre-selected: show a `Menu` of jobs to pick one. If no jobs exist, show empty state and `[esc] back`.
- Once selected, run the same `Form` as Add but pre-fill every step with the current job values. The Review step highlights changed fields with `ValueStyle` and leaves unchanged ones in `Muted`.
- On confirm: call `service.Update(id, input)` with only the changed fields set as non-nil pointers. Unchanged fields must be nil — do not send identity updates.

### 8.6 Enable-jobs screen (`enable.go`)

- Load jobs, filter to `Enabled == false`.
- **Empty state:** `Empty{Icon: "✓", Title: "All jobs are already enabled.", Hint: "[esc] back"}`. Do not show the checklist.
- Otherwise render `Checklist` of disabled jobs showing `ID  Name  Schedule`.
- Footer: `[space] toggle  [a] select all  [⏎] enable selected  [esc] cancel`.
- On confirm: for each selected id, call `service.Enable(id)` sequentially, showing a `Spinner` with label `Enabling N of M…`. Aggregate errors and, at the end, render a summary: green success list + red failed list, each with the reason.

### 8.7 Disable-jobs screen (`disable.go`)

Mirror of Enable, but:
- Filters to `Enabled == true`.
- Confirmation uses `dangerous=false` (not destructive).
- **Empty state:** `Empty{Icon: "○", Title: "No enabled jobs to disable.", Hint: "[esc] back"}`.

### 8.8 Run-now screen (`run.go`)

- Select job from `Menu` (empty state if none).
- Show a `Confirm` with the job details and prompt `Run <ID> now?`.
- On confirm: `Spinner` labeled `Running…` + below it a live `Viewer` that fills with stdout as it arrives. (For V1, since `service.Run` returns output in one shot, show a steady spinner and then render the full output on completion. Do not fake streaming.)
- After completion: show exit status (success/failure), elapsed time, and footer `[enter] menu`.

### 8.9 Delete screen (`delete.go`)

- Select job from `Menu` (empty state if none).
- `Confirm{Prompt: "Delete <ID> — <Name>? This cannot be undone.", Dangerous: true}`.
- On confirm: `service.Delete(id)`, success panel, `[enter] menu`.

---

## 9. Logs Wizard Flows (`shipyard logs config`)

### 9.1 Entrypoint

Add `newLogsConfigInteractiveCmd` so that `shipyard logs config` with **no arguments** opens the interactive wizard. The existing `shipyard logs config set retention-days <n>` must continue to work unchanged (those are subcommands with args).

Rule: if `args == nil` AND stdin is a TTY → open wizard. Otherwise, preserve the current read-only `LoadConfig` behavior so piped/scripted usage keeps working. Do not break `logs config set retention-days`.

### 9.2 Main Menu screen

Title: `Logs Control Panel`
Breadcrumb: `logs ›`

Items:

1. `View sources` — badge = source count
2. `Show recent events`
3. `Tail live events`
4. `Configure retention`
5. `Prune old logs`
6. `Exit`

**Empty state (no sources yet):** above the menu, render `Empty{Icon: "⎋", Title: "No logs yet.", Description: "Shipyard writes events automatically as subsystems run.", Hint: "Use the cron wizard to create and run a job to produce log events."}`. Items 1, 2, 3, 5 appear disabled. Items 4 and 6 stay enabled.

### 9.3 View sources (`sources.go`)

- Render a `Table` with columns: `Source`, `Files`, `Size`, `Newest file`.
- `⏎` on a row: push a sub-screen that delegates to `show.go` pre-filtered by that source.
- **Empty state:** same as Main Menu empty-state language.

### 9.4 Show events (`show.go`)

Stage 1 — Filters form (all optional):
- `Source` — select with options loaded from `service.ListSources()` plus an `all sources` entry.
- `Entity ID` — text input.
- `Level` — select: `any`, `info`, `warn`, `error`.
- `Limit` — number input, default 50, min 1, max 500.

Stage 2 — Results:
- Scrollable `Viewer` with rows rendered as:
  `HH:MM:SS  [LEVEL]  source/entity  message`
- Level is colored: `info`=accent, `warn`=warning, `error`=danger.
- Footer: `[↑↓] scroll  [/] filter  [esc] back`.
- `/` opens an inline input that applies a client-side substring filter to the already-loaded events.

**Empty state (no matches):** `Empty{Icon: "⎋", Title: "No log events match these filters.", Hint: "[esc] change filters"}`.

### 9.5 Tail live events (`tail.go`)

- Same filters stage as `show.go` minus the limit field.
- Live view updates as `service.Tail` emits events. Reuse `service.Tail` with a stop channel controlled by the model (`esc` closes it).
- Show a small `● live` indicator in the header while running, green.
- **Empty state / idle state:** `Empty{Icon: "⋯", Title: "Waiting for events…", Hint: "[esc] stop tailing"}` rendered until the first event arrives.

### 9.6 Configure retention (`retention.go`)

- Load current config.
- Single-field `Form`:
  - Label: `Retention (days)`
  - Input: number. Validator: integer, ≥ 1, ≤ 3650.
- Below the input, render a live sentence: `Logs older than N days will be deleted on next prune.`
- On confirm: call `service.SetRetentionDays(n)`, then show success panel with the updated value.

**Empty state:** not applicable. Always show current value.

### 9.7 Prune old logs (`prune.go`)

- Pre-prune preview: load config, scan `ListSources` and compute an estimated `{files, bytes}` that would be deleted. If this is too complex for V1, skip the preview and go straight to confirmation.
- `Confirm{Prompt: "Delete files older than <N> days?", Dangerous: true}`.
- On confirm: call `service.Prune()`, render the result panel with `DeletedFiles` and `FreedBytes`.
- **Empty state (nothing to prune):** `Empty{Icon: "✓", Title: "Nothing to prune.", Description: "All log files are within the retention window.", Hint: "[esc] back"}`.

---

## 10. CLI Wiring

### 10.1 Cron

In `internal/cli/cron.go`:

```go
cmd.AddCommand(newCronConfigCmd())
```

Implement `newCronConfigCmd` so that it:

1. Calls `tty.RequireTTY(cmd.OutOrStderr())`. If it returns an error, return it (exit code 2 via cobra).
2. Builds `service, err := cron.NewService()`.
3. Builds `root := cronwiz.NewRoot(service)`.
4. Runs `tea.NewProgram(root, tea.WithAltScreen()).Run()`.
5. Returns the error from Run (nil on graceful quit).

Flag scheme: no flags for V1.

Help text: short `Interactive cron control panel`, long explains it opens a wizard and points users to the flag-based commands for scripting.

### 10.2 Logs

In `internal/cli/logs.go`, modify `newLogsConfigCmd` so the **bare** `shipyard logs config` invocation branches:

- If stdin is a TTY AND there are no args → open wizard (same pattern as cron).
- Otherwise → current read-only behavior (print config).
- `shipyard logs config set retention-days <n>` keeps working via the existing subcommand tree.

### 10.3 Alt-screen behavior

Use `tea.WithAltScreen()` for both wizards so the user’s scrollback stays intact on exit. After the wizard exits, if the last action produced a useful artifact (e.g., a created job ID, a prune result), echo a one-line summary to the main screen using the existing `ui.Emphasis` / `ui.Highlight` helpers so there’s a textual record outside the TUI.

---

## 11. Tests

Testing is a required deliverable of this refactor. Do not mark the work complete without all of the following.

### 11.1 Component unit tests

Location: `internal/ui/tui/components/*_test.go`.

For each component, write at least:

- **Rendering smoke test:** construct, render once, assert non-empty output and that key literal strings appear.
- **Update test:** send representative `tea.Msg` values (KeyMsg for arrows/space/enter, WindowSizeMsg) and assert on the resulting model state.
- **Emission test:** confirm the component produces the expected domain message on submit.

### 11.2 Theme test

`internal/ui/tui/theme/theme_test.go`:

- All exported styles return non-zero values.
- `NO_COLOR=1` returns styles that render plain text for representative inputs.

### 11.3 TTY helper test

`internal/ui/tui/tty/detect_test.go`:

- `RequireTTY` errors and writes the expected message when given a non-terminal fd.

### 11.4 Wizard screen tests (`cronwiz`, `logwiz`)

For each screen file, write a `_test.go` using a **fake service** that satisfies the screen’s service interface. Cover:

- **Happy path:** simulate the full keystroke sequence a user would type for the success case, assert the fake service recorded the expected call with the expected payload.
- **Validation path:** for forms, send invalid input (empty name, invalid cron expression, etc.) and assert that the model stays on the same step and exposes the validation error in its `View()` output.
- **Empty state path:** inject a fake service that returns zero jobs / zero sources, and assert the screen renders the empty-state copy defined in this document.
- **Cancel / back path:** assert `esc` returns the screen to its parent without calling the service.

Prefer `teatest` (`github.com/charmbracelet/x/exp/teatest`) for keystroke-driven integration tests at the screen and root level. Plain state-machine tests (call `Update` directly) are acceptable and encouraged for the validator-heavy steps.

### 11.5 Integration test at the root

`internal/ui/tui/cronwiz/root_test.go`:

- Boot `Root` with a fake `CronService`.
- Script a sequence: open main menu → choose Add → fill every step → confirm → return to menu.
- Assert the service received exactly one `Add` call with the filled payload and that the final `View()` is back on the main menu.

Equivalent test for `logwiz`.

### 11.6 Regression: existing tests

All pre-existing tests must still pass unmodified:

```
GOTOOLCHAIN=go1.26.2 go test ./...
```

### 11.7 Manual validation checklist

Run a scripted local session and confirm each item:

- `shipyard cron config` opens the wizard, main menu renders, footer hints visible.
- Main menu counts reflect current state.
- Add → Review → Confirm creates a job; `shipyard cron list` (flag command) shows it.
- Update pre-fills current values; saving modifies only changed fields.
- Enable screen shows only disabled jobs; Disable shows only enabled jobs.
- Run screen shows output correctly.
- Delete screen requires confirmation and cannot be triggered accidentally from Cancel focus.
- Empty states render verbatim as specified for:
  - no jobs at all
  - no disabled jobs
  - no enabled jobs
  - no log sources
  - no log events matching filters
  - nothing to prune
- `shipyard logs config` opens the wizard.
- Retention edit persists to disk; `cat ~/.shipyard/logs.json` reflects the new value.
- `shipyard logs config set retention-days 30` still works from the CLI (non-interactive path preserved).
- `NO_COLOR=1 shipyard cron config` renders legibly without ANSI.
- Resize the terminal during a wizard session — layout adapts without glitches.
- Piping `echo | shipyard cron config` exits with code 2 and the expected message (non-TTY).

---

## 12. Execution Order (do not reorder)

1. **Dependencies.** Add modules from §2. `go mod tidy`. Commit.
2. **Scaffold packages.** Create every file listed in §3 with a `package` declaration and a `// TODO` comment. Ensure `go build ./...` succeeds. Commit as `scaffold(tui): create tui package tree`.
3. **Theme.** Implement §4 with its test. Commit.
4. **TTY helper.** Implement §6 with its test. Commit.
5. **Components.** Implement §5 one component at a time, each with its test. Commit per component (small commits preferred).
6. **Cron wizard screens.** Implement §8 screens in this order: `root`, `menu`, `add`, `list`, `update`, `enable`, `disable`, `run`, `delete`. Add tests alongside each screen. Commit per screen.
7. **Logs wizard screens.** Implement §9 screens in this order: `root`, `menu`, `retention`, `sources`, `show`, `tail`, `prune`. Tests alongside. Commit per screen.
8. **CLI wiring.** Implement §10 for cron and logs. Commit.
9. **Integration tests.** Implement §11.5 / §11.4 wizard-level scenarios. Commit.
10. **Manual validation.** Run §11.7 checklist. If any item fails, fix before proceeding.
11. **README.** Update `README.md` with two short sections: `shipyard cron config` and `shipyard logs config`, each with 3-5 lines explaining the wizard. Do not remove documentation of existing commands.
12. **Version bump.** Bump `VERSION` to `0.14`. Commit as `release: 0.14 — interactive cron & logs wizards`.

---

## 13. Definition of Done

All must be true:

- [ ] All new code lives under `internal/ui/tui/**` and does not modify `internal/cron` or `internal/logs` business logic.
- [ ] `shipyard cron config` opens a functional wizard with every flow in §8.
- [ ] `shipyard logs config` opens a functional wizard with every flow in §9.
- [ ] Every screen listed in §8 and §9 renders its specified empty state when applicable.
- [ ] Validation errors in the Add/Update cron schedule step render inline without leaving the step.
- [ ] All tests described in §11.1 through §11.5 exist and pass.
- [ ] All pre-existing tests still pass.
- [ ] The manual validation checklist in §11.7 passes end to end on macOS or Linux.
- [ ] `NO_COLOR=1` works.
- [ ] Non-TTY invocation exits cleanly with code 2.
- [ ] `VERSION` bumped, `README` updated.

---

## 14. Out-of-scope follow-ups (do not implement here)

These are next steps documented for the roadmap but **must not** be introduced in this refactor:

- Telegram bot / daemon subsystem.
- AI provider abstraction.
- Agent registry and runtime.
- Skills / plugins framework.
- Cron expression humanizer (natural-language summary beyond preset lookup).

Leave hooks in the code (package boundaries, service interfaces) that make these future additions clean, but do not add their code.
