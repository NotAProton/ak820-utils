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
	numModes
)

var modeNames = [...]string{"ripple", "bars", "bloom", "plasma", "wings"}

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
	mono    [numBands]float64 // mono bands 0..1, smoothed
	peaks   [numBands]float64 // falling peak-hold per band
	left    float64
	right   float64
	bass    float64
	mid     float64
	treble  float64
	overall float64
}

// deriveSpectrum folds one raw cava frame (0..100 per bar) into the
// smoothed spectrum. First half of raw is the left channel, second the
// right; mono band i averages raw[i] with raw[i+numBands].
func deriveSpectrum(s *spectrum, raw []int, dt float64) {
	attack := minF(1.0, dt*25)
	fall := minF(1.0, dt*18)

	var leftSum, rightSum float64
	for i := 0; i < numBands; i++ {
		l := float64(raw[i]) / 100
		r := float64(raw[i+numBands]) / 100
		leftSum += l
		rightSum += r
		v := (l + r) / 2
		if v > s.mono[i] {
			s.mono[i] += (v - s.mono[i]) * attack
		} else {
			s.mono[i] += (v - s.mono[i]) * fall
		}
		if s.mono[i] > s.peaks[i] {
			s.peaks[i] = s.mono[i]
		} else {
			s.peaks[i] -= 0.45 * dt
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
}

func smooth(cur, target, dt, attackRate, fallRate float64) float64 {
	rate := fallRate
	if target > cur {
		rate = attackRate
	}
	return cur + (target-cur)*minF(1, dt*rate)
}

// beatTracker triggers on bass onsets above a rolling average.
type beatTracker struct {
	ema      float64
	last     time.Time
	cooldown time.Duration
}

func (b *beatTracker) trig(bass float64, now time.Time) bool {
	b.ema += (bass - b.ema) * 0.06
	if bass > b.ema*1.4 && bass > 0.10 && now.Sub(b.last) > b.cooldown {
		b.last = now
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

// modeState carries all per-mode dynamics across frames.
type modeState struct {
	beats   beatTracker
	ripples []ripple
	wing    spring
	bloomL  spring
	bloomR  spring
	frame   uint32
}

// flicker returns a deterministic 0..1 sparkle for a slot that changes
// every few frames, so treble perimeters shimmer without strobing.
func (fx *modeState) flicker(slot int) float64 {
	h := uint32(slot)*2654435761 ^ fx.frame*40503
	h ^= h >> 13
	h *= 1274126177
	return float64(h&0xffff) / 65535
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
// height, peak markers on the falling edge.
func (m *Model) paintBars(c keyCell, t theme, sp *spectrum) [3]byte {
	band := int(c.x * numBands)
	if band >= numBands {
		band = numBands - 1
	}
	lvl := sp.mono[band]
	pk := sp.peaks[band]
	height := 1 - c.y
	switch {
	case lvl > 0.01 && height <= lvl:
		r, g, b := gradRGB(t.keyGrad, boost(lvl))
		return [3]byte{r, g, b}
	case math.Abs(height-pk) < 0.14 && pk > 0.15:
		return t.peakRGB
	default:
		return ambient
	}
}

// paintRipple renders concentric shockwaves from the center keys with
// treble sparkle around the perimeter.
func (m *Model) paintRipple(c keyCell, t theme, now time.Time, sp *spectrum) [3]byte {
	cx, cy := rippleX*kbAspect, rippleY
	dx, dy := c.px-cx, c.py-cy
	d := math.Sqrt(dx*dx + dy*dy)

	var I float64
	kept := m.fx.ripples[:0]
	for _, rp := range m.fx.ripples {
		age := now.Sub(rp.start).Seconds()
		if age < rippleLife {
			kept = append(kept, rp)
			r := age * rippleSpeed
			off := d - r
			w := rp.amp * math.Exp(-(off*off)/(2*rippleSpread*rippleSpread)) * math.Exp(-age*1.6)
			if w > I {
				I = w
			}
		}
	}
	m.fx.ripples = kept

	if I < 0.05 {
		I = 0.04 + 0.30*sp.bass*math.Exp(-(dx*dx+dy*dy)/0.45)
	}
	if c.row == 0 || c.row == numRows-1 || c.x < 0.10 || c.x > 0.90 {
		I += sp.treble * 0.55 * m.fx.flicker(c.slot)
	}
	r, g, b := gradRGB(t.keyGrad, boost(clamp01(I)))
	return [3]byte{r, g, b}
}

// paintBloom blends two radial bubbles driven by the left and right
// stereo channels; overlapping areas mix the gradient's cool and warm
// ends.
func (m *Model) paintBloom(c keyCell, t theme, dt float64, sp *spectrum) [3]byte {
	m.fx.bloomL.step(0.12+0.62*sp.left, dt)
	m.fx.bloomR.step(0.12+0.62*sp.right, dt)

	lx, ly := bloomLX*kbAspect, bloomLY
	rx, ry := bloomRX*kbAspect, bloomRY
	g := func(ex, ey, rad float64) float64 {
		dx, dy := c.px-ex, c.py-ey
		d2 := dx*dx + dy*dy
		return math.Exp(-d2 / (rad*rad + 1e-6) * 2.0)
	}
	Il := g(lx, ly, m.fx.bloomL.v)
	Ir := g(rx, ry, m.fx.bloomR.v)

	cL1, cL2, cL3 := gradRGB(t.keyGrad, 0.22)
	cR1, cR2, cR3 := gradRGB(t.keyGrad, 0.85)
	w := Il + Ir + 0.04
	r := clamp01((float64(cL1)*Il + float64(cR1)*Ir) / w)
	gr := clamp01((float64(cL2)*Il + float64(cR2)*Ir) / w)
	b := clamp01((float64(cL3)*Il + float64(cR3)*Ir) / w)
	f := boost(clamp01(Il + Ir))
	return [3]byte{byte(r * f), byte(gr * f), byte(b * f)}
}

// paintPlasma renders an undulating surface: bass drives height, mids
// drive phase, treble adds turbulence.
func (m *Model) paintPlasma(c keyCell, t theme, now time.Time, sp *spectrum) [3]byte {
	tt := now.Sub(m.startT).Seconds()
	ph := sp.mid * 1.6
	h := math.Sin(6.28318*(c.px*0.55+tt*0.20+ph)) +
		math.Sin(6.28318*(c.py*1.35-tt*0.31))
	turb := sp.treble * math.Sin(6.28318*(c.px*2.3+c.py*1.8+tt*1.05))
	v := (h+turb)/2.8 + 0.5
	I := clamp01(v) * (0.16 + 0.84*minF(1, sp.overall*1.8+0.30*sp.bass))
	r, g, b := gradRGB(t.keyGrad, boost(I))
	return [3]byte{r, g, b}
}

// paintWings pulses bass from the center keys outward toward Esc and
// Enter, fanning across the top and bottom rows.
func (m *Model) paintWings(c keyCell, t theme, dt float64, sp *spectrum) [3]byte {
	m.fx.wing.step(minF(1, sp.bass*1.25), dt)
	dxn := minF(1, math.Abs(c.x-rippleX)/0.50)
	I := m.fx.wing.v * math.Pow(1-dxn, 1.6) * (1 + 0.30*sp.treble)
	I *= 1 + 0.22*(1-math.Abs(2*c.y-1))*dxn
	I += 0.03 + 0.05*sp.overall
	r, g, b := gradRGB(t.keyGrad, boost(clamp01(I)))
	return [3]byte{r, g, b}
}
