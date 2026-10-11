package viz

import (
	"math"
	"testing"
	"time"
)

func TestDeriveMonoBands(t *testing.T) {
	var s spectrum
	raw := make([]int, numBars)
	raw[7] = 100        // left band 7
	raw[7+numBands] = 0 // right band 7
	for i := 0; i < 600; i++ {
		deriveSpectrum(&s, raw, 1.0/60)
	}
	if math.Abs(s.mono[7]-0.5) > 0.05 {
		t.Errorf("mono[7] = %v, want ~0.5 (left+right averaged)", s.mono[7])
	}
	if s.left < 0.1 {
		t.Errorf("left = %v, want high", s.left)
	}
	if s.right > 0.05 {
		t.Errorf("right = %v, want ~0", s.right)
	}
}

func TestBeatCooldown(t *testing.T) {
	b := beatTracker{cooldown: 120 * time.Millisecond}
	now := time.Now()
	b.ema = 0.05
	if !b.trig(0.5, now) {
		t.Fatal("first onset should trigger")
	}
	if b.trig(0.5, now.Add(50*time.Millisecond)) {
		t.Error("second onset within cooldown should not trigger")
	}
	if !b.trig(0.5, now.Add(200*time.Millisecond)) {
		t.Error("onset after cooldown should trigger")
	}
}

func TestRippleRemoval(t *testing.T) {
	m := &Model{}
	m.fx.ripples = []ripple{{start: time.Now().Add(-2 * time.Second), amp: 0.8}}
	c := keyCell{slot: 0, row: 3, x: 0.47, y: 0.55, px: 0.47 * kbAspect, py: 0.55}
	var s spectrum
	s.bass = 0.3
	now := time.Now()
	m.stepDynamics(1.0/60.0, &s, now)
	m.paintRipple(c, themes[0], now, &s)
	if len(m.fx.ripples) != 0 {
		t.Errorf("expired ripple not removed: %d left", len(m.fx.ripples))
	}
}

func TestRippleRingShape(t *testing.T) {
	m := &Model{}
	m.fx.ripples = []ripple{{start: time.Now().Add(-500 * time.Millisecond), amp: 0.9}}
	var s spectrum
	lum := func(c [3]byte) float64 {
		return float64(c[0]) + float64(c[1]) + float64(c[2])
	}
	onRing := keyCell{slot: 0, row: 3, x: 0.47, y: 0.55, px: 0.47*kbAspect + 0.5*rippleSpeed, py: 0.55}
	far := keyCell{slot: 1, row: 0, x: 0.1, y: 0.0, px: 0.1 * kbAspect, py: 0.0}
	lit := lum(m.paintRipple(onRing, themes[0], time.Now(), &s))
	dark := lum(m.paintRipple(far, themes[0], time.Now(), &s))
	if lit <= dark {
		t.Errorf("ring key lum %v should exceed far key lum %v", lit, dark)
	}
}

func TestBoostCurve(t *testing.T) {
	if boost(0) != 0 {
		t.Error("boost(0) should stay dark")
	}
	if boost(1) != 1 {
		t.Error("boost(1) should stay full")
	}
	if boost(0.25) <= 0.25 {
		t.Errorf("boost(0.25) = %v, want lifted above 0.25", boost(0.25))
	}
	prev := 0.0
	for _, v := range []float64{0.0, 0.1, 0.3, 0.5, 0.8, 1.0} {
		b := boost(v)
		if b < prev {
			t.Errorf("boost not monotonic at %v", v)
		}
		prev = b
	}
}

func TestAspectDistance(t *testing.T) {
	// A key 0.1 right of center must be as far as the aspect suggests:
	// 0.1 * 2.8 = 0.28 physical units.
	dx := (0.47+0.1)*kbAspect - 0.47*kbAspect
	if math.Abs(dx-0.28) > 1e-9 {
		t.Errorf("aspect dx = %v, want 0.28", dx)
	}
}
