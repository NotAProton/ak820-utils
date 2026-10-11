package mole

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSpawnRoundEmptyQuits(t *testing.T) {
	m := Model{playable: nil, rng: &lockedRNG{r: nil}}
	// spawnRound with no playable must return tea.Quit, never index panic.
	cmd := m.spawnRound()
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("empty playable must quit, got %T", msg)
	}
}

func TestBonusClamped(t *testing.T) {
	m := &Model{level: 1, maxTime: 1.0, roundStart: time.Now().Add(-5 * time.Second), target: "a"}
	before := m.score
	m.registerHit(5000, "a", false)
	if m.score < before {
		t.Fatal("negative bonus reduced score")
	}
	if m.feedback == "" || !strings.Contains(m.feedback, "A") {
		t.Fatalf("hit feedback must name the target, got %q", m.feedback)
	}
}

func TestControlKeysIgnored(t *testing.T) {
	m := Model{playable: []string{"a"}, target: "a", lives: 3, level: 1, maxTime: 1.4, roundStart: time.Now()}
	// Simulate a tab press: multi-char name that can never be a target.
	km := tea.KeyMsg{Type: tea.KeyTab}
	updated, _ := m.Update(km)
	mm := updated.(Model)
	if mm.lives != 3 || mm.misses != 0 {
		t.Fatalf("control key cost a life: lives=%d misses=%d", mm.lives, mm.misses)
	}
}

func TestHeartsBounds(t *testing.T) {
	if len(hearts(-5)) == 0 || len(hearts(99)) == 0 {
		t.Fatal("hearts empty")
	}
}
