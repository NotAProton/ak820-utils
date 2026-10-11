package mole

import (
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestHitNamesTargetRegression(t *testing.T) {
	m := &Model{level: 1, maxTime: 1.0, roundStart: time.Now(), lives: 3}
	m.registerHit(120, "q", false)
	if !strings.Contains(m.feedback, "Q") {
		t.Fatalf("feedback must name target Q, got %q", m.feedback)
	}
	m2 := Model{playable: []string{"q"}, target: "q", lives: 3, level: 1,
		maxTime: 1.4, roundStart: time.Now()}
	km := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}
	updated, _ := m2.Update(km)
	mm := updated.(Model)
	if mm.hits != 1 {
		t.Fatalf("q hit not counted: %+v", mm)
	}
	if !strings.Contains(mm.feedback, "Q") {
		t.Fatalf("key-path feedback must name Q, got %q", mm.feedback)
	}
}

func TestWrongKeyNamesBoth(t *testing.T) {
	m := Model{playable: []string{"a", "b"}, target: "a", lives: 3, level: 1,
		maxTime: 1.4, roundStart: time.Now()}
	km := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}}
	updated, _ := m.Update(km)
	mm := updated.(Model)
	if mm.misses != 1 || mm.lives != 2 || mm.combo != 0 {
		t.Fatalf("miss bookkeeping wrong: %+v", mm)
	}
	if !strings.Contains(mm.feedback, "B") || !strings.Contains(mm.feedback, "A") {
		t.Fatalf("miss must name pressed and wanted, got %q", mm.feedback)
	}
}

func TestGoldenCapsLives(t *testing.T) {
	m := &Model{level: 1, maxTime: 1.0, roundStart: time.Now(), lives: maxLives}
	m.registerHit(100, "a", true)
	if m.lives != maxLives {
		t.Fatalf("golden must not exceed max lives: %d", m.lives)
	}
	m.lives = 1
	m.registerHit(100, "a", true)
	if m.lives != 2 {
		t.Fatalf("golden must restore one life: %d", m.lives)
	}
}

func TestReactionsCapped(t *testing.T) {
	m := &Model{level: 1, maxTime: 1.0, roundStart: time.Now(), lives: 3}
	for i := 0; i < maxReactions+50; i++ {
		m.registerHit(int64(i), "a", false)
	}
	if len(m.reactions) != maxReactions {
		t.Fatalf("reactions must cap at %d, got %d", maxReactions, len(m.reactions))
	}
}

func TestExpiryAndGameOver(t *testing.T) {
	m := Model{playable: []string{"a"}, target: "a", lives: 1, level: 9,
		maxTime: 0.45, roundStart: time.Now().Add(-time.Second)}
	updated, _ := m.Update(tickMsg(time.Now()))
	mm := updated.(Model)
	if mm.lives != 0 || mm.misses != 1 {
		t.Fatalf("expiry must cost last life: %+v", mm)
	}
	if mm.maxTime != 0.45 {
		t.Errorf("maxTime floor must hold at 0.45, got %v", mm.maxTime)
	}
	view := mm.View()
	if view == "" || !strings.Contains(view, "GAME OVER") {
		t.Fatalf("game-over card missing: %q", view)
	}
	// Ticks after death must be no-ops, never negative lives.
	updated, _ = mm.Update(tickMsg(time.Now()))
	if updated.(Model).lives != 0 {
		t.Error("lives went negative after death")
	}
}

func TestViewAlwaysRenders(t *testing.T) {
	cases := []Model{
		{},
		{target: "a", maxTime: 1.0, roundStart: time.Now(), lives: 3},
		{quitting: true},
		{width: -5},
	}
	for i, c := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("case %d panicked: %v", i, r)
				}
			}()
			if c.View() == "" {
				t.Errorf("case %d empty view", i)
			}
		}()
	}
}

func TestPaintNilBoardNoPanic(t *testing.T) {
	m := &Model{target: "a", maxTime: 1.0, roundStart: time.Now(), playable: []string{"a"}}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("paint panicked on nil board: %v", r)
		}
	}()
	m.paint(time.Now())
	m.hitFlash = time.Now()
	m.levelUpAt = time.Now()
	m.paint(time.Now())
	m.missFlash = time.Now()
	m.paint(time.Now())
}

func TestNewNilBoard(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Error("New(nil) must fail")
	}
}

func TestScoreNoPersistAccuracy(t *testing.T) {
	m := Model{score: 100, hits: 0, misses: 0}
	st := m.Score()
	if st.Accuracy != 0 {
		t.Errorf("0/0 accuracy must be 0, got %d", st.Accuracy)
	}
}

func TestLockedRNGConcurrent(t *testing.T) {
	r := &lockedRNG{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = r.intn(0)
			_ = r.intn(5)
			_ = r.float64()
		}()
	}
	wg.Wait()
	if got := r.intn(0); got != 0 {
		t.Errorf("intn(0) must be 0, got %d", got)
	}
}

func TestSpawnNilRNGDeterministic(t *testing.T) {
	m := Model{playable: []string{"a", "b"}, rng: &lockedRNG{}}
	cmd := m.spawnRound()
	msg := cmd().(spawnRoundMsg)
	if msg.target != "a" && msg.target != "b" {
		t.Fatalf("bad target %q", msg.target)
	}
}
