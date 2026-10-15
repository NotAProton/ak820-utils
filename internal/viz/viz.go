// Package viz renders an audio spectrum on the keyboard and in the terminal.
// cava feeds the frequency bars; the TUI draws the same bars so the effect
// is visible without looking at the keyboard.
package viz

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/NotAProton/ak820-utils/internal/kb"
	"github.com/NotAProton/ak820-utils/internal/ui"
)

const (
	numBars   = 18
	numRows   = 6
	peakDecay = 0.9 // fraction kept per second
)

// cavaMsg carries one read of spectrum bars (0-100 each).
type cavaMsg []int

type tickMsg time.Time

type theme struct {
	name      string
	colors    []string // lipgloss colors, silence → peak
	peakColor string
	keyGrad   [][3]byte // RGB gradient for the keyboard, silence → peak
	peakRGB   [3]byte
}

var themes = []theme{
	{
		name: "synthwave", peakColor: "#ffe57f",
		colors:  []string{"#2a1b3d", "#ff77a9", "#ba77ff", "#ff5983", "#ffea6c", "#ffe57f"},
		keyGrad: [][3]byte{{10, 2, 30}, {60, 8, 90}, {140, 10, 130}, {220, 30, 120}, {255, 90, 90}, {255, 200, 120}},
		peakRGB: [3]byte{255, 255, 220},
	},
	{
		name: "ocean", peakColor: "#f8f8f2",
		colors:  []string{"#0c1b2e", "#1d4e89", "#3bc9ff", "#56f89a", "#b8fff0", "#f8f8f2"},
		keyGrad: [][3]byte{{0, 5, 25}, {0, 40, 90}, {0, 100, 160}, {0, 170, 210}, {60, 220, 230}, {200, 255, 250}},
		peakRGB: [3]byte{255, 255, 255},
	},
	{
		name: "fire", peakColor: "#fff6d8",
		colors:  []string{"#2b0f0c", "#7a1f0f", "#ff5a2a", "#ff944d", "#ffea6c", "#fff6d8"},
		keyGrad: [][3]byte{{15, 0, 0}, {90, 8, 0}, {180, 30, 0}, {240, 90, 0}, {255, 160, 20}, {255, 240, 120}},
		peakRGB: [3]byte{255, 255, 255},
	},
}

// styleCache memoizes one foreground style per color so View never builds
// styles inside the per-cell loop.
var styleCache = struct {
	sync.RWMutex
	m map[string]lipgloss.Style
}{m: make(map[string]lipgloss.Style)}

func fgStyle(color string) lipgloss.Style {
	styleCache.RLock()
	s, ok := styleCache.m[color]
	styleCache.RUnlock()
	if ok {
		return s
	}
	s = lipgloss.NewStyle().Foreground(lipgloss.Color(color))
	styleCache.Lock()
	styleCache.m[color] = s
	styleCache.Unlock()
	return s
}

var dimCellStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#2a3258"))

// ThemeNames lists available themes.
func ThemeNames() []string {
	out := make([]string, 0, len(themes))
	for _, t := range themes {
		out = append(out, t.name)
	}
	sort.Strings(out)
	return out
}

// keyCell is one physical key: its RGB slot, its normalized board
// position, and its physical x (aspect-corrected) for radial effects.
type keyCell struct {
	slot   int
	bar    int
	row    int
	x, y   float64
	px, py float64
}

var ambient = [3]byte{2, 0, 8}

// layout assigns every addressable key a bar column and row, following the
// keyboard's physical geometry (row bands top to bottom, x-order within).
// Unknown keys are skipped so board variants degrade to a dark key instead
// of a panic; short coordinate rows are guarded for the same reason.
func layout(board *kb.KB) []keyCell {
	rows := [][]string{
		{"esc", "f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9", "f10", "f11", "f12", "del"},
		{"grave", "1", "2", "3", "4", "5", "6", "7", "8", "9", "0", "minus", "equal", "backspace", "home"},
		{"tab", "q", "w", "e", "r", "t", "y", "u", "i", "o", "p", "lbracket", "rbracket", "backslash", "pgup"},
		{"capslock", "a", "s", "d", "f", "g", "h", "j", "k", "l", "semicolon", "apostrophe", "enter", "pgdn"},
		{"lshift", "z", "x", "c", "v", "b", "n", "m", "comma", "period", "slash", "rshift", "up", "end"},
		{"lctrl", "lwin", "lalt", "space", "ralt", "fn", "rctrl", "left", "down", "right"},
	}
	rowX := [][]float64{
		{0.04, 0.09, 0.13, 0.17, 0.21, 0.26, 0.30, 0.35, 0.38, 0.44, 0.48, 0.52, 0.56, 0.62},
		{0.04, 0.08, 0.12, 0.16, 0.20, 0.24, 0.28, 0.33, 0.37, 0.41, 0.45, 0.49, 0.53, 0.57, 0.67},
		{0.04, 0.10, 0.14, 0.18, 0.22, 0.26, 0.30, 0.35, 0.39, 0.43, 0.47, 0.51, 0.55, 0.59, 0.67},
		{0.05, 0.11, 0.15, 0.19, 0.23, 0.27, 0.32, 0.36, 0.40, 0.44, 0.48, 0.52, 0.57, 0.67},
		{0.04, 0.13, 0.17, 0.21, 0.25, 0.29, 0.33, 0.38, 0.42, 0.46, 0.50, 0.54, 0.62, 0.67},
		{0.04, 0.09, 0.14, 0.22, 0.45, 0.49, 0.53, 0.58, 0.62, 0.66},
	}
	var cells []keyCell
	for r, row := range rows {
		if r >= len(rowX) {
			continue
		}
		for i, name := range row {
			slot, ok := board.Key(name)
			if !ok {
				continue
			}
			if i >= len(rowX[r]) {
				continue
			}
			x := rowX[r][i]
			bar := int(x * numBars)
			if bar >= numBars {
				bar = numBars - 1
			}
			if bar < 0 {
				bar = 0
			}
			cells = append(cells, keyCell{
				slot: slot, bar: bar, row: r,
				x: x, y: float64(r) / float64(numRows-1),
				px: x * kbAspect, py: float64(r) / float64(numRows-1),
			})
		}
	}
	return cells
}

// Model is the visualizer bubbletea model.
type Model struct {
	kb      *kb.KB
	cells   []keyCell
	themeIx int

	bars      []float64
	peaks     []float64
	raw       chan cavaMsg
	cava      *exec.Cmd
	cavaCfg   string
	quit      chan struct{}
	closed    *atomic.Bool
	errMsg    string
	lastFrame time.Time
	lastPaint time.Time

	modeIx int
	fx     modeState
	startT time.Time
	spec   spectrum

	width int
}

// New builds the model and starts cava. Unknown themes are an error so
// typos never silently run a different theme.
func New(board *kb.KB, themeName string) (Model, error) {
	ix, ok := indexOfTheme(themeName)
	if !ok {
		return Model{}, fmt.Errorf("unknown theme %q; valid: %s", themeName, strings.Join(ThemeNames(), " "))
	}
	m := Model{
		kb: board, cells: layout(board), themeIx: ix,
		bars: make([]float64, numBars), peaks: make([]float64, numBars),
		raw: make(chan cavaMsg, 8), quit: make(chan struct{}),
		closed: &atomic.Bool{},
		startT: time.Now(),
		width:  80,
	}
	m.lastFrame = time.Now()
	m.lastPaint = time.Now()
	m.fx.beats.cooldown = DefaultBeatCooldown
	m.fx.beatPeriod = 0.5
	if err := m.startCava(); err != nil {
		m.cleanupFailedStart()
		return Model{}, err
	}
	return m, nil
}

func indexOfTheme(name string) (int, bool) {
	for i, t := range themes {
		if t.name == name {
			return i, true
		}
	}
	return 0, false
}

func (m *Model) startCava() error {
	if _, err := exec.LookPath("cava"); err != nil {
		return fmt.Errorf("the visualizer renders audio from cava; install it (https://github.com/karlstav/cava) — %w", err)
	}
	cfg, err := os.CreateTemp("", "ak820-utils-cava-*.conf")
	if err != nil {
		return err
	}
	m.cavaCfg = cfg.Name()
	fmt.Fprintf(cfg, `[general]
bars = %d
framerate = 60
sensitivity = 130

[input]
method = pulse
source = @DEFAULT_MONITOR@
channels = stereo

[output]
method = raw
raw_target = /dev/stdout
data_format = ascii
ascii_max_range = 100
`, numBars)
	if err := cfg.Close(); err != nil {
		return err
	}

	cmd := exec.Command("cava", "-p", m.cavaCfg)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting cava: %w", err)
	}
	m.cava = cmd
	quit := m.quit
	raw := m.raw
	go func() {
		defer stdout.Close()
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			select {
			case <-quit:
				return
			default:
			}
			text := strings.TrimSpace(scanner.Text())
			text = strings.TrimSuffix(text, ";")
			if text == "" {
				continue
			}
			parts := strings.Split(text, ";")
			if len(parts) != numBars {
				continue
			}
			msg := make(cavaMsg, numBars)
			valid := true
			for i, f := range parts {
				f = strings.TrimSpace(f)
				v, err := strconv.Atoi(f)
				if err != nil {
					valid = false
					break
				}
				if v < 0 {
					v = 0
				}
				if v > 100 {
					v = 100
				}
				msg[i] = v
			}
			if !valid {
				continue
			}
			select {
			case raw <- msg:
			case <-quit:
				return
			}
		}
	}()
	return nil
}

// cleanupFailedStart removes the temp config when startCava fails after
// creating it. Called only on the New error path (no cava running yet).
func (m *Model) cleanupFailedStart() {
	if m.cavaCfg != "" {
		_ = os.Remove(m.cavaCfg)
		m.cavaCfg = ""
	}
}

// Init kicks off the render loop and the cava subscription.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.tick(), m.listenCava())
}

// listenCava forwards the next spectrum read into the program, or exits
// when the model is closed.
func (m Model) listenCava() tea.Cmd {
	return func() tea.Msg {
		select {
		case msg, ok := <-m.raw:
			if !ok {
				return nil
			}
			return msg
		case <-m.quit:
			return nil
		}
	}
}

func (m Model) tick() tea.Cmd {
	return tea.Tick(time.Second/60, func(time.Time) tea.Msg { return tickMsg(time.Now()) })
}

// Update consumes bars and renders.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = ui.ClampWidth(msg.Width)
		return m, nil
	case cavaMsg:
		if len(msg) < numBars || len(m.bars) < numBars || len(m.peaks) < numBars {
			return m, m.listenCava()
		}
		now := time.Now()
		dt := now.Sub(m.lastFrame).Seconds()
		if dt <= 0 || dt > 0.5 {
			dt = 1.0 / 60.0
		}
		m.lastFrame = now
		m.lastPaint = now
		// Smooth the raw bars for the terminal spectrum.
		attack := minF(1.0, dt*25)
		fall := minF(1.0, dt*18)
		for i, v := range msg {
			if i >= len(m.bars) {
				break
			}
			lvl := float64(v) / 100.0
			if lvl > m.bars[i] {
				m.bars[i] += (lvl - m.bars[i]) * attack
			} else {
				m.bars[i] += (lvl - m.bars[i]) * fall
			}
			if m.bars[i] > m.peaks[i] {
				m.peaks[i] = m.bars[i]
			} else {
				m.peaks[i] -= 0.45 * dt
				if m.peaks[i] < 0 {
					m.peaks[i] = 0
				}
			}
		}
		// Fold the frame into the shared spectrum the modes read.
		deriveSpectrum(&m.spec, msg, dt)
		// Beat detection reads instantaneous (unsmoothed) multi-band
		// energy: smoothing blunts the transient edge a kick is.
		if ev, fired := m.fx.beats.updateInput(beatInputFromRaw([]int(msg)), now); fired {
			m.fx.noteBeatEvent(now, ev)
			m.fx.ripples = append(m.fx.ripples,
				ripple{start: now, amp: 0.35 + 0.65*ev.Strength})
			if len(m.fx.ripples) > 8 {
				m.fx.ripples = m.fx.ripples[len(m.fx.ripples)-8:]
			}
		}
		m.paintKeyboard(now, dt)
		return m, m.listenCava()
	case tickMsg:
		// Animate flowing modes even when cava is silent so plasma and
		// ripples never freeze between frames.
		now := time.Now()
		dt := now.Sub(m.lastPaint).Seconds()
		if dt <= 0 || dt > 0.5 {
			dt = 1.0 / 60.0
		}
		m.lastPaint = now
		// Decay peaks toward zero on ticks without audio.
		for i := range m.peaks {
			if i < len(m.bars) && m.peaks[i] > m.bars[i] {
				m.peaks[i] -= 0.45 * dt
				if m.peaks[i] < 0 {
					m.peaks[i] = 0
				}
			}
		}
		// Advance the quiet timer so the idle starfield fades in. Ticks do
		// not fold silence into the spectrum; only cava frames move levels.
		if m.spec.overall > 0.03 {
			m.spec.quietFor = 0
		} else {
			m.spec.quietFor += dt
			if m.spec.quietFor > 30 {
				m.spec.quietFor = 30
			}
		}
		m.paintKeyboard(now, dt)
		return m, m.tick()
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			return m, tea.Quit
		case "m", "tab":
			if numModes > 0 {
				m.modeIx = (m.modeIx + 1) % numModes
			}
		case "t":
			if len(themes) > 0 {
				m.themeIx = (m.themeIx + 1) % len(themes)
			}
		}
	}
	return m, nil
}

// paintKeyboard paints every mode's per-key colors and flushes to the board.
// Flush failures surface in the TUI instead of failing silently. A nil
// board or empty geometry renders nothing so unit tests can exercise paint
// logic without hardware.
func (m *Model) paintKeyboard(now time.Time, dt float64) {
	if m.themeIx < 0 || m.themeIx >= len(themes) {
		m.themeIx = 0
	}
	if m.modeIx < 0 || m.modeIx >= numModes {
		m.modeIx = 0
	}
	if dt <= 0 || dt > 0.5 {
		dt = 1.0 / 60.0
	}
	t := themes[m.themeIx]
	m.fx.frame++
	m.stepDynamics(dt, &m.spec, now)
	if m.kb == nil || len(m.cells) == 0 {
		return
	}
	for _, c := range m.cells {
		var rgb [3]byte
		switch m.modeIx {
		case modeBars:
			rgb = m.paintBars(c, t, &m.spec)
		case modeBloom:
			rgb = m.paintBloom(c, t, dt, &m.spec)
		case modePlasma:
			rgb = m.paintPlasma(c, t, now, &m.spec)
		case modeWings:
			rgb = m.paintWings(c, t, dt, &m.spec)
		case modeStrobe:
			rgb = m.paintStrobe(c, t, now, &m.spec)
		default:
			rgb = m.paintRipple(c, t, now, &m.spec)
		}
		m.kb.SetSlot(c.slot, rgb[0], rgb[1], rgb[2])
	}
	if err := m.kb.Flush(); err != nil {
		m.errMsg = "keyboard error: " + err.Error()
	} else {
		m.errMsg = ""
	}
}

func gradRGB(grad [][3]byte, val float64) (byte, byte, byte) {
	if len(grad) == 0 {
		return 0, 0, 0
	}
	if len(grad) == 1 {
		return grad[0][0], grad[0][1], grad[0][2]
	}
	if val < 0 {
		val = 0
	}
	if val > 1 {
		val = 1
	}
	f := val * float64(len(grad)-1)
	i0 := int(f)
	i1 := i0 + 1
	if i0 < 0 {
		i0 = 0
	}
	if i0 >= len(grad) {
		i0 = len(grad) - 1
	}
	if i1 >= len(grad) {
		i1 = len(grad) - 1
	}
	t := f - float64(i0)
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	c0, c1 := grad[i0], grad[i1]
	mix := func(a, b byte) byte {
		return byte(float64(a) + (float64(b)-float64(a))*t)
	}
	return mix(c0[0], c1[0]), mix(c0[1], c1[1]), mix(c0[2], c1[2])
}

// View renders the terminal spectrum.
func (m Model) View() string {
	themeIx := m.themeIx
	if themeIx < 0 || themeIx >= len(themes) {
		themeIx = 0
	}
	modeIx := m.modeIx
	if modeIx < 0 || modeIx >= numModes {
		modeIx = 0
	}
	t := themes[themeIx]
	width := ui.ClampWidth(m.width)
	if width < 40 {
		width = 40
	}

	header := lipgloss.JoinHorizontal(lipgloss.Center,
		ui.Badge("ak820 spectrum", true), " ",
		ui.Pill(t.name, ui.ColorSparkleGold), " ",
		ui.Pill(modeNames[modeIx], ui.ColorRainbowBlue), " ",
		ui.Pill(bpmLabel(m), ui.ColorRainbowPurple), " ",
		beatPill(m))

	strip := ui.RenderRainbowStrip(width - 4)

	// Spectrum card: top row first; each cell shows the bar level.
	var rows []string
	for r := numRows - 1; r >= 0; r-- {
		threshold := float64(r) / float64(numRows-1)
		var line strings.Builder
		for bar := 0; bar < numBars; bar++ {
			var lvl, pk float64
			if bar < len(m.bars) {
				lvl = m.bars[bar]
			}
			if bar < len(m.peaks) {
				pk = m.peaks[bar]
			}
			flash := modeIx == modeStrobe && beatFlash(m)
			switch {
			case flash && lvl > 0.01:
				line.WriteString(fgStyle(t.peakColor).Render("██"))
			case lvl > 0.01 && threshold <= lvl:
				line.WriteString(fgStyle(t.colors[colorIndex(lvl)]).Render("██"))
			case abs(threshold-pk) < 0.14 && pk > 0.15:
				line.WriteString(fgStyle(t.peakColor).Render("──"))
			default:
				line.WriteString(dimCellStyle.Render("··"))
			}
		}
		rows = append(rows, line.String())
	}
	specBody := strings.Join(rows, "\n")
	if m.errMsg != "" {
		specBody += "\n" + ui.StyleMiss.Render("✦ keyboard error: "+m.errMsg)
	}
	specCard := ui.RenderCard("spectrum ✧ live eq", specBody, true)

	// Mode card lists every mode with an active marker.
	var modeLines []string
	for i, name := range modeNames {
		marker := "·"
		if i == modeIx {
			marker = "»"
			modeLines = append(modeLines, ui.StyleMain.Render(marker+" "+name+" ★"))
		} else {
			modeLines = append(modeLines, ui.StyleDim.Render(marker+" "+name))
		}
	}
	// Theme card lists every theme with an active marker.
	var themeLines []string
	for i, th := range themes {
		if i == themeIx {
			themeLines = append(themeLines, ui.StyleMain.Render("» "+th.name+" ★"))
		} else {
			themeLines = append(themeLines, ui.StyleDim.Render("· "+th.name))
		}
	}
	level := int(clamp01(m.spec.overall) * 12)
	lvlBar := strings.Repeat("■", level) + strings.Repeat("·", 12-level)
	sideBody := "MODES\n" + strings.Join(modeLines, "\n") +
		"\n\nTHEMES\n" + strings.Join(themeLines, "\n") +
		"\n\nTEMPO " + bpmLabel(m) +
		"\nBEATS " + fmt.Sprintf("%d", m.fx.beatCount) +
		"\nLEVEL " + fgStyle(string(ui.ColorRainbowGreen)).Render(lvlBar)
	if modeIx == modeStrobe {
		sideBody += "\n" + ui.StyleMiss.Render("⚠ strobe: photosensitivity caution")
	}
	if m.spec.quietFor >= 2 {
		sideBody += "\n" + ui.StyleDim.Render("✧ idle drift — play audio")
	}
	sideCard := ui.RenderCard("board ✦ control", sideBody, false)

	body := lipgloss.JoinHorizontal(lipgloss.Top, specCard, sideCard)
	// Wrap rather than clip when the terminal is narrow.
	if lipgloss.Width(body) > width {
		body = specCard + "\n" + sideCard
	}
	help := ui.StyleDim.Render(" m/tab: mode ✦ t: theme ✦ q: quit")
	return header + "\n" + strip + "\n" + body + "\n" + help
}

// bpmLabel reports the locked tempo, or a searching placeholder.
func bpmLabel(m Model) string {
	if m.fx.beats.confident && m.fx.beats.bpm > 0 {
		return fmt.Sprintf("%d BPM ✦", int(m.fx.beats.bpm+0.5))
	}
	if m.fx.beats.bpm > 0 {
		return fmt.Sprintf("%d BPM ?", int(m.fx.beats.bpm+0.5))
	}
	return "-- BPM"
}

// beatFlash reports whether a beat landed in the last 120ms.
func beatFlash(m Model) bool {
	return !m.fx.lastBeat.IsZero() && time.Since(m.fx.lastBeat) < 120*time.Millisecond
}

func beatPill(m Model) string {
	if beatFlash(m) {
		return ui.Pill("● BEAT", ui.ColorRainbowRed)
	}
	return ui.StyleDim.Render("○ beat")
}

func colorIndex(lvl float64) int {
	i := int(lvl * 6)
	if i < 0 {
		i = 0
	}
	if i > 5 {
		i = 5
	}
	return i
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// Close stops cava and removes the temp config. It is idempotent and safe
// for concurrent callers; the mutex is not held across Process.Wait.
func (m *Model) Close() {
	if m.closed != nil && m.closed.CompareAndSwap(false, true) {
		select {
		case <-m.quit:
		default:
			close(m.quit)
		}
	}
	cmd := m.cava
	cfg := m.cavaCfg
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	if cfg != "" {
		_ = os.Remove(cfg)
	}
}
