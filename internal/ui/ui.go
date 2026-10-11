// Package ui implements the shared Nyan-Cat Retro-Pop terminal styling for
// ak820-utils: cosmic backgrounds, pastel rainbow ribbons, and rounded
// candy-coated cards built on lipgloss.
package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Palette tokens. All inputs use 24-bit TrueColor hex so panels stay
// readable on deep navy; never use raw ANSI 16 colors for content.
var (
	ColorVoidDark   = lipgloss.Color("#0c0f1d")
	ColorVoidCard   = lipgloss.Color("#161a2e")
	ColorVoidBorder = lipgloss.Color("#2a3258")
	ColorTextMain   = lipgloss.Color("#f8f8f2")
	ColorTextMuted  = lipgloss.Color("#7b83a6")

	ColorRainbowRed    = lipgloss.Color("#ff5983")
	ColorRainbowOrange = lipgloss.Color("#ff944d")
	ColorRainbowYellow = lipgloss.Color("#ffea6c")
	ColorRainbowGreen  = lipgloss.Color("#56f89a")
	ColorRainbowBlue   = lipgloss.Color("#3bc9ff")
	ColorRainbowPurple = lipgloss.Color("#ba77ff")

	ColorPopTartPink  = lipgloss.Color("#ff77a9")
	ColorPopTartCrust = lipgloss.Color("#e8b382")
	ColorSparkleGold  = lipgloss.Color("#ffe57f")
)

// Rainbow returns the ribbon order red through purple.
func Rainbow() []lipgloss.TerminalColor {
	return []lipgloss.TerminalColor{
		ColorRainbowRed, ColorRainbowOrange, ColorRainbowYellow,
		ColorRainbowGreen, ColorRainbowBlue, ColorRainbowPurple,
	}
}

// Cached styles. View functions must reuse these instead of building new
// styles per cell per frame.
var (
	StyleTitle = lipgloss.NewStyle().Bold(true).Foreground(ColorPopTartPink)
	StyleDim   = lipgloss.NewStyle().Foreground(ColorTextMuted)
	StyleMain  = lipgloss.NewStyle().Foreground(ColorTextMain)
	StyleGold  = lipgloss.NewStyle().Bold(true).Foreground(ColorSparkleGold)
	StyleHit   = lipgloss.NewStyle().Bold(true).Foreground(ColorRainbowGreen)
	StyleMiss  = lipgloss.NewStyle().Bold(true).Foreground(ColorRainbowRed)
	StyleCard  = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorVoidBorder).
			Background(ColorVoidCard).
			Padding(1, 2).
			Margin(0, 1)
	StyleCardActive = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorPopTartPink).
			Background(ColorVoidCard).
			Padding(1, 2).
			Margin(0, 1)
)

// RenderRainbowStrip renders a horizontal ribbon of width columns using
// block glyphs across the rainbow palette. Widths at or below zero render
// an empty string; widths smaller than the color count still cover every
// color by giving the remainder to the final segment.
func RenderRainbowStrip(width int) string {
	if width <= 0 {
		return ""
	}
	colors := Rainbow()
	chunk := width / len(colors)
	if chunk < 1 {
		chunk = 1
	}
	var sb strings.Builder
	used := 0
	for i, c := range colors {
		size := chunk
		if i == len(colors)-1 {
			size = width - used
		} else if used+size > width {
			size = width - used
		}
		if size <= 0 {
			break
		}
		sb.WriteString(lipgloss.NewStyle().Foreground(c).Render(strings.Repeat("▀", size)))
		used += size
		if used >= width {
			break
		}
	}
	// Pad any remainder caused by tiny widths so callers always get the
	// requested visible width.
	if got := lipgloss.Width(sb.String()); got < width {
		sb.WriteString(strings.Repeat(" ", width-got))
	}
	return sb.String()
}

// Badge renders an uppercase pill label. Active pills invert to pink on
// dark; passive pills stay muted.
func Badge(text string, active bool) string {
	if active {
		return lipgloss.NewStyle().Background(ColorPopTartPink).Foreground(ColorVoidDark).
			Bold(true).Padding(0, 1).Render("✦ " + strings.ToUpper(text))
	}
	return lipgloss.NewStyle().Background(ColorVoidBorder).Foreground(ColorTextMuted).
		Bold(true).Padding(0, 1).Render("✦ " + strings.ToUpper(text))
}

// Pill renders a compact value pill with an explicit background color.
func Pill(text string, bg lipgloss.TerminalColor) string {
	return lipgloss.NewStyle().Background(bg).Foreground(ColorVoidDark).
		Bold(true).Padding(0, 1).Render(text)
}

// RenderCard renders a rounded container with a badge header. The body must
// already be styled; cards never mutate it.
func RenderCard(title, body string, active bool) string {
	style := StyleCard
	if active {
		style = StyleCardActive
	}
	sparkle := lipgloss.NewStyle().Foreground(ColorSparkleGold).Render("✧")
	header := lipgloss.JoinHorizontal(lipgloss.Center, Badge(title, active), " ", sparkle)
	return style.Render(lipgloss.JoinVertical(lipgloss.Left, header, "", body))
}

// ClampWidth bounds a requested terminal width to a usable range. Values at
// or below zero fall back to 80; oversized terminals cap at 120 so ribbons
// and cards never stretch into unreadable lines.
func ClampWidth(w int) int {
	if w <= 0 {
		return 80
	}
	if w > 120 {
		return 120
	}
	return w
}
