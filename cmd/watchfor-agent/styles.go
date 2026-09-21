//go:build linux

package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	accent    = lipgloss.Color("#4F8EF7")
	green     = lipgloss.Color("#22C55E")
	amber     = lipgloss.Color("#F59E0B")
	fgDim     = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#8B95A7"}
	fgText    = lipgloss.AdaptiveColor{Light: "#111827", Dark: "#E5E7EB"}
	border    = lipgloss.AdaptiveColor{Light: "#D1D5DB", Dark: "#2B3445"}
	stTitle   = lipgloss.NewStyle().Bold(true).Foreground(fgText)
	stDim     = lipgloss.NewStyle().Foreground(fgDim)
	stOn      = lipgloss.NewStyle().Foreground(green).Bold(true)
	stOff     = lipgloss.NewStyle().Foreground(fgDim)
	stWarn    = lipgloss.NewStyle().Foreground(amber)
	stCursor  = lipgloss.NewStyle().Foreground(accent).Bold(true)
	stRowSel  = lipgloss.NewStyle().Bold(true).Foreground(fgText)
	stRow     = lipgloss.NewStyle().Foreground(fgText)
	stPane    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.AdaptiveColor{Light: "#93C5FD", Dark: "#3B5B9A"}).Padding(0, 1)
	stKey     = lipgloss.NewStyle().Foreground(accent).Bold(true)
	bandBg    = lipgloss.AdaptiveColor{Light: "#DBEAFE", Dark: "#1E2A47"}
	stBand    = lipgloss.NewStyle().Background(bandBg)
	stRule    = lipgloss.NewStyle().Foreground(accent)
	stGroup   = lipgloss.NewStyle().Foreground(fgDim).Bold(true)
	stBig     = lipgloss.NewStyle().Bold(true).Foreground(fgText)
	stBarOn   = lipgloss.NewStyle().Foreground(green)
	stBarWarn = lipgloss.NewStyle().Foreground(amber)
	stBarBad  = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444"))
	stBarOff  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#D1D5DB", Dark: "#334155"})
	// the wordmark: blue to cyan, letter by letter
	wordmark = []string{"#3B82F6", "#3F8AF5", "#4392F4", "#479AF2", "#4BA2F0", "#4FAAEE", "#53B2EC", "#57BAEA", "#5BC2E8", "#5FCAE6", "#63D2E4", "#67DAE2"}
)

func brand(text string) string {
	var b strings.Builder
	for i, r := range text {
		c := wordmark[min(i, len(wordmark)-1)]
		b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(c)).Render(string(r)))
	}
	return b.String()
}

// bar draws a usage bar: ████████░░░░ 53%, coloured by how full it is.
func bar(pct float64, width int) string {
	if width < 4 {
		width = 4
	}
	filled := int(pct/100*float64(width) + 0.5)
	if filled > width {
		filled = width
	}
	style := stBarOn
	switch {
	case pct >= 90:
		style = stBarBad
	case pct >= 75:
		style = stBarWarn
	}
	return style.Render(strings.Repeat("█", filled)) + stBarOff.Render(strings.Repeat("░", width-filled)) + fmt.Sprintf(" %3.0f%%", pct)
}
