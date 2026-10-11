package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestRainbowStripWidths(t *testing.T) {
	if got := RenderRainbowStrip(0); got != "" {
		t.Fatalf("width 0 must be empty, got %q", got)
	}
	if got := RenderRainbowStrip(-5); got != "" {
		t.Fatalf("negative width must be empty, got %q", got)
	}
	for _, w := range []int{1, 2, 3, 5, 6, 7, 40, 80} {
		got := RenderRainbowStrip(w)
		if lipgloss.Width(got) != w {
			t.Errorf("width %d: visible width = %d", w, lipgloss.Width(got))
		}
	}
}

func TestRainbowCoversAllColors(t *testing.T) {
	if len(Rainbow()) != 6 {
		t.Fatalf("rainbow must have 6 stops, got %d", len(Rainbow()))
	}
}

func TestClampWidth(t *testing.T) {
	if ClampWidth(0) != 80 || ClampWidth(-3) != 80 {
		t.Error("non-positive width must fall back to 80")
	}
	if ClampWidth(1000) != 120 {
		t.Error("oversized width must cap at 120")
	}
	if ClampWidth(80) != 80 {
		t.Error("in-range width must pass through")
	}
}

func TestBadgeCardPillNonEmpty(t *testing.T) {
	if Badge("x", true) == "" || Badge("x", false) == "" {
		t.Error("badge empty")
	}
	if Pill("1", ColorSparkleGold) == "" {
		t.Error("pill empty")
	}
	if RenderCard("t", "body", true) == "" || RenderCard("t", "body", false) == "" {
		t.Error("card empty")
	}
	if !strings.Contains(RenderCard("t", "body", true), "body") {
		t.Error("card must contain body verbatim")
	}
}

func TestCachedStylesShared(t *testing.T) {
	a := StyleTitle
	b := StyleTitle
	if lipgloss.Width(a.Render("x")) != lipgloss.Width(b.Render("x")) {
		t.Error("cached styles diverged")
	}
}
