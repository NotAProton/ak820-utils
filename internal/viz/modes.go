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

// beatTracker triggers on bass onsets above a rolling average. It is a
// Schmitt trigger: firing disarms it until the signal dips back near the
// average, so a loud sustained section cannot chatter at the threshold and
// strobe-like consumers cannot average out to a constant glow. The
// cooldown is a backstop rate limit on top.
type beatTracker struct {
	ema      float64
	last     time.Time
	cooldown time.Duration
	fired    bool // set on fire, cleared when bass dips to re-arm
}

// DefaultBeatCooldown is the production cooldown between ripple triggers.
const DefaultBeatCooldown = 120 * time.Millisecond

func (b *beatTracker) trig(bass float64, now time.Time) bool {
	b.ema += (bass - b.ema) * 0.06
	if b.fired {
		// Re-arm only on a genuine dip: the cooldown alone cannot
		// separate a sustained loud section from repeated onsets.
		if bass < b.ema*1.15 {
			b.fired = false
		}
		return false
	}
	if bass > b.ema*1.4 && bass > 0.10 && now.Sub(b.last) > b.cooldown {
		b.last = now
		b.fired = true
		return true
	}
	return false
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

// numAccentKeys is the accent row size (14 top-row keys plus 4 nav keys)
// used to spread the rainbow hue across the overlay.
const numAccentKeys = 18

// modeState carries all per-mode dynamics across frames.
type modeState struct {
	beats   beatTracker
	ripples []ripple
	wing    spring
	bloomL  spring
	bloomR  spring
	frame   uint32

	accentOn   bool
	beatCount  uint64
	lastBeat   time.Time
	beatPeriod float64 // seconds between beats, EMA
}

// noteBeat records a bass onset for the tempo-synced accent overlay. The
// period EMA is clamped to a musical range so stray triggers cannot freeze
// or strobe the rainbow.
func (fx *modeState) noteBeat(now time.Time) {
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
}

// paintAccent renders the mode-independent tempo overlay for the top row
// and nav cluster: a rainbow that steps one full cycle every eight beats
// and pulses bright on each onset. Hue spreads across the row so the board
// shows a spectrum, not a single flashing color.
func (m *Model) paintAccent(c keyCell, now time.Time, sp *spectrum) [3]byte {
	tt := now.Sub(m.startT).Seconds()
	if sp.quietFor >= 2 {
		hue := tt*0.05 + float64(c.accentIx)/numAccentKeys
		hue -= math.Floor(hue)
		r, g, b := hsvRGB(hue, 0.7, 0.15)
		return [3]byte{r, g, b}
	}
	period := m.fx.beatPeriod
	if period <= 0 {
		period = 0.5
	}
	age := tt
	if !m.fx.lastBeat.IsZero() {
		age = now.Sub(m.fx.lastBeat).Seconds()
		if age < 0 {
			age = 0
		}
	}
	pulse := math.Exp(-age * 2.5)
	ag := sp.gain()
	floor := 0.22 + 0.25*minF(1, sp.overall*ag)
	v := clamp01(floor + 0.78*pulse*(0.35+0.65*minF(1, sp.bass*ag+sp.flux*0.5)))
	hue := float64(m.fx.beatCount)/8 + float64(c.accentIx)/numAccentKeys
	hue -= math.Floor(hue)
	r, g, b := hsvRGB(hue, 0.9, boost(v))
	return [3]byte{r, g, b}
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

// strobePalette is the fixed synthwave rotation for strobe mode: hot
// pink, purple, cyan, warning red, and pale yellow.
var strobePalette = [][3]byte{
	{255, 119, 169},
	{186, 119, 255},
	{59, 201, 255},
	{255, 89, 131},
	{255, 234, 108},
}

// strobeHold is how long the board stays full-bright after an onset before
// the hard cut back to near-black.
const strobeHold = 0.08 // seconds

// paintStrobe flashes the whole board in synthwave on every bass beat: a
// hard snap from near-black to full-bright, a short hold, then a hard cut.
// The color steps through the synthwave palette per beat. Between beats the
// board sits near-black with a faint bass glow; in silence it breathes dim
// instead of strobing.
func (m *Model) paintStrobe(c keyCell, t theme, now time.Time, sp *spectrum) [3]byte {
	_ = t
	_ = c
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
	if age < strobeHold {
		pal := strobePalette[m.fx.beatCount%uint64(len(strobePalette))]
		return [3]byte{pal[0], pal[1], pal[2]}
	}
	ag := sp.gain()
	floor := 0.015 + 0.06*minF(1, sp.bass*ag)
	return [3]byte{byte(floor * 255), byte(floor * 60), byte(floor * 120)}
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
