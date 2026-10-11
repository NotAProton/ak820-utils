package viz

import (
	"math"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTickWithoutAudioNoPanic(t *testing.T) {
	m := Model{bars: make([]float64, numBars), peaks: make([]float64, numBars), width: 80, startT: time.Now()}
	m.lastPaint = time.Now()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("tick panicked: %v", r)
		}
	}()
	updated, cmd := m.Update(tickMsg(time.Now()))
	_ = updated
	_ = cmd
	if got := updated.(Model).View(); got == "" {
		t.Error("View empty after tick")
	}
}

func TestPaintNilBoardSafe(t *testing.T) {
	m := &Model{startT: time.Now()}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("paintKeyboard panicked on nil board: %v", r)
		}
	}()
	m.paintKeyboard(time.Now(), 1.0/60)
}

func TestBadIndexesClamped(t *testing.T) {
	m := Model{bars: make([]float64, numBars), peaks: make([]float64, numBars),
		themeIx: 99, modeIx: -3, width: 80, startT: time.Now()}
	m.paintKeyboard(time.Now(), 1.0/60)
	if got := m.View(); got == "" {
		t.Error("View empty with bad indexes")
	}
	// Update must also clamp theme/mode cycling on empty lists safely.
	m.themeIx = 0
	m.modeIx = 0
	updated, _ := m.Update(tea.WindowSizeMsg{Width: -10})
	if updated.(Model).width != 80 {
		t.Errorf("negative window width must fall back to 80, got %d", updated.(Model).width)
	}
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 500})
	if updated.(Model).width != 120 {
		t.Errorf("oversized width must cap at 120, got %d", updated.(Model).width)
	}
}

func TestDeriveSpectrumEdges(t *testing.T) {
	var s spectrum
	// nil, short, negative dt, huge dt must never panic.
	deriveSpectrum(&s, nil, 1.0/60)
	deriveSpectrum(&s, []int{1}, -1)
	deriveSpectrum(&s, make([]int, numBars), 99)
	// Out-of-range bars clamp to 0..100.
	raw := make([]int, numBars)
	for i := range raw {
		raw[i] = 1000
	}
	raw[0] = -50
	for i := 0; i < 120; i++ {
		deriveSpectrum(&s, raw, 1.0/60)
	}
	for i, v := range s.mono {
		if v < 0 || v > 1 {
			t.Errorf("mono[%d] = %v out of 0..1", i, v)
		}
	}
	if s.agc < 0.08 {
		t.Errorf("agc floor violated: %v", s.agc)
	}
	if math.IsNaN(s.overall) || math.IsInf(s.overall, 0) {
		t.Error("overall non-finite")
	}
}

func TestHelpersBounded(t *testing.T) {
	var s spectrum
	s.agc = 0
	if g := s.gain(); g < 1 || g > 2.5 {
		t.Errorf("gain(%v) = %v out of 1..2.5", s.agc, g)
	}
	s.agc = 10
	if g := s.gain(); g != 1 {
		t.Errorf("saturated agc must give gain 1, got %v", g)
	}
	if got := idleMix(&spectrum{quietFor: 0}, 0.8, 3, 1.0); got != 0.8 {
		t.Errorf("fresh audio must pass through, got %v", got)
	}
	if got := idleMix(&spectrum{quietFor: 30}, 0.8, 3, 1.0); got < 0 || got > 1 {
		t.Errorf("idle mix out of range: %v", got)
	}
	for _, h := range []float64{-1, 0, 0.25, 0.5, 1, 2.7} {
		r, g, b := hsvRGB(h, 0.85, 1)
		_ = r
		_ = g
		_ = b
	}
	for _, tt := range []float64{0, 1.5, 100} {
		if v := flowField(0.3, 0.7, tt); v < -1.01 || v > 1.01 {
			t.Errorf("flowField out of -1..1: %v", v)
		}
	}
	// All painters must stay in gamut on silence and on full blast.
	hot := spectrum{bass: 1, mid: 1, treble: 1, overall: 1, left: 1, right: 1}
	for i := range hot.mono {
		hot.mono[i] = 1
		hot.peaks[i] = 1
		hot.onset[i] = 1
	}
	m := &Model{startT: time.Now()}
	m.fx.frame = 41
	c := keyCell{slot: 5, row: 2, x: 0.3, y: 0.4, px: 0.3 * kbAspect, py: 0.4}
	now := time.Now()
	for _, tc := range [][3]byte{
		m.paintBars(c, themes[0], &spectrum{}),
		m.paintRipple(c, themes[1], now, &spectrum{quietFor: 30}),
		m.paintBloom(c, themes[2], 1.0/60, &spectrum{}),
		m.paintPlasma(c, themes[0], now, &spectrum{quietFor: 30}),
		m.paintWings(c, themes[1], 1.0/60, &spectrum{}),
		m.paintBars(c, themes[0], &hot),
		m.paintRipple(c, themes[1], now, &hot),
		m.paintBloom(c, themes[2], 1.0/60, &hot),
		m.paintPlasma(c, themes[0], now, &hot),
		m.paintWings(c, themes[1], 1.0/60, &hot),
	} {
		_ = tc
	}
}

func TestFlickerStable(t *testing.T) {
	fx := &modeState{frame: 0}
	a := fx.flicker(9)
	fx.frame = 1
	b := fx.flicker(9)
	if math.Abs(a-b) > 0.25 {
		t.Errorf("adjacent frames must interpolate, flicker jumped %v -> %v", a, b)
	}
	fx.frame = 1<<31 - 1 // near wrap must not panic
	_ = fx.flicker(9)
	fx.frame++
	_ = fx.twinkle(9, 12.5, 0.9)
	_ = fx.twinkle(9, 12.5, 0)
}

func TestAccentBeatTempo(t *testing.T) {
	fx := &modeState{beatPeriod: 0.5}
	now := time.Now()
	fx.noteBeat(now)
	if fx.beatCount != 1 {
		t.Fatalf("beatCount = %d, want 1", fx.beatCount)
	}
	// Absurd intervals clamp into the musical range.
	fx.noteBeat(now.Add(10 * time.Second))
	if fx.beatPeriod > 1.5 {
		t.Errorf("period escaped clamp: %v", fx.beatPeriod)
	}
	fx.noteBeat(now.Add(10*time.Second + time.Millisecond))
	if fx.beatPeriod < 0.25 {
		t.Errorf("period escaped clamp: %v", fx.beatPeriod)
	}
	// Zero-value tracker self-initializes instead of dividing by zero.
	fx2 := &modeState{}
	fx2.noteBeat(now)
	if fx2.beatPeriod <= 0 || fx2.beatCount != 1 {
		t.Errorf("zero tracker not initialized: %+v", fx2)
	}
}

func TestAccentPaintCyclesHue(t *testing.T) {
	m := &Model{startT: time.Now()}
	now := time.Now()
	m.fx.accentOn = true
	m.fx.beatPeriod = 0.5
	hot := &spectrum{bass: 0.8, overall: 0.7, flux: 0.4}
	hot.agc = 0.45
	c := keyCell{slot: 3, accent: true, accentIx: 0}
	m.fx.noteBeat(now)
	a := m.paintAccent(c, now, hot)
	// One beat later the hue must have stepped (same key, new count).
	m.fx.noteBeat(now.Add(500 * time.Millisecond))
	b := m.paintAccent(c, now.Add(500*time.Millisecond), hot)
	if a == b {
		t.Error("hue must step on each beat")
	}
	// Neighbors must differ: the rainbow spreads across the row.
	c2 := keyCell{slot: 4, accent: true, accentIx: 9}
	d := m.paintAccent(c2, now.Add(500*time.Millisecond), hot)
	if b == d {
		t.Error("accent hue must spread across keys")
	}
	// Silence renders a dim drift, never black strobe or full blast.
	quiet := &spectrum{quietFor: 30}
	q := m.paintAccent(c, now, quiet)
	lum := int(q[0]) + int(q[1]) + int(q[2])
	if lum == 0 || lum > 200 {
		t.Errorf("idle accent lum %d out of dim range", lum)
	}
}

func TestAccentToggleKey(t *testing.T) {
	m := Model{
		bars: make([]float64, numBars), peaks: make([]float64, numBars),
		width: 80, startT: time.Now(),
	}
	m.fx.accentOn = true
	km := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}
	updated, _ := m.Update(km)
	if updated.(Model).fx.accentOn {
		t.Error("'a' must disable the accent overlay")
	}
	updated, _ = updated.(Model).Update(km)
	if !updated.(Model).fx.accentOn {
		t.Error("'a' must re-enable the accent overlay")
	}
}

func TestDeriveParallelIndependent(t *testing.T) {
	raw := make([]int, numBars)
	for i := range raw {
		raw[i] = 60
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var s spectrum
			for j := 0; j < 50; j++ {
				deriveSpectrum(&s, raw, 1.0/60)
			}
		}()
	}
	wg.Wait()
}
