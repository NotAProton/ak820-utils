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
	// Prime the window with quiet so the detector is armed.
	for i := 0; i < minBeatContext; i++ {
		if b.update(0.05, now.Add(time.Duration(i)*time.Second/60)) {
			t.Fatal("quiet context must not fire")
		}
	}
	if !b.update(0.6, now.Add(time.Second)) {
		t.Fatal("first onset should trigger")
	}
	if b.update(0.6, now.Add(time.Second+50*time.Millisecond)) {
		t.Error("second onset within cooldown should not trigger")
	}
	if !b.update(0.6, now.Add(time.Second+200*time.Millisecond)) {
		t.Error("onset after cooldown should trigger")
	}
}

func TestBeatChatterSuppressed(t *testing.T) {
	b := beatTracker{cooldown: 120 * time.Millisecond}
	now := time.Now()
	// Prime with quiet, then step to a loud sustained hover. The window
	// turnover may flash a few times, but the stream must settle: no
	// endless chatter, and steady state stays dark.
	for i := 0; i < minBeatContext; i++ {
		b.update(0.05, now.Add(time.Duration(i)*time.Second/60))
	}
	fires := 0
	steady := 0
	for i := 0; i < 90; i++ {
		lvl := 0.5
		if i%2 == 0 {
			lvl = 0.52
		}
		if b.update(lvl, now.Add(time.Duration(minBeatContext+i)*time.Second/60)) {
			fires++
			if i >= 60 {
				steady++
			}
		}
	}
	if fires > 8 {
		t.Errorf("hovering signal fired %d times, want a bounded burst", fires)
	}
	if steady != 0 {
		t.Errorf("steady hover fired %d times, want 0", steady)
	}
}

// kickTrain synthesizes a Tokyo-Drift-like bass signal: a heavy sustained
// floor with a kick spike every period, plus low noise. Kicks decay fast
// (~60ms) like a real transient.
func kickTrain(dur, period, floor, kick float64, noise float64, seed int64) []float64 {
	dt := 1.0 / 60.0
	n := int(dur / dt)
	out := make([]float64, n)
	r := uint64(seed)
	next := func() float64 {
		r = r*6364136223846793005 + 1442695040888963407
		return float64(r>>33) / (1 << 31)
	}
	for i := range out {
		tt := float64(i) * dt
		v := floor + (next()-0.5)*2*noise
		if since := tt - float64(int(tt/period))*period; since < 0.3 {
			v += kick * float64(int(1000*(1-since/0.3))) / 1000
		}
		if v < 0 {
			v = 0
		}
		if v > 1 {
			v = 1
		}
		out[i] = v
	}
	return out
}

func TestBeatKickTrain140BPM(t *testing.T) {
	b := beatTracker{cooldown: 120 * time.Millisecond}
	now := time.Now()
	// 140 BPM four-on-the-floor over a heavy 0.45 floor: the case a
	// fixed-ratio detector misses once its average adapts.
	sig := kickTrain(4.0, 60.0/140.0, 0.45, 0.45, 0.02, 7)
	var fires []int
	for i, e := range sig {
		if b.update(e, now.Add(time.Duration(i)*time.Second/60)) {
			fires = append(fires, i)
		}
	}
	if len(fires) < 7 || len(fires) > 12 {
		t.Fatalf("140bpm train fired %d times, want ~9 (one per kick)", len(fires))
	}
	// Every fire must land within 100ms after a kick attack.
	period := 60.0 / 140.0
	for _, f := range fires {
		tt := float64(f) / 60.0
		if since := tt - float64(int(tt/period))*period; since > 0.1 {
			t.Errorf("fire at %.3fs is %.3fs past the kick", tt, since)
		}
	}
}

func TestBeatFloorOnlySilent(t *testing.T) {
	b := beatTracker{cooldown: 120 * time.Millisecond}
	now := time.Now()
	fires := 0
	// Heavy floor, no kicks: the detector must stay quiet.
	for i, e := range kickTrain(3.0, 60.0/140.0, 0.5, 0.0, 0.02, 3) {
		if b.update(e, now.Add(time.Duration(i)*time.Second/60)) {
			fires++
		}
	}
	if fires != 0 {
		t.Errorf("floor-only signal fired %d times, want 0", fires)
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
