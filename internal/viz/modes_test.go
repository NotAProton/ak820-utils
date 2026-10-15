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

func TestTempoConverges140(t *testing.T) {
	b := beatTracker{cooldown: 120 * time.Millisecond}
	now := time.Now()
	sig := kickTrain(9.0, 60.0/140.0, 0.45, 0.45, 0.02, 11)
	for i, e := range sig {
		b.update(e, now.Add(time.Duration(i)*time.Second/60))
	}
	if !b.confident {
		t.Fatal("tempo never became confident on a steady 140bpm train")
	}
	if b.bpm < 130 || b.bpm > 150 {
		t.Errorf("tempo = %.1f bpm, want ~140", b.bpm)
	}
}

// TestSteadySectionKeepsPulsing is the section-start regression: once the
// onset gate adapts to a loud section, the tempo grid must carry the pulse
// through the middle instead of going quiet until the next section.
func TestSteadySectionKeepsPulsing(t *testing.T) {
	b := beatTracker{cooldown: 120 * time.Millisecond}
	now := time.Now()
	sig := kickTrain(10.0, 60.0/140.0, 0.45, 0.45, 0.02, 21)
	fires := 0
	// Middle window: seconds 4-7, long past any startup transient.
	// ~7 kicks live there; the grid must catch most of them.
	for i, e := range sig {
		tt := float64(i) / 60.0
		if b.update(e, now.Add(time.Duration(i)*time.Second/60)) && tt >= 4 && tt < 7 {
			fires++
		}
	}
	if fires < 5 {
		t.Errorf("steady middle fired %d times, want >= 5 of ~7 kicks", fires)
	}
}

func TestPredictionStaysDarkInSilence(t *testing.T) {
	b := beatTracker{cooldown: 120 * time.Millisecond}
	now := time.Now()
	// Prime tempo with 6s of kicks, then cut to near-silence: the grid
	// must not hallucinate beats into the breakdown.
	sig := kickTrain(6.0, 60.0/140.0, 0.45, 0.45, 0.02, 31)
	for i, e := range sig {
		b.update(e, now.Add(time.Duration(i)*time.Second/60))
	}
	if !b.confident {
		t.Fatal("tempo should be confident after 6s of kicks")
	}
	fires := 0
	for i := 0; i < 180; i++ {
		if b.update(0.03, now.Add(time.Duration(360+i)*time.Second/60)) {
			fires++
		}
	}
	if fires != 0 {
		t.Errorf("silence fired %d times on a confident grid, want 0", fires)
	}
}

// TestWeakKicksKeepPulsing covers kicks too small to cross the onset gate:
// the tempo grid bootstraps from flux periodicity alone (phase anchored
// to the strongest flux peak) and carries the section.
func TestWeakKicksKeepPulsing(t *testing.T) {
	b := beatTracker{cooldown: 120 * time.Millisecond}
	now := time.Now()
	sig := kickTrain(10.0, 60.0/140.0, 0.6, 0.2, 0.02, 41)
	fires := 0
	for i, e := range sig {
		tt := float64(i) / 60.0
		if b.update(e, now.Add(time.Duration(i)*time.Second/60)) && tt >= 4 && tt < 7 {
			fires++
		}
	}
	if !b.confident {
		t.Fatal("tempo should lock onto periodic weak kicks")
	}
	if fires < 5 {
		t.Errorf("weak-kick middle fired %d times, want >= 5 of ~7 kicks", fires)
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

// bandTrain synthesizes multi-band frames at 60fps: a constant sub floor
// with periodic punch kicks, optionally scaled to a quiet volume.
func bandTrain(dur, period, sub, punchKick, snare, scale float64) []beatInput {
	n := int(dur * 60)
	out := make([]beatInput, n)
	for i := range out {
		tt := float64(i) / 60
		punch := 0.30 * scale
		if since := tt - float64(int(tt/period))*period; since < 0.06 {
			punch = punchKick * scale
		}
		low := sub * scale
		sn := snare * scale
		out[i] = beatInput{Low: low, Punch: punch, Snare: sn,
			Combined: 0.55*low + 0.30*punch + 0.15*sn}
	}
	return out
}

func countFires(b *beatTracker, sig []beatInput, now time.Time, from, to float64) int {
	fires := 0
	for i, in := range sig {
		tt := float64(i) / 60
		if _, fired := b.updateInput(in, now.Add(time.Duration(i)*time.Second/60)); fired && tt >= from && tt < to {
			fires++
		}
	}
	return fires
}

func TestBeatSaturated808WithKick(t *testing.T) {
	b := beatTracker{cooldown: DefaultBeatCooldown}
	now := time.Now()
	// Sustained 0.85 sub floor; kicks live only in upper-bass punch.
	sig := bandTrain(6, 60.0/140.0, 0.85, 1.0, 0.1, 1.0)
	if fires := countFires(&b, sig, now, 2, 6); fires < 6 {
		t.Errorf("saturated 808 fired %d times in 4s, want >= 6 of ~9 kicks", fires)
	}
	if ev := b.lastEvent; ev.Strength <= 0 {
		t.Errorf("beat strength not recorded: %+v", ev)
	}
}

func TestBeatQuietVolumeTrack(t *testing.T) {
	b := beatTracker{cooldown: DefaultBeatCooldown}
	now := time.Now()
	// Same train at 20% amplitude must still fire and lock tempo.
	sig := bandTrain(9, 60.0/140.0, 0.45, 0.9, 0.1, 0.2)
	fires := countFires(&b, sig, now, 4, 7)
	if fires < 5 {
		t.Errorf("quiet track fired %d times in middle, want >= 5", fires)
	}
	if !b.confident || b.bpm < 130 || b.bpm > 150 {
		t.Errorf("quiet track tempo confident=%v bpm=%.1f, want ~140 locked", b.confident, b.bpm)
	}
}

func TestBeatSyncopationPreservesGrid(t *testing.T) {
	b := beatTracker{cooldown: DefaultBeatCooldown}
	now := time.Now()
	sig := bandTrain(9, 60.0/140.0, 0.45, 0.9, 0.1, 1.0)
	for i, in := range sig {
		b.updateInput(in, now.Add(time.Duration(i)*time.Second/60))
	}
	if !b.confident {
		t.Fatal("tempo should be confident before syncopation")
	}
	before := b.nextBeat
	period := time.Duration(60 / b.bpm * float64(time.Second))
	// One loud off-beat hit halfway between grid points.
	offTime := before.Add(period / 2)
	hit := beatInput{Low: 1, Punch: 1, Snare: 0.2, Combined: 0.9}
	if _, fired := b.updateInput(hit, offTime); !fired {
		t.Fatal("syncopated hit should still fire visually")
	}
	// Grid phase must survive: nextBeat stays within 60ms of a grid point
	// derived from the pre-hit grid.
	diff := b.nextBeat.Sub(before)
	rem := diff % period
	if rem < 0 {
		rem += period
	}
	if rem > 60*time.Millisecond && period-rem > 60*time.Millisecond {
		t.Errorf("grid derailed by syncopation: nextBeat shifted %v off grid (period %v)", rem, period)
	}
}

func TestBeatInputFromRaw(t *testing.T) {
	raw := make([]int, numBars)
	raw[0], raw[1] = 100, 100
	raw[0+numBands], raw[1+numBands] = 100, 100
	raw[4], raw[5], raw[6] = 90, 90, 90
	in := beatInputFromRaw(raw)
	if in.Low != 1 {
		t.Errorf("low = %v, want 1", in.Low)
	}
	if in.Snare < 0.4 {
		t.Errorf("snare = %v, want stereo-averaged ~0.45", in.Snare)
	}
	if got := beatInputFromRaw([]int{1, 2}); got != (beatInput{}) {
		t.Errorf("short frame = %+v, want zero", got)
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
