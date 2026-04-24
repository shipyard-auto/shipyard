package logs

import (
	"fmt"
	"io"
	"strings"

	"github.com/shipyard-auto/shipyard/internal/ui"
)

// RenderOptions tweaks pretty-printing behavior.
type RenderOptions struct {
	// ShowSource adds the source name as a prefix. Useful when querying
	// across multiple sources.
	ShowSource bool
	// ShowTrace appends the trace id (first 8 chars) when present.
	ShowTrace bool
}

// RenderPretty writes a single human-readable line for rec to w.
// The format is source-aware: HTTP records highlight the status code,
// crew records highlight tool name, lifecycle records use entity id.
func RenderPretty(w io.Writer, rec Record, opts RenderOptions) error {
	ts := rec.Timestamp.Local().Format("15:04:05")
	level := strings.ToUpper(rec.Level)

	parts := []string{
		ui.Paint(ts, ui.StyleDim),
		paintLevel(level),
	}
	if opts.ShowSource && rec.Source != "" {
		parts = append(parts, ui.Paint(rec.Source, ui.StyleCyan))
	}

	switch rec.Source {
	case SourceFairway:
		parts = append(parts, renderFairway(rec))
	case SourceCrew:
		parts = append(parts, renderCrew(rec))
	default:
		parts = append(parts, renderEntity(rec))
	}

	if rec.Message != "" {
		parts = append(parts, rec.Message)
	} else if rec.Event != "" {
		parts = append(parts, ui.Paint(rec.Event, ui.StyleDim))
	}
	if rec.Error != "" {
		parts = append(parts, ui.Paint("error="+rec.Error, ui.StyleRed))
	}
	if opts.ShowTrace && rec.TraceID != "" {
		short := rec.TraceID
		if len(short) > 8 {
			short = short[:8]
		}
		parts = append(parts, ui.Paint("trace="+short, ui.StyleDim))
	}

	_, err := fmt.Fprintln(w, strings.Join(parts, " "))
	return err
}

func renderFairway(rec Record) string {
	if rec.HTTPMethod == "" && rec.HTTPPath == "" {
		return ui.Paint(rec.Event, ui.StyleDim)
	}
	status := paintStatus(rec.HTTPStatus)
	dur := fmt.Sprintf("%dms", rec.DurationMs)
	return fmt.Sprintf("%s %s %s %s", status, rec.HTTPMethod, rec.HTTPPath, ui.Paint(dur, ui.StyleDim))
}

func renderCrew(rec Record) string {
	switch rec.Event {
	case EventToolCallStart, EventToolCallEnd:
		ok := "ok"
		style := ui.StyleGreen
		if !rec.ToolOK && rec.Event == EventToolCallEnd {
			ok = "fail"
			style = ui.StyleRed
		}
		return fmt.Sprintf("%s %s %s", ui.Paint(rec.ToolName, ui.StyleBold), ui.Paint(ok, style), ui.Paint(fmt.Sprintf("%dms", rec.DurationMs), ui.StyleDim))
	case EventRunStart, EventRunEnd, EventRunError:
		name := rec.EntityName
		if name == "" {
			name = rec.EntityID
		}
		return fmt.Sprintf("%s %s", ui.Paint(name, ui.StyleBold), ui.Paint(rec.Event, ui.StyleDim))
	}
	return ui.Paint(rec.Event, ui.StyleDim)
}

func renderEntity(rec Record) string {
	if rec.EntityID == "" {
		return ui.Paint(rec.Event, ui.StyleDim)
	}
	id := ui.Paint(rec.EntityID, ui.StyleCyan)
	if rec.EntityName != "" {
		return fmt.Sprintf("%s/%s", id, rec.EntityName)
	}
	return id
}

func paintLevel(level string) string {
	switch level {
	case "ERROR":
		return ui.Paint(level, ui.StyleBold, ui.StyleRed)
	case "WARN":
		return ui.Paint(level, ui.StyleBold, ui.StyleYellow)
	case "INFO":
		return ui.Paint(level, ui.StyleBold, ui.StyleGreen)
	case "DEBUG":
		return ui.Paint(level, ui.StyleDim)
	}
	return level
}

func paintStatus(status int) string {
	s := fmt.Sprintf("%d", status)
	switch {
	case status == 0:
		return ui.Paint("---", ui.StyleDim)
	case status >= 500:
		return ui.Paint(s, ui.StyleBold, ui.StyleRed)
	case status >= 400:
		return ui.Paint(s, ui.StyleBold, ui.StyleYellow)
	case status >= 300:
		return ui.Paint(s, ui.StyleCyan)
	case status >= 200:
		return ui.Paint(s, ui.StyleBold, ui.StyleGreen)
	}
	return s
}
