package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
)

const ansiReset = "\033[0m"

type Style string

const (
	StyleBold   Style = "\033[1m"
	StyleDim    Style = "\033[2m"
	StyleCyan   Style = "\033[36m"
	StyleBlue   Style = "\033[34m"
	StyleGreen  Style = "\033[32m"
	StyleRed    Style = "\033[31m"
	StyleYellow Style = "\033[33m"
	StyleWhite  Style = "\033[97m"
)

func SupportsColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}

	term := os.Getenv("TERM")
	return term != "" && term != "dumb"
}

func Paint(text string, styles ...Style) string {
	if !SupportsColor() || len(styles) == 0 {
		return text
	}

	var b strings.Builder
	for _, style := range styles {
		b.WriteString(string(style))
	}
	b.WriteString(text)
	b.WriteString(ansiReset)
	return b.String()
}

func Line(symbol string, width int, style ...Style) string {
	return Paint(strings.Repeat(symbol, width), style...)
}

func SectionTitle(title string) string {
	return Paint(title, StyleBold, StyleCyan)
}

func Muted(text string) string {
	return Paint(text, StyleDim)
}

func Highlight(text string) string {
	return Paint(text, StyleBold, StyleGreen)
}

func Emphasis(text string) string {
	return Paint(text, StyleBold, StyleWhite)
}

func Printf(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, format, args...)
}
