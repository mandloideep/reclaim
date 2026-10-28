package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// styles are the colors of the screen, from the 16 ANSI colors the tables
// use: done green like tier A, warnings yellow, and none at all under
// NO_COLOR, which the renderer detects. The findings tree colors the tiers
// itself, A green, B yellow and C red.
type styles struct {
	title, bold, dim, warn, good, cursor, key lipgloss.Style
}

func newStyles(re *lipgloss.Renderer) styles {
	return styles{
		title:  re.NewStyle().Bold(true),
		bold:   re.NewStyle().Bold(true),
		dim:    re.NewStyle().Faint(true),
		warn:   re.NewStyle().Foreground(lipgloss.Color("3")),
		good:   re.NewStyle().Foreground(lipgloss.Color("2")).Bold(true),
		cursor: re.NewStyle().Reverse(true),
		key:    re.NewStyle().Bold(true),
	}
}

// keys renders the keys of a footer and what they do, such as "enter scan",
// one part per key, for Model.footer to lay out.
func (s styles) keys(pairs ...string) []string {
	parts := make([]string, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, s.key.Render(pairs[i])+" "+pairs[i+1])
	}
	return parts
}

// pad pads s with spaces to width visible columns.
func pad(s string, width int) string {
	if w := lipgloss.Width(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}

// padLeft pads s on the left to width visible columns.
func padLeft(s string, width int) string {
	if w := lipgloss.Width(s); w < width {
		return strings.Repeat(" ", width-w) + s
	}
	return s
}

// wrapText breaks s into lines of at most width columns.
func wrapText(s string, width int) []string {
	if s == "" {
		return []string{""}
	}
	text := lipgloss.NewStyle().Width(max(width, 10)).Render(s)
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return lines
}

// thousands writes n with commas between groups of three digits.
func thousands(n int) string {
	s := strconv.Itoa(n)
	if n < 0 {
		return "-" + thousands(-n)
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// clock renders an elapsed time as m:ss.
func clock(d time.Duration) string {
	d = d.Round(time.Second)
	return fmt.Sprintf("%d:%02d", int(d/time.Minute), int(d%time.Minute/time.Second))
}

// plural formats a count with the singular or plural noun.
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return thousands(n) + " " + many
}
