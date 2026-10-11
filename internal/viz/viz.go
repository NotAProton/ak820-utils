// Package viz renders an audio spectrum on the keyboard and in the terminal.
// cava feeds the frequency bars; the TUI draws the same bars so the effect
// is visible without looking at the keyboard.
package viz

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/NotAProton/ak820-utils/internal/kb"
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
		name: "synthwave", peakColor: "229",
		colors:  []string{"236", "53", "127", "199", "213", "218"},
		keyGrad: [][3]byte{{10, 2, 30}, {60, 8, 90}, {140, 10, 130}, {220, 30, 120}, {255, 90, 90}, {255, 200, 120}},
		peakRGB: [3]byte{255, 255, 220},
	},
	{
		name: "ocean", peakColor: "231",
		colors:  []string{"17", "24", "32", "39", "81", "123"},
		keyGrad: [][3]byte{{0, 5, 25}, {0, 40, 90}, {0, 100, 160}, {0, 170, 210}, {60, 220, 230}, {200, 255, 250}},
		peakRGB: [3]byte{255, 255, 255},
	},
	{
		name: "fire", peakColor: "231",
		colors:  []string{"52", "88", "124", "166", "208", "220"},
		keyGrad: [][3]byte{{15, 0, 0}, {90, 8, 0}, {180, 30, 0}, {240, 90, 0}, {255, 160, 20}, {255, 240, 120}},
		peakRGB: [3]byte{255, 255, 255},
	},
}

// ThemeNames lists available themes.
func ThemeNames() []string {
	out := make([]string, 0, len(themes))
	for _, t := range themes {
		out = append(out, t.name)
	}
	sort.Strings(out)
	return out
}

// keyCell places one physical key in the bar grid (bar column, row).
type keyCell struct {
	slot int
	bar  int
	row  int // 0 = top row
}

// layout assigns every addressable key a bar column and row, following the
// keyboard's physical geometry (row bands top to bottom, x-order within).
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
		for i, name := range row {
			slot, ok := board.Key(name)
			if !ok {
				continue
			}
			bar := int(rowX[r][i] * numBars)
			if bar >= numBars {
				bar = numBars - 1
			}
			cells = append(cells, keyCell{slot: slot, bar: bar, row: r})
		}
	}
	return cells
}

// Model is the visualizer bubbletea model.
type Model struct {
	kb      *kb.KB
	cells   []keyCell
	themeIx int

	bars   []float64
	peaks  []float64
	raw    chan cavaMsg
	cava   *exec.Cmd
	quit   chan struct{}
	rng    *rand.Rand
	errMsg string
}

// New builds the model and starts cava.
func New(board *kb.KB, themeName string) (Model, error) {
	m := Model{
		kb: board, cells: layout(board),
		bars: make([]float64, numBars), peaks: make([]float64, numBars),
		raw: make(chan cavaMsg, 8), quit: make(chan struct{}),
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	m.themeIx = indexOfTheme(themeName)
	if err := m.startCava(); err != nil {
		return Model{}, err
	}
	return m, nil
}

func indexOfTheme(name string) int {
	for i, t := range themes {
		if t.name == name {
			return i
		}
	}
	return 0
}

func (m *Model) startCava() error {
	if _, err := exec.LookPath("cava"); err != nil {
		return fmt.Errorf("the visualizer renders audio from cava; install it (https://github.com/karlstav/cava) — %w", err)
	}
	cfg, err := os.CreateTemp("", "ak820-utils-cava-*.conf")
	if err != nil {
		return err
	}
	fmt.Fprintf(cfg, "input = pulse\nmethod = pulse\nchannels = mono\nbars = %d\nsamplerate = 44100\nascii_max_range = 100\n", numBars)
	cfg.Close()

	cmd := exec.Command("cava", "-p", cfg.Name())
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting cava: %w", err)
	}
	m.cava = cmd
	go func() {
		buf := make([]byte, numBars)
		for {
			n, err := stdout.Read(buf)
			if err != nil {
				return
			}
			if n == numBars {
				msg := make(cavaMsg, numBars)
				for i := 0; i < numBars; i++ {
					msg[i] = int(buf[i])
				}
				select {
				case m.raw <- msg:
				case <-m.quit:
					return
				}
			}
		}
	}()
	return nil
}

// Init kicks off the render loop.
func (m Model) Init() tea.Cmd {
	return m.tick()
}

func (m Model) tick() tea.Cmd {
	return tea.Tick(time.Second/30, func(time.Time) tea.Msg { return tickMsg(time.Now()) })
}

// Update consumes bars and renders.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case cavaMsg:
		for i, v := range msg {
			lvl := float64(v) / 100.0
			if lvl > m.bars[i] {
				m.bars[i] += (lvl - m.bars[i]) * 0.5
			} else {
				m.bars[i] += (lvl - m.bars[i]) * 0.35
			}
			if m.bars[i] > m.peaks[i] {
				m.peaks[i] = m.bars[i]
			} else {
				m.peaks[i] -= 0.015
			}
		}
		m.paintKeyboard()
		return m, nil
	case tickMsg:
		return m, m.tick()
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			return m, tea.Quit
		case "t":
			m.themeIx = (m.themeIx + 1) % len(themes)
		}
	}
	return m, nil
}

// paintKeyboard maps bar heights onto key rows (bottom rows light first).
func (m *Model) paintKeyboard() {
	t := themes[m.themeIx]
	for _, c := range m.cells {
		lvl := m.bars[c.bar]
		pk := m.peaks[c.bar]
		height := float64(numRows-1-c.row) / float64(numRows-1)
		switch {
		case height <= lvl:
			r, g, b := gradRGB(t.keyGrad, lvl)
			m.kb.SetSlot(c.slot, r, g, b)
		case abs(height-pk) < 0.14 && pk > 0.15:
			m.kb.SetSlot(c.slot, t.peakRGB[0], t.peakRGB[1], t.peakRGB[2])
		default:
			m.kb.SetSlot(c.slot, 2, 0, 8)
		}
	}
	_ = m.kb.Flush()
}

func gradRGB(grad [][3]byte, val float64) (byte, byte, byte) {
	if val < 0 {
		val = 0
	}
	if val > 1 {
		val = 1
	}
	f := val * float64(len(grad)-1)
	i0 := int(f)
	i1 := i0 + 1
	if i1 >= len(grad) {
		i1 = len(grad) - 1
	}
	t := f - float64(i0)
	c0, c1 := grad[i0], grad[i1]
	mix := func(a, b byte) byte {
		return byte(float64(a) + (float64(b)-float64(a))*t)
	}
	return mix(c0[0], c1[0]), mix(c0[1], c1[1]), mix(c0[2], c1[2])
}

// View renders the terminal spectrum.
func (m Model) View() string {
	t := themes[m.themeIx]
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")).
		Render(fmt.Sprintf(" AK820 SPECTRUM — %s ", t.name)) +
		dimStyle().Render("  t: theme   q: quit") + "\n")

	// Spectrum: top row first; each cell shows the bar level at that height.
	var rows []string
	for r := numRows - 1; r >= 0; r-- {
		threshold := float64(r) / float64(numRows-1)
		var line strings.Builder
		for bar := 0; bar < numBars; bar++ {
			lvl := m.bars[bar]
			pk := m.peaks[bar]
			switch {
			case threshold <= lvl:
				line.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(t.colors[colorIndex(lvl)])).Render("██"))
			case abs(threshold-pk) < 0.14 && pk > 0.15:
				line.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(t.peakColor)).Render("──"))
			default:
				line.WriteString(dimStyle().Render("░░"))
			}
		}
		rows = append(rows, line.String())
	}
	b.WriteString(strings.Join(rows, "\n"))
	if m.errMsg != "" {
		b.WriteString("\n" + m.errMsg)
	}
	return b.String()
}

func colorIndex(lvl float64) int {
	i := int(lvl * 6)
	if i > 5 {
		i = 5
	}
	return i
}

func dimStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// Close stops cava.
func (m Model) Close() {
	close(m.quit)
	if m.cava != nil && m.cava.Process != nil {
		_ = m.cava.Process.Kill()
		_ = m.cava.Wait()
	}
}
