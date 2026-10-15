package viz

import (
	"math"
	"time"
)

// Geometric audio modes. Each mode maps the shared spectrum onto the
// physical keyboard geometry; M/Tab cycles between them live.
const (
	modeRipple = iota
	modeBars
	modeBloom
	modePlasma
	modeWings
	modeStrobe
	numModes
)

var modeNames = [...]string{"ripple", "bars", "bloom", "plasma", "wings", "strobe"}

// kbAspect is the keyboard's physical width-to-height ratio; multiplying
// normalized x by it keeps radial effects circular on the board.
const kbAspect = 2.8

// numBands is the mono band count: cava outputs 9 left bars then 9 right
// bars when channels = stereo, so index i pairs with i+9.
const numBands = numBars / 2

// key layout anchors, in normalized coordinates.
var (
	rippleX, rippleY = 0.47, 0.55 // G/H/Space
	bloomLX, bloomLY = 0.35, 0.50 // E/D
	bloomRX, bloomRY = 0.72, 0.50 // I/K
)

// spectrum is the per-frame audio state shared by every mode.
type spectrum struct {
	mono     [numBands]float64 // mono bands 0..1, smoothed
	peaks    [numBands]float64 // falling peak-hold per band
	onset    [numBands]float64 // positive-only flux per band, decaying
	flux     float64           // overall spectral flux 0..1, smoothed
	agc      float64           // slow follower of overall level for auto-gain
	quietFor float64           // seconds since last audible frame
	left     float64
	right    float64
	bass     float64
	mid      float64
	treble   float64
	overall  float64
}

// deriveSpectrum folds one raw cava frame (0..100 per bar) into the
// smoothed spectrum. First half of raw is the left channel, second the
// right; mono band i averages raw[i] with raw[i+numBands].
func deriveSpectrum(s *spectrum, raw []int, dt float64) {
	if len(raw) < 2*numBands {
		return
	}
	if dt < 0 {
		dt = 1.0 / 60.0
	}
	if dt > 0.5 {
		dt = 0.5
	}
	attack := minF(1.0, dt*25)
	fall := minF(1.0, dt*18)

	clampBar := func(v int) float64 {
		if v < 0 {
			v = 0
		}
		if v > 100 {
			v = 100
		}
		return float64(v) / 100
	}
	var leftSum, rightSum float64
	var fluxSum float64
	for i := 0; i < numBands; i++ {
		l := clampBar(raw[i])
		r := clampBar(raw[i+numBands])
		leftSum += l
		rightSum += r
		v := (l + r) / 2
		prev := s.mono[i]
		if v > s.mono[i] {
			s.mono[i] += (v - s.mono[i]) * attack
		} else {
			s.mono[i] += (v - s.mono[i]) * fall
		}
		if rise := s.mono[i] - prev; rise > 0 {
			s.onset[i] += rise * 2
			if s.onset[i] > 1 {
				s.onset[i] = 1
			}
			fluxSum += rise
		} else {
			s.onset[i] -= dt * 3
			if s.onset[i] < 0 {
				s.onset[i] = 0
			}
		}
		if s.mono[i] > s.peaks[i] {
			s.peaks[i] = s.mono[i]
		} else {
			s.peaks[i] -= 0.45 * dt
			if s.peaks[i] < 0 {
				s.peaks[i] = 0
			}
		}
	}
	s.left = leftSum / numBands
	s.right = rightSum / numBands

	bass := (s.mono[0] + s.mono[1] + s.mono[2]) / 3
	mid := (s.mono[3] + s.mono[4] + s.mono[5]) / 3
	treble := (s.mono[6] + s.mono[7] + s.mono[8]) / 3
	s.bass = smooth(s.bass, bass, dt, 30, 10)
	s.mid = smooth(s.mid, mid, dt, 20, 8)
	s.treble = smooth(s.treble, treble, dt, 35, 22)
	s.overall = (s.bass + s.mid + s.treble) / 3
	s.flux = smooth(s.flux, minF(1, fluxSum*4), dt, 30, 6)
	s.agc = smooth(s.agc, s.overall, dt, 0.8, 0.25)
	if s.agc < 0.08 {
		s.agc = 0.08
	}
	if s.overall > 0.03 {
		s.quietFor = 0
	} else {
		s.quietFor += dt
		if s.quietFor > 30 {
			s.quietFor = 30
		}
	}
}

func smooth(cur, target, dt, attackRate, fallRate float64) float64 {
	rate := fallRate
	if target > cur {
		rate = attackRate
	}
	return cur + (target-cur)*minF(1, dt*rate)
}

// beatWindow is the onset context in frames (~0.7s at 60fps cava output:
// long enough to span a full beat at dance tempos, short enough to track
// section changes without a long hangover).
const beatWindow = 43

// Tempo layer (Ellis-lite): the onset envelope is autocorrelated over a
// multi-second window to estimate the period, and beats are predicted on
// that grid so steady sections keep pulsing instead of going quiet once
// the onset gate adapts. Lags cover 60-200 BPM at 60fps.
const (
	tempoWindow    = 240 // ~4s of onset envelope
	tempoWarmup    = 120 // ~2s: first estimate before the window fills
	tempoRecompute = 30  // frames between tempo estimates (~0.5s)
	minTempoLag    = 18  // 200 BPM
	maxTempoLag    = 60  // 60 BPM
)

// predictFloor is the normalized energy a predicted (non-onset) beat
// needs: tempo may say "now", but silence stays dark.
const predictFloor = 0.15

// noiseGate is the raw combined energy below which input is silence,
// regardless of peak normalization.
const noiseGate = 0.04

// beatKind classifies a fired beat for visual choreography.
type beatKind int

const (
	kindKick beatKind = iota
	kindSnare
)

// beatInput is one frame of instantaneous, unsmoothed sub-band energy.
type beatInput struct {
	Low      float64 // mono bands 0-1: sub / low kick
	Punch    float64 // mono bands 2-3: upper-bass punch
	Snare    float64 // mono bands 4-6: snare / transient crack
	Combined float64 // weighted mix used for level and tempo
}

// beatInputFromRaw folds a raw cava frame (stereo: left bars then right
// bars) into mono sub-bands. Short frames yield a zero input.
func beatInputFromRaw(raw []int) beatInput {
	if len(raw) < 2*numBands {
		return beatInput{}
	}
	mono := func(i int) float64 {
		v := (clampBarInt(raw[i]) + clampBarInt(raw[i+numBands])) / 2
		return v
	}
	low := (mono(0) + mono(1)) / 2
	punch := (mono(2) + mono(3)) / 2
	snare := (mono(4) + mono(5) + mono(6)) / 3
	return beatInput{Low: low, Punch: punch, Snare: snare,
		Combined: 0.55*low + 0.30*punch + 0.15*snare}
}

func clampBarInt(v int) float64 {
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	return float64(v) / 100
}

// beatEvent describes a fired beat: strength scales visual impact, kind
// selects choreography, and drop marks a high-energy hit after a dip.
type beatEvent struct {
	Strength  float64
	Kind      beatKind
	Drop      bool
	Predicted bool
}

// minBeatContext is the frames needed before the detector arms (~200ms),
// so startup transients never fire on an empty window.
const minBeatContext = 12

// beatTracker fires on multi-band onsets. Each sub-band is normalized by
// a fast-attack / slow-release peak follower, so quiet and loud sources
// share one dynamic range. A beat fires on either an adaptive level cross
// or a positive-flux spike above the flux window's mean + k*std, which
// catches kicks punching through a saturated sub-bass floor where the
// level barely moves. Dense sections raise the level variance, which
// lowers the level bar instead of raising it.
type beatTracker struct {
	hist     [beatWindow]float64 // ring of normalized combined energies
	head     int
	filled   int
	last     time.Time
	cooldown time.Duration

	fluxHist   [beatWindow]float64 // ring of weighted onset flux
	fluxHeadW  int
	fluxFilled int

	peakLow, peakPunch, peakSnare, peakComb float64
	prevLow, prevPunch, prevSnare           float64
	prevInit                                bool

	fluxRing  [tempoWindow]float64 // onset strength envelope ring
	fluxHead  int
	fluxFull  bool
	sinceTpo  int
	bpm       float64 // estimated tempo, 0 when unknown
	confident bool
	nextBeat  time.Time // predicted beat phase on the tempo grid

	lastEvent   beatEvent
	dipFrames   int
	wasDipped   bool
	offCount    int
	offResidual float64 // seconds, residual of pending off-grid phase
	hasOff      bool
}

// DefaultBeatCooldown is the production backstop between beat fires.
const DefaultBeatCooldown = 120 * time.Millisecond

// update folds one scalar energy sample as an all-bands input. It is
// kept for scalar callers and tests; production uses updateInput.
func (b *beatTracker) update(e float64, now time.Time) bool {
	_, ok := b.updateInput(beatInput{Low: e, Punch: e, Snare: e, Combined: e}, now)
	return ok
}

// peakStep advances a fast-attack / slow-release peak follower.
func peakStep(peak, v float64) float64 {
	if v >= peak {
		return v
	}
	return peak + (v-peak)*0.005
}

func peakScale(peak float64) float64 {
	if peak < 0.12 {
		return 0.12
	}
	return peak
}

// updateInput folds one multi-band frame and reports the fired event.
func (b *beatTracker) updateInput(in beatInput, now time.Time) (beatEvent, bool) {
	clampU := func(v float64) float64 {
		if v < 0 {
			return 0
		}
		if v > 1 {
			return 1
		}
		return v
	}
	in.Low, in.Punch, in.Snare, in.Combined = clampU(in.Low), clampU(in.Punch), clampU(in.Snare), clampU(in.Combined)
	silent := in.Combined < noiseGate
	// Flux uses the pre-update peak scale so a jump from a quiet floor
	// spikes even though the clamped level saturates.
	var lowF, punchF, snareF float64
	if b.prevInit && !silent {
		lowF = clampU((in.Low - b.prevLow) / peakScale(b.peakLow))
		if in.Low <= b.prevLow {
			lowF = 0
		}
		punchF = clampU((in.Punch - b.prevPunch) / peakScale(b.peakPunch))
		if in.Punch <= b.prevPunch {
			punchF = 0
		}
		snareF = clampU((in.Snare - b.prevSnare) / peakScale(b.peakSnare))
		if in.Snare <= b.prevSnare {
			snareF = 0
		}
	}
	flux := clampU(0.55*lowF + 0.30*punchF + 0.15*snareF)
	b.peakLow = peakStep(b.peakLow, in.Low)
	b.peakPunch = peakStep(b.peakPunch, in.Punch)
	b.peakSnare = peakStep(b.peakSnare, in.Snare)
	b.peakComb = peakStep(b.peakComb, in.Combined)
	b.prevLow, b.prevPunch, b.prevSnare = in.Low, in.Punch, in.Snare
	b.prevInit = true

	e := clampU(in.Combined / peakScale(b.peakComb))
	if silent {
		e = 0
	}
	b.hist[b.head%beatWindow] = e
	b.head++
	if b.filled < beatWindow {
		b.filled++
	}
	b.fluxHist[b.fluxHeadW%beatWindow] = flux
	b.fluxHeadW++
	if b.fluxFilled < beatWindow {
		b.fluxFilled++
	}
	// Onset envelope for tempo: weighted flux, already peak-normalized.
	b.fluxRing[b.fluxHead%tempoWindow] = flux
	b.fluxHead++
	if b.fluxHead >= tempoWindow {
		b.fluxFull = true
	}
	// Drop bookkeeping: a dip is 20+ frames under 30% normalized energy.
	if e < 0.3 {
		b.dipFrames++
		if b.dipFrames >= 20 {
			b.wasDipped = true
		}
	} else {
		b.dipFrames = 0
	}
	b.sinceTpo++
	if b.sinceTpo >= tempoRecompute && b.fluxHead >= tempoWarmup {
		b.sinceTpo = 0
		b.estimateTempo(now)
	}
	if b.filled < minBeatContext {
		return beatEvent{}, false
	}
	var sum, sqSum float64
	for i := 0; i < b.filled; i++ {
		sum += b.hist[i]
		sqSum += b.hist[i] * b.hist[i]
	}
	mean := sum / float64(b.filled)
	variance := sqSum/float64(b.filled) - mean*mean
	if variance < 0 {
		variance = 0
	}
	c := 1.45 - 6*variance
	if c < 1.15 {
		c = 1.15
	}
	if c > 1.5 {
		c = 1.5
	}
	var fSum, fSq float64
	for i := 0; i < b.fluxFilled; i++ {
		fSum += b.fluxHist[i]
		fSq += b.fluxHist[i] * b.fluxHist[i]
	}
	fMean := fSum / float64(b.fluxFilled)
	fVar := fSq/float64(b.fluxFilled) - fMean*fMean
	if fVar < 0 {
		fVar = 0
	}
	fStd := math.Sqrt(fVar)

	kind := kindKick
	if snareF > lowF && snareF > punchF && snareF > 0.08 {
		kind = kindSnare
	}
	strength := clamp01(0.4*e + 0.6*clamp01(flux*1.6))
	drop := b.wasDipped && (e > 0.7 || flux > 0.25)
	levelFire := e > 0.12 && e > mean*c
	fluxFire := flux > 0.10 && flux > fMean+1.2*fStd
	if !silent && (levelFire || fluxFire) && now.Sub(b.last) > b.cooldown {
		ev := beatEvent{Strength: strength, Kind: kind, Drop: drop}
		b.fireOnset(now, ev)
		b.wasDipped = false
		return ev, true
	}
	// Tempo prediction carries steady sections.
	if b.confident && b.bpm > 0 && !b.nextBeat.IsZero() && now.Sub(b.last) > b.cooldown {
		period := time.Duration(60 / b.bpm * float64(time.Second))
		if now.After(b.nextBeat.Add(period)) {
			b.nextBeat = now.Add(period)
			return beatEvent{}, false
		}
		if !now.Before(b.nextBeat) && e > predictFloor && !silent {
			b.nextBeat = b.nextBeat.Add(period)
			ev := beatEvent{Strength: clamp01(0.3 + 0.5*e), Kind: kindKick, Drop: drop, Predicted: true}
			b.firePredicted(now, ev)
			b.wasDipped = false
			return ev, true
		}
	}
	return beatEvent{}, false
}

// estimateTempo runs an Ellis-style tempo estimate: autocorrelate the
// onset envelope over the tempo window and pick the lag in the musical
// range with continuity and center priors. Octave ambiguity is harmless
// here: half- and double-time both land on the grid for lights.
func (b *beatTracker) estimateTempo(now time.Time) {
	// Use whatever envelope is available: warmup at 2s, full 4s later.
	n := b.fluxHead
	if n > tempoWindow {
		n = tempoWindow
	}
	if n < tempoWarmup {
		return
	}
	var xs [tempoWindow]float64
	var mean float64
	for i := 0; i < n; i++ {
		xs[i] = b.fluxRing[(b.fluxHead-n+i+tempoWindow*2)%tempoWindow]
		mean += xs[i]
	}
	mean /= float64(n)
	for i := 0; i < n; i++ {
		xs[i] -= mean
	}
	var total float64
	for _, v := range xs {
		total += v * v
	}
	if total <= 0 {
		b.confident = false
		return
	}
	curLag := 0
	if b.bpm > 0 {
		curLag = int(3600 / b.bpm) // period frames at 60fps
	}
	bestLag, bestScore, sumScore := 0, -1.0, 0.0
	var raw [maxTempoLag + 1]float64
	for lag := minTempoLag; lag <= maxTempoLag; lag++ {
		var acc float64
		for i := lag; i < n; i++ {
			acc += xs[i] * xs[i-lag]
		}
		acc /= float64(n - lag)
		raw[lag] = acc
		sumScore += acc
		if score := acc * priorWeight(lag, curLag); score > bestScore {
			bestScore, bestLag = score, lag
		}
	}
	// Confidence: a real pulse stands clearly above the lag average.
	// Deliberately not gated on onset fires: weak kicks that never cross
	// the energy gate still imprint periodicity on the flux envelope, and
	// requiring fires would leave the grid dead exactly when steady
	// sections need it. Breakdown discipline comes from the prediction
	// energy floor, not from confidence.
	if bestScore <= 0 {
		b.confident = false
		return
	}
	peakRatio := bestScore / (sumScore / float64(maxTempoLag-minTempoLag+1))
	if peakRatio < 1.4 {
		b.confident = false
		return
	}
	was := b.confident
	b.confident = true
	// Parabolic interpolation around the autocorrelation peak: the lag
	// grid is coarse (140 BPM falls between bins), and sub-bin accuracy
	// keeps the predicted grid from drifting over long sections.
	lagFloat := float64(bestLag)
	if bestLag > minTempoLag && bestLag < maxTempoLag {
		a, bb, cc := raw[bestLag-1], raw[bestLag], raw[bestLag+1]
		if denom := a - 2*bb + cc; denom < 0 {
			if off := 0.5 * (a - cc) / denom; off > -1 && off < 1 {
				lagFloat += off
			}
		}
	}
	newBPM := 3600 / lagFloat
	if b.bpm <= 0 {
		b.bpm = newBPM
	} else if absF(newBPM-b.bpm)/b.bpm > 0.2 {
		// Genuine tempo change: jump and re-base the grid.
		b.bpm = newBPM
		if !b.last.IsZero() {
			b.nextBeat = b.last.Add(time.Duration(60 / b.bpm * float64(time.Second)))
		}
	} else {
		b.bpm += (newBPM - b.bpm) * 0.15
	}
	// Phase bootstrap: without any onset fire there is no phase anchor,
	// so take it from the strongest recent flux peak instead. Without
	// this, confident tempo with sub-gate kicks would still never flash.
	if !was || b.nextBeat.IsZero() || now.After(b.nextBeat.Add(2*time.Duration(60/b.bpm*float64(time.Second)))) {
		b.anchorPhase(now)
	}
}

// anchorPhase sets the predicted grid from the strongest flux peak in the
// last two periods, projected forward to the first beat at or after now.
func (b *beatTracker) anchorPhase(now time.Time) {
	period := 60 / b.bpm
	span := int(2 * period * 60)
	if span > tempoWindow {
		span = tempoWindow
	}
	if span < 2 {
		return
	}
	best, bestV := 0, -1.0
	for i := 0; i < span; i++ {
		if v := b.fluxRing[(b.fluxHead-1-i+tempoWindow*2)%tempoWindow]; v > bestV {
			best, bestV = i, v
		}
	}
	if bestV <= 0 {
		return
	}
	peakAgo := time.Duration(float64(best) / 60 * float64(time.Second))
	peakTime := now.Add(-peakAgo)
	k := int(peakAgo.Seconds()/period) + 1
	b.nextBeat = peakTime.Add(time.Duration(float64(k) * period * float64(time.Second)))
}

// priorWeight holds the continuity and center priors for a lag: trust
// the current tempo unless the evidence clearly moved, with a mild pull
// toward dance tempos.
func priorWeight(lag, curLag int) float64 {
	w := 1.0
	if curLag > 0 {
		if d := absI(lag - curLag); d < 6 {
			w *= 1 + 0.4*(1-float64(d)/6)
		}
	}
	if bpm := 3600 / float64(lag); bpm > 110 && bpm < 150 {
		w *= 1.1
	}
	return w
}

func absI(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// fireOnset records an onset beat. Near the predicted grid it pulls the
// phase softly toward the hit; far off-grid it fires visually without
// moving the grid unless consecutive hits agree on a new phase.
func (b *beatTracker) fireOnset(now time.Time, ev beatEvent) {
	b.last = now
	b.lastEvent = ev
	if b.bpm <= 0 {
		return
	}
	period := 60 / b.bpm
	periodDur := time.Duration(period * float64(time.Second))
	if b.nextBeat.IsZero() || !b.confident {
		b.nextBeat = now.Add(periodDur)
		return
	}
	// Residual to the nearest grid point, in seconds (-period/2..period/2).
	elapsed := now.Sub(b.nextBeat).Seconds()
	k := math.Round(elapsed / period)
	residual := elapsed - k*period
	if absF(residual) <= 0.18*period {
		// Soft pull: shift the whole grid by half the residual.
		shift := time.Duration(residual * 0.5 * float64(time.Second))
		b.nextBeat = b.nextBeat.Add(shift).Add(time.Duration((k + 1) * period * float64(time.Second)))
		b.offCount, b.hasOff = 0, false
		return
	}
	// Off-grid: count consecutive hits sharing a residual. Three in a
	// row rebase; a lone syncopation leaves the grid alone.
	if b.hasOff && absF(residual-b.offResidual) < 0.04 {
		b.offCount++
	} else {
		b.offCount, b.offResidual, b.hasOff = 1, residual, true
	}
	if b.offCount >= 3 {
		b.nextBeat = now.Add(periodDur)
		b.offCount, b.hasOff = 0, false
		return
	}
	// Keep the grid in the future without firing catch-up beats.
	for !b.nextBeat.After(now) {
		b.nextBeat = b.nextBeat.Add(periodDur)
	}
}

// firePredicted records a tempo-grid beat; the caller already advanced
// nextBeat, so phase is untouched here.
func (b *beatTracker) firePredicted(now time.Time, ev beatEvent) {
	b.last = now
	b.lastEvent = ev
}

// spring follows a target with fast attack and slower release.
type spring struct{ v float64 }

func (s *spring) step(target, dt float64) {
	rate := 5.0
	if target > s.v {
		rate = 14
	}
	s.v += (target - s.v) * minF(1, dt*rate)
}

// ripple is one expanding shockwave.
type ripple struct {
	start time.Time
	amp   float64
}

const (
	rippleSpeed  = 2.4  // physical units per second
	rippleLife   = 1.3  // seconds
	rippleSpread = 0.10 // ring width (gaussian sigma)
)

// modeState carries all per-mode dynamics across frames.
type modeState struct {
	beats   beatTracker
	ripples []ripple
	wing    spring
	bloomL  spring
	bloomR  spring
	frame   uint32

	beatCount    uint64
	lastBeat     time.Time
	beatPeriod   float64 // seconds between beats, EMA
	lastStrength float64
	lastKind     beatKind
	lastDrop     bool
}

// noteBeat records a beat with default impact. The
// period EMA is clamped to a musical range so stray triggers cannot
// freeze or strobe the beat-driven effects.
func (fx *modeState) noteBeat(now time.Time) {
	fx.noteBeatEvent(now, beatEvent{Strength: 0.7, Kind: kindKick})
}

// noteBeatEvent records a fired beat and its impact for beat-driven
// effects (strobe, ripples, TUI indicator).
func (fx *modeState) noteBeatEvent(now time.Time, ev beatEvent) {
	if fx.beatPeriod <= 0 {
		fx.beatPeriod = 0.5
	}
	if !fx.lastBeat.IsZero() {
		if p := now.Sub(fx.lastBeat).Seconds(); p > 0 {
			if p < 0.25 {
				p = 0.25
			}
			if p > 1.5 {
				p = 1.5
			}
			fx.beatPeriod += (p - fx.beatPeriod) * 0.3
		}
	}
	fx.lastBeat = now
	fx.beatCount++
	fx.lastStrength = clamp01(ev.Strength)
	fx.lastKind = ev.Kind
	fx.lastDrop = ev.Drop
}

// flicker returns a deterministic 0..1 sparkle for a slot. It interpolates
// between hashes refreshed every 8 frames so treble perimeters shimmer
// instead of strobing at full frame rate.
func (fx *modeState) flicker(slot int) float64 {
	epoch := fx.frame / 8
	t := float64(fx.frame%8) / 8
	return hash01(slot, epoch)*(1-t) + hash01(slot, epoch+1)*t
}

func hash01(slot int, epoch uint32) float64 {
	h := uint32(slot)*2654435761 ^ epoch*40503
	h ^= h >> 13
	h *= 1274126177
	h ^= h >> 16
	return float64(h&0xffff) / 65535
}

// twinkle is a temporally coherent shimmer in 0..1: each slot has a fixed
// phase offset, and the shared treble level sets how many slots are awake.
// now is seconds since the model start.
func (fx *modeState) twinkle(slot int, now float64, level float64) float64 {
	phase := float64(hash01(slot, 7)) * 6.28318
	w := 0.5 + 0.5*math.Sin(now*2.2+phase)
	gate := clamp01((level - float64(hash01(slot, 13))*0.7) * 3)
	return w * gate
}

// gain lifts quiet listening levels toward full scale using the slow AGC
// follower. Silence stays dark; typical volume renders visibly.
func (sp *spectrum) gain() float64 {
	g := 1.0
	if sp.agc > 0.01 {
		g = 0.45 / sp.agc
		if g < 1 {
			g = 1
		}
		if g > 2.5 {
			g = 2.5
		}
	}
	return g
}

// idleMix blends an effect intensity toward a slow starfield drift when the
// input has been quiet for over two seconds, so the board never sits black
// or frozen between tracks.
func idleMix(sp *spectrum, I float64, slot int, now float64) float64 {
	if sp.quietFor < 2 {
		return I
	}
	k := (sp.quietFor - 2) / 3
	if k > 1 {
		k = 1
	}
	drift := 0.10 + 0.10*(0.5+0.5*math.Sin(now*0.7+float64(hash01(slot, 29))*6.28318))
	return I*(1-k) + drift*k
}

// hsvRGB converts hue/saturation/value in 0..1 to 8-bit RGB.
func hsvRGB(h, s, v float64) (byte, byte, byte) {
	if s <= 0 {
		c := byte(clamp01(v) * 255)
		return c, c, c
	}
	h -= math.Floor(h)
	i := int(h * 6)
	f := h*6 - float64(i)
	p := v * (1 - s)
	q := v * (1 - s*f)
	t := v * (1 - s*(1-f))
	var r, g, b float64
	switch i % 6 {
	case 0:
		r, g, b = v, t, p
	case 1:
		r, g, b = q, v, p
	case 2:
		r, g, b = p, v, t
	case 3:
		r, g, b = p, q, v
	case 4:
		r, g, b = t, p, v
	default:
		r, g, b = v, p, q
	}
	return byte(clamp01(r) * 255), byte(clamp01(g) * 255), byte(clamp01(b) * 255)
}

// flowField is a smooth pseudo-noise surface in -1..1 built from layered
// sines with irrational-ish frequencies, so plasma drifts without tiling.
func flowField(px, py, tt float64) float64 {
	return (math.Sin(6.28318*(px*0.45+tt*0.11)+1.7*math.Sin(6.28318*(py*0.35-tt*0.07))) +
		0.6*math.Sin(6.28318*(px*1.15-py*0.8+tt*0.16))) / 1.6
}

// boost lifts mid-level intensities so the board is visibly lit at
// typical listening volume; 0 stays dark and 1 stays full.
func boost(i float64) float64 {
	if i < 0.01 {
		return 0
	}
	if i > 1 {
		i = 1
	}
	return math.Pow(i, 0.62)
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// paintBars is the straight vertical equalizer: band from x, lit by row
// height, peak markers on the falling edge. It is the only literal EQ
// mode; every other mode is spatial or flowing.
func (m *Model) paintBars(c keyCell, t theme, sp *spectrum) [3]byte {
	band := int(c.x * numBands)
	if band >= numBands {
		band = numBands - 1
	}
	if band < 0 {
		band = 0
	}
	g := sp.gain()
	lvl := minF(1, sp.mono[band]*g+sp.onset[band]*0.3)
	pk := minF(1, sp.peaks[band]*g)
	height := 1 - c.y
	tt := float64(m.fx.frame) / 60.0
	switch {
	case lvl > 0.01 && height <= lvl:
		r, g2, b := gradRGB(t.keyGrad, boost(lvl))
		return [3]byte{r, g2, b}
	case math.Abs(height-pk) < 0.14 && pk > 0.15:
		return t.peakRGB
	default:
		I := idleMix(sp, 0, c.slot, tt)
		r, g2, b := gradRGB(t.keyGrad, boost(I))
		if I <= 0.001 {
			return ambient
		}
		return [3]byte{r, g2, b}
	}
}

// paintRipple renders concentric shockwaves from the center keys with
// treble twinkle around the perimeter. The floor is a bass-driven center
// glow; rings add on top, and edge keys shimmer coherently instead of
// hashing white noise.
func (m *Model) paintRipple(c keyCell, t theme, now time.Time, sp *spectrum) [3]byte {
	cx, cy := rippleX*kbAspect, rippleY
	dx, dy := c.px-cx, c.py-cy
	d := math.Sqrt(dx*dx + dy*dy)
	tt := now.Sub(m.startT).Seconds()

	var I float64
	for _, rp := range m.fx.ripples {
		age := now.Sub(rp.start).Seconds()
		if age < 0 || age >= rippleLife {
			continue
		}
		r := age * rippleSpeed
		off := d - r
		w := rp.amp * math.Exp(-(off*off)/(2*rippleSpread*rippleSpread)) * math.Exp(-age*1.6)
		if w > I {
			I = w
		}
	}

	g := sp.gain()
	base := 0.05 + 0.35*minF(1, sp.bass*g)*math.Exp(-(dx*dx+dy*dy)/0.45)
	I += base
	if c.row == 0 || c.row == numRows-1 || c.x < 0.10 || c.x > 0.90 {
		I += minF(1, sp.treble*g) * 0.6 * m.fx.twinkle(c.slot, tt, minF(1, sp.treble*g+0.15))
	}
	I = idleMix(sp, I, c.slot, tt)
	r, g2, b := gradRGB(t.keyGrad, boost(clamp01(I)))
	return [3]byte{r, g2, b}
}

// stepDynamics advances per-frame state once per frame (never per key):
// bloom/wing smoothing rates assume one step per frame, and expired ripples
// are pruned here instead of once per cell.
func (m *Model) stepDynamics(dt float64, sp *spectrum, now time.Time) {
	m.fx.bloomL.step(0.12+0.62*sp.left, dt)
	m.fx.bloomR.step(0.12+0.62*sp.right, dt)
	m.fx.wing.step(minF(1, sp.bass*1.25), dt)
	kept := m.fx.ripples[:0]
	for _, rp := range m.fx.ripples {
		if age := now.Sub(rp.start).Seconds(); age >= 0 && age < rippleLife {
			kept = append(kept, rp)
		}
	}
	m.fx.ripples = kept
}

// paintBloom blends two radial bubbles driven by the left and right
// stereo channels. The left bubble samples the cool end of the theme
// gradient and the right the warm end, so stereo width reads as color, not
// just brightness. Mono sources collapse to a single central hue.
func (m *Model) paintBloom(c keyCell, t theme, dt float64, sp *spectrum) [3]byte {
	_ = dt
	lx, ly := bloomLX*kbAspect, bloomLY
	rx, ry := bloomRX*kbAspect, bloomRY
	g := func(ex, ey, rad float64) float64 {
		dx, dy := c.px-ex, c.py-ey
		d2 := dx*dx + dy*dy
		return math.Exp(-d2 / (rad*rad + 1e-6) * 2.0)
	}
	ag := sp.gain()
	Il := g(lx, ly, m.fx.bloomL.v) * minF(1, 0.25+sp.left*ag)
	Ir := g(rx, ry, m.fx.bloomR.v) * minF(1, 0.25+sp.right*ag)

	cL1, cL2, cL3 := gradRGB(t.keyGrad, 0.22)
	cR1, cR2, cR3 := gradRGB(t.keyGrad, 0.85)
	w := Il + Ir + 0.04
	r := clamp01((float64(cL1)*Il + float64(cR1)*Ir) / w / 255)
	gr := clamp01((float64(cL2)*Il + float64(cR2)*Ir) / w / 255)
	b := clamp01((float64(cL3)*Il + float64(cR3)*Ir) / w / 255)
	// Stereo separation widens saturation: centered mono stays pastel,
	// hard-panned audio pushes the active side toward full color.
	sep := math.Abs(Il - Ir)
	f := boost(clamp01(Il + Ir + 0.10*sep))
	tt := float64(m.fx.frame) / 60.0
	f = idleMix(sp, f, c.slot, tt)
	return [3]byte{byte(r * f * 255), byte(gr * f * 255), byte(b * f * 255)}
}

// paintPlasma renders a flowing hue field: the noise surface picks the
// color and the level picks the brightness. This is the only mode that
// cycles hue with time and mids, so it never reads as another EQ variant.
func (m *Model) paintPlasma(c keyCell, t theme, now time.Time, sp *spectrum) [3]byte {
	_ = t
	tt := now.Sub(m.startT).Seconds()
	n := flowField(c.px, c.py, tt)
	turb := sp.treble * math.Sin(6.28318*(c.px*2.3+c.py*1.8+tt*1.05))
	v := clamp01((n+turb*0.5)/1.4 + 0.5)
	hue := c.px*0.55 + c.py*0.25 + tt*0.06 + sp.mid*0.45 + v*0.18
	if hue < 0 {
		hue -= math.Floor(hue)
	} else {
		hue -= math.Floor(hue)
	}
	ag := sp.gain()
	I := clamp01(v) * (0.18 + 0.82*minF(1, sp.overall*ag*1.6+0.25*sp.bass))
	I = idleMix(sp, I, c.slot, tt)
	r, g, b := hsvRGB(hue, 0.85, boost(I))
	return [3]byte{r, g, b}
}

const (
	strobeIgnite   = 0.035 // xenon core, seconds
	strobeAfterEnd = 0.135 // chromatic afterburn end, seconds
	strobeEdgeEnd  = 0.175 // perimeter afterburn end, seconds
)

// strobePattern selects the ignition geometry for a beat.
type strobePattern int

const (
	patSupernova strobePattern = iota
	patSplit
	patSlash
)

func (m *Model) strobePatternFor() strobePattern {
	if m.fx.beatCount%4 == 0 && m.fx.beatCount > 0 {
		return patSlash
	}
	if m.fx.lastKind == kindSnare || (m.fx.beats.bpm > 135 && m.fx.beatCount%2 == 1) {
		return patSplit
	}
	return patSupernova
}

// mixRGB blends two colors by k in 0..1.
func mixRGB(a, b [3]byte, k float64) [3]byte {
	k = clamp01(k)
	return [3]byte{
		byte(float64(a[0]) + (float64(b[0])-float64(a[0]))*k),
		byte(float64(a[1]) + (float64(b[1])-float64(a[1]))*k),
		byte(float64(a[2]) + (float64(b[2])-float64(a[2]))*k),
	}
}

func scaleRGB(c [3]byte, k float64) [3]byte {
	k = clamp01(k)
	return [3]byte{byte(float64(c[0]) * k), byte(float64(c[1]) * k), byte(float64(c[2]) * k)}
}

// paintStrobe is the Overdrive Strobe: a white-hot xenon ignition with
// rotating spatial geometry, a chromatic afterburn cooling through the
// active theme, and a near-black gap lit only by sub-bass undercurrent
// on the bottom row and treble arc sparks. Drops and every 8th beat
// double-tap. Silence breathes dim instead of strobing.
func (m *Model) paintStrobe(c keyCell, t theme, now time.Time, sp *spectrum) [3]byte {
	tt := now.Sub(m.startT).Seconds()
	if sp.quietFor >= 2 {
		k := 0.5 + 0.5*math.Sin(tt*3.14159)
		v := 0.05 + 0.08*k
		r, g, b := hsvRGB(0.88+0.06*k, 0.8, v)
		return [3]byte{r, g, b}
	}
	age := tt
	if !m.fx.lastBeat.IsZero() {
		age = now.Sub(m.fx.lastBeat).Seconds()
		if age < 0 {
			age = 0
		}
	}
	strength := m.fx.lastStrength
	if strength <= 0 {
		strength = 0.7
	}
	overdrive := m.fx.lastDrop || (m.fx.beatCount > 0 && m.fx.beatCount%8 == 0)
	// Spatial travel: supernova reaches the edge in ~25ms; split and
	// slash use position along their axis instead.
	cx, cy := rippleX*kbAspect, rippleY
	dx, dy := c.px-cx, c.py-cy
	dist := math.Sqrt(dx*dx + dy*dy)
	pat := m.strobePatternFor()
	travel := dist / 1.6 * 0.025
	active := 1.0
	switch pat {
	case patSplit:
		left := c.x < 0.5
		wantLeft := m.fx.beatCount%2 == 0
		if left != wantLeft {
			active = 0.07
		}
		travel = 0
	case patSlash:
		// Diagonal front sweeping top-left to bottom-right.
		s := clamp01((c.px/2.8 + c.py) / 2)
		travel = s * 0.025
	}
	local := age - travel
	white := mixRGB([3]byte{255, 255, 255}, t.peakRGB, 0.25)
	hot := scaleRGB(white, (0.45+0.55*strength)*active)
	// Double-tap overdrive: second ignition after a blackout gap.
	if overdrive && age >= 0.050 && age < 0.075 {
		return hot
	}
	if overdrive && age >= strobeIgnite && age < 0.050 {
		return [3]byte{1, 0, 3}
	}
	if local >= 0 && local < strobeIgnite {
		return hot
	}
	edge := c.row == 0 || c.row == numRows-1 || c.x < 0.10 || c.x > 0.90
	end := strobeAfterEnd
	if edge {
		end = strobeEdgeEnd
	}
	if local >= strobeIgnite && local < end {
		k := (local - strobeIgnite) / (end - strobeIgnite)
		gr, gg, gb := gradRGB(t.keyGrad, boost((1-k)*(0.45+0.55*strength)))
		cool := mixRGB(white, [3]byte{gr, gg, gb}, 0.35+0.65*k)
		return scaleRGB(cool, active)
	}
	// Dark gap: bottom row breathes with sub-bass; treble sparks arc.
	if c.row == numRows-1 {
		ag := sp.gain()
		lvl := boost(clamp01(0.04 + 0.22*minF(1, sp.bass*ag)))
		gr, gg, gb := gradRGB(t.keyGrad, lvl)
		return [3]byte{gr, gg, gb}
	}
	hiOnset := 0.0
	if len(sp.onset) > 8 {
		hiOnset = sp.onset[7]
		if sp.onset[8] > hiOnset {
			hiOnset = sp.onset[8]
		}
	}
	if sp.treble > 0.35 && hiOnset > 0.4 && hash01(c.slot, m.fx.frame) > 0.985 {
		return scaleRGB(t.peakRGB, 0.9)
	}
	return [3]byte{1, 0, 3}
}

// toward Esc and Enter with an asymmetric trailing tail. Treble adds a
// faint leading-edge shimmer.
func (m *Model) paintWings(c keyCell, t theme, dt float64, sp *spectrum) [3]byte {
	_ = dt
	d := math.Abs(c.x-rippleX) / 0.50
	if d > 1 {
		d = 1
	}
	front := 0.15 + 0.85*clamp01(m.fx.wing.v)
	off := d - front
	var I float64
	if off <= 0 {
		I = math.Exp(off * off / -0.012) // tight bright front
	} else {
		I = math.Exp(-off*4.5) * 0.75 // long fading tail behind
	}
	ag := sp.gain()
	I *= 0.25 + 0.75*minF(1, m.fx.wing.v*1.2+sp.bass*ag*0.4)
	I *= 1 + 0.22*(1-math.Abs(2*c.y-1))*d
	I += 0.03 + 0.05*minF(1, sp.overall*ag)
	I += sp.onset[min(len(sp.onset)-1, int(c.x*numBands))] * 0.25 * (1 - d)
	tt := float64(m.fx.frame) / 60.0
	I = idleMix(sp, I, c.slot, tt)
	r, g, b := gradRGB(t.keyGrad, boost(clamp01(I)))
	return [3]byte{r, g, b}
}
