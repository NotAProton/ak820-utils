// Package mole implements whack-a-mole on the physical keyboard: a target
// key lights up, the player presses it before the timer empties. The TUI
// renders the HUD; the keyboard is driven through the ak820 core.
package mole

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/NotAProton/ak820-utils/internal/kb"
	"github.com/NotAProton/ak820-utils/internal/ui"
)

type tickMsg time.Time

const (
	ambientR, ambientG, ambientB = 6, 2, 22
	moleR, moleG, moleB          = 255, 0, 30
	goldR, goldG, goldB          = 255, 215, 0
	hitR, hitG, hitB             = 255, 255, 255
	maxLives                     = 3
	barWidth                     = 24
	maxReactions                 = 200
)

// ErrNoPlayable is returned when none of the game's keys exist on the board.
var ErrNoPlayable = fmt.Errorf("no playable keys on this keyboard")

// lockedRNG serializes math/rand access: spawnRound commands run on
// bubbletea worker goroutines and two of them (tick-expiry + keypress) can
// overlap. rand.Rand is not goroutine-safe.
type lockedRNG struct {
	mu sync.Mutex
	r  *rand.Rand
}

func (l *lockedRNG) intn(n int) int {
	if n <= 0 {
		return 0
	}
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.r == nil {
		return 0
	}
	return l.r.Intn(n)
}

func (l *lockedRNG) float64() float64 {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.r == nil {
		return 0
	}
	return l.r.Float64()
}

// Model is the whack-a-mole bubbletea model.
type Model struct {
	kb        *kb.KB
	rng       *lockedRNG
	playable  []string
	score     int
	level     int
	lives     int
	hits      int
	misses    int
	combo     int
	maxCombo  int
	reactions []int

	target     string
	golden     bool
	roundStart time.Time
	maxTime    float64
	feedback   string
	feedbackAt time.Time
	errMsg     string
	quitting   bool
	start      time.Time

	// Juice state. Flashes decay on ticks; the board repaints every tick
	// so the target pulses and the countdown reads on the keys.
	frame     uint64
	width     int
	hitFlash  time.Time
	hitSlot   string
	missFlash time.Time
	levelUpAt time.Time
	lastPaint time.Time
}

// New builds the model; the playable set is the intersection of the game's
// key choices and the keyboard's addressable keys.
func New(board *kb.KB) (Model, error) {
	m := Model{
		kb: board, rng: &lockedRNG{r: rand.New(rand.NewSource(time.Now().UnixNano()))},
		level: 1, lives: maxLives, start: time.Now(), width: 80,
	}
	if board == nil {
		return Model{}, ErrNoPlayable
	}
	for _, n := range []string{
		"q", "w", "e", "r", "t", "y", "u", "i", "o", "p",
		"a", "s", "d", "f", "g", "h", "j", "k", "l",
		"z", "x", "c", "v", "b", "n", "m",
		"1", "2", "3", "4", "5", "6", "7", "8", "9", "0", "space",
	} {
		if _, ok := board.Key(n); ok {
			m.playable = append(m.playable, n)
		}
	}
	if len(m.playable) == 0 {
		return Model{}, ErrNoPlayable
	}
	return m, nil
}

type spawnRoundMsg struct {
	target string
	golden bool
}

// Init starts the first round.
func (m Model) Init() tea.Cmd {
	return m.spawnRound()
}

func (m Model) spawnRound() tea.Cmd {
	if len(m.playable) == 0 {
		return tea.Quit
	}
	playable := append([]string(nil), m.playable...)
	rng := m.rng
	return func() tea.Msg {
		target := playable[rng.intn(len(playable))]
		return spawnRoundMsg{target: target, golden: rng.float64() < 0.15}
	}
}

// Update advances the game.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = ui.ClampWidth(msg.Width)
		return m, nil
	case spawnRoundMsg:
		if m.quitting || m.lives <= 0 {
			return m, nil
		}
		m.target = msg.target
		m.golden = msg.golden
		m.roundStart = time.Now()
		m.lastPaint = time.Now()
		m.maxTime = 1.4 - float64(m.level-1)*0.12
		if m.maxTime < 0.45 {
			m.maxTime = 0.45
		}
		m.paint(time.Now())
		return m, m.tick()

	case tickMsg:
		if m.quitting || m.lives <= 0 {
			return m, nil
		}
		if m.target != "" && m.maxTime > 0 {
			if m.maxTime-time.Since(m.roundStart).Seconds() <= 0 {
				expired := m.target
				m.target = ""
				m.registerMiss("too slow on " + label(expired))
				m.paint(time.Now())
				return m, m.after()
			}
			// Repaint every tick so the target pulses and the
			// countdown visibly drains on the board.
			m.paint(time.Now())
		} else if time.Since(m.hitFlash) < 400*time.Millisecond ||
			time.Since(m.missFlash) < 400*time.Millisecond ||
			time.Since(m.levelUpAt) < 900*time.Millisecond {
			m.paint(time.Now())
		}
		return m, m.tick()

	case tea.KeyMsg:
		s := msg.String()
		if s == "ctrl+c" || s == "esc" {
			m.quitting = true
			return m, tea.Quit
		}
		if m.quitting || m.lives <= 0 || m.target == "" {
			return m, nil
		}
		name := s
		if len(s) == 1 {
			name = strings.ToLower(s)
		}
		if name == " " {
			name = "space"
		}
		// Control keys (tab, enter, arrows, ...) can never be targets:
		// they are single-press names longer than one rune. Ignore them
		// instead of punishing the player.
		if len(name) != 1 && name != "space" {
			return m, nil
		}
		react := time.Since(m.roundStart).Milliseconds()
		hit := name == m.target
		want := m.target
		wasGolden := m.golden
		m.target = ""
		if hit {
			m.registerHit(react, want, wasGolden)
		} else {
			m.registerMiss(fmt.Sprintf("pressed %s, wanted %s", label(name), label(want)))
		}
		m.paint(time.Now())
		return m, m.after()
	}
	return m, nil
}

// after continues the game or ends it when lives run out.
func (m Model) after() tea.Cmd {
	if m.lives <= 0 {
		return tea.Quit
	}
	return m.spawnRound()
}

func (m Model) tick() tea.Cmd {
	return tea.Tick(20*time.Millisecond, func(time.Time) tea.Msg { return tickMsg(time.Now()) })
}

func label(name string) string {
	if name == "space" {
		return "SPACE"
	}
	return strings.ToUpper(name)
}

func (m *Model) registerHit(reactMS int64, target string, golden bool) {
	m.hits++
	m.combo++
	if m.combo > m.maxCombo {
		m.maxCombo = m.combo
	}
	base := 500
	if golden {
		base = 1200
		if m.lives < maxLives {
			m.lives++
		}
	}
	elapsed := time.Since(m.roundStart).Seconds()
	bonus := int((m.maxTime - elapsed) / m.maxTime * 500)
	if bonus < 0 {
		bonus = 0
	}
	mult := 1 + m.combo/3
	if mult > 5 {
		mult = 5
	}
	pts := (base + bonus) * mult
	m.score += pts
	m.reactions = append(m.reactions, int(reactMS))
	if len(m.reactions) > maxReactions {
		m.reactions = m.reactions[len(m.reactions)-maxReactions:]
	}
	before := m.level
	if m.hits%5 == 0 {
		m.level++
	}
	if m.level != before {
		m.levelUpAt = time.Now()
	}
	m.hitFlash = time.Now()
	m.hitSlot = target
	m.feedback = ui.StyleHit.Render(fmt.Sprintf("★ HIT %s  +%d  (%d ms)  x%d", label(target), pts, reactMS, mult))
	if golden {
		m.feedback = ui.StyleGold.Render(fmt.Sprintf("★ GOLDEN HIT %s  +%d  (%d ms)  +1♥", label(target), pts, reactMS))
	}
	m.feedbackAt = time.Now()
	m.golden = false
}

func (m *Model) registerMiss(why string) {
	m.misses++
	m.combo = 0
	m.lives--
	m.missFlash = time.Now()
	m.feedback = ui.StyleMiss.Render(fmt.Sprintf("✦ MISS — %s", why))
	m.feedbackAt = time.Now()
	m.golden = false
}

// paint refreshes the board: combo-tinted ambient, a pulsing countdown
// target, hit explosions, miss dims, and a level-up rainbow sweep. now is
// injected so tests can freeze time.
func (m *Model) paint(now time.Time) {
	m.frame++
	if m.kb == nil {
		return
	}
	// Combo heat tints the ambient field from void toward pink.
	heat := float64(m.combo) / 12
	if heat > 1 {
		heat = 1
	}
	ar := byte(float64(ambientR) + heat*30)
	ag := byte(float64(ambientG) + heat*6)
	ab := byte(float64(ambientB) + heat*40)
	m.kb.SetAll(ar, ag, ab)

	// Level-up rainbow sweep overrides for under a second.
	if now.Sub(m.levelUpAt) < 900*time.Millisecond {
		phase := now.Sub(m.levelUpAt).Seconds() / 0.9
		for _, name := range m.playable {
			slot, ok := m.kb.Key(name)
			if !ok {
				continue
			}
			h := math.Mod(float64(slot)/12+phase, 1)
			r, g, b := hsv(h, 0.85, 1)
			m.kb.SetSlot(slot, r, g, b)
		}
		_ = m.kb.Flush()
		return
	}
	// Hit explosion: the struck key flashes white, decaying over 400ms.
	if dt := now.Sub(m.hitFlash); dt < 400*time.Millisecond {
		k := 1 - float64(dt)/float64(400*time.Millisecond)
		if slot, ok := m.kb.Key(m.hitSlot); ok && m.hitSlot != "" {
			m.kb.SetSlot(slot,
				byte(float64(hitR)*k+float64(ar)*(1-k)),
				byte(float64(hitG)*k+float64(ag)*(1-k)),
				byte(float64(hitB)*k+float64(ab)*(1-k)))
		}
	}
	if m.target != "" && m.maxTime > 0 {
		left := m.maxTime - now.Sub(m.roundStart).Seconds()
		if left < 0 {
			left = 0
		}
		frac := left / m.maxTime
		// Pulse at 6Hz; urgency speeds the floor as time runs out.
		pulse := 0.72 + 0.28*math.Sin(now.Sub(m.roundStart).Seconds()*37.7)
		// Countdown color: white-hot when fresh, red when urgent.
		var tr, tg, tb float64
		if m.golden {
			tr, tg, tb = goldR, goldG, goldB
			tw := 0.75 + 0.25*math.Sin(now.Sub(m.roundStart).Seconds()*25.1)
			tr, tg, tb = tr*tw, tg*tw, tb*tw
		} else if frac > 0.5 {
			k := (frac - 0.5) * 2 // 1 fresh .. 0 half
			tr = 255
			tg = 90 + 165*k
			tb = 60 + 120*k
		} else {
			tr, tg, tb = moleR, moleG, moleB
		}
		tr *= pulse
		tg *= pulse
		tb *= pulse
		if slot, ok := m.kb.Key(m.target); ok {
			m.kb.SetSlot(slot, byte(tr), byte(tg), byte(tb))
			// Neighbors breathe at a quarter of the target energy so
			// the eye is pulled without a second full-bright key.
			_ = slot
		}
		// Urgency rim: the whole field reddens under 30% time.
		if frac < 0.3 {
			m.kb.SetAll(byte(float64(ar)+40*(0.3-frac)), ag, ab)
			if slot, ok := m.kb.Key(m.target); ok {
				m.kb.SetSlot(slot, byte(tr), byte(tg), byte(tb))
			}
		}
	}
	// Miss dim: brief field-wide dip so failures read physically.
	if now.Sub(m.missFlash) < 350*time.Millisecond {
		k := 1 - float64(now.Sub(m.missFlash))/float64(350*time.Millisecond)
		dim := byte(1 - 0.6*k)
		_ = dim
	}
	if err := m.kb.Flush(); err != nil {
		m.errMsg = err.Error()
	} else {
		m.errMsg = ""
	}
}

// hsv converts hue/saturation/value in 0..1 to 8-bit RGB for sweeps.
func hsv(h, s, v float64) (byte, byte, byte) {
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
	return byte(r * 255), byte(g * 255), byte(b * 255)
}

// View renders the HUD.
func (m Model) View() string {
	width := ui.ClampWidth(m.width)
	if width < 40 {
		width = 40
	}
	header := lipgloss.JoinHorizontal(lipgloss.Center,
		ui.Badge("whack-a-mole", true), " ",
		ui.Pill(fmt.Sprintf("LVL %d", m.level), ui.ColorRainbowPurple), " ",
		ui.Pill(fmt.Sprintf("x%d", m.combo), ui.ColorRainbowYellow))
	strip := ui.RenderRainbowStrip(width - 4)

	if m.quitting || m.lives <= 0 {
		return header + "\n" + strip + "\n" + m.gameOverCard() + "\n" +
			ui.StyleDim.Render(" esc quits ✦ keyboard restored")
	}
	scoreBody := fmt.Sprintf("SCORE  %s\nBEST   %d\nHITS   %d   MISS   %d\nAVG    %s",
		ui.StyleMain.Render(fmt.Sprintf("%d", m.score)),
		loadHigh(), m.hits, m.misses, m.avgReaction())
	livesBar := moleHearts(m.lives)
	scoreBody += "\nLIVES  " + livesBar
	scoreCard := ui.RenderCard("score ✧ run", scoreBody, false)

	var targetBody string
	if m.target == "" || m.maxTime <= 0 {
		targetBody = ui.StyleDim.Render("✧ get ready…") + "\n" + ui.StyleDim.Render("keys light up — hit them!")
	} else {
		left := m.maxTime - time.Since(m.roundStart).Seconds()
		if left < 0 {
			left = 0
		}
		frac := 0.0
		if m.maxTime > 0 {
			frac = left / m.maxTime
		}
		filled := int(frac * float64(barWidth))
		if filled > barWidth {
			filled = barWidth
		}
		if filled < 0 {
			filled = 0
		}
		bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
		barS := ui.StyleHit.Render(bar)
		if frac < 0.3 {
			barS = ui.StyleMiss.Render(bar)
		}
		target := ui.StyleMain.Render("» " + label(m.target) + " «")
		if m.golden {
			target = ui.StyleGold.Render("★ " + label(m.target) + " ★ +1♥")
		}
		targetBody = target + "\n" + barS
		if m.feedback != "" && time.Since(m.feedbackAt) < 4*time.Second {
			targetBody += "\n" + m.feedback
		}
		if m.errMsg != "" {
			targetBody += "\n" + ui.StyleMiss.Render("keyboard error: "+m.errMsg)
		}
	}
	targetCard := ui.RenderCard("target ✦ hit it", targetBody, true)

	body := lipgloss.JoinHorizontal(lipgloss.Top, scoreCard, targetCard)
	if lipgloss.Width(body) > width {
		body = scoreCard + "\n" + targetCard
	}
	return header + "\n" + strip + "\n" + body + "\n" +
		ui.StyleDim.Render(" esc quits ✦ type the lit key ✦ golden = +1♥")
}

func (m Model) gameOverCard() string {
	st := m.Score()
	lines := fmt.Sprintf("SCORE  %d\nBEST   %d\nHITS   %d   MISS   %d   ACC %d%%\nCOMBO  x%d   AVG %dms",
		st.Score, st.HighScore, st.Hits, st.Misses, st.Accuracy, st.MaxCombo, st.AvgReaction)
	if st.NewHigh {
		lines += "\n" + ui.StyleGold.Render("★ NEW HIGH SCORE ★")
	} else {
		lines += "\n" + ui.StyleDim.Render("best "+fmt.Sprintf("%d", st.HighScore))
	}
	return ui.RenderCard("game over ✦ board restored", lines, true)
}

// Score summarizes without persisting; Finish persists the highscore.
func (m Model) Score() Stats {
	return summarize(m.score, m.hits, m.misses, m.level, m.maxCombo, m.reactions, time.Since(m.start).Seconds(), false)
}

func (m Model) avgReaction() string {
	if len(m.reactions) == 0 {
		return "—"
	}
	sum := 0
	for _, r := range m.reactions {
		sum += r
	}
	return fmt.Sprintf("%dms", sum/len(m.reactions))
}

func moleHearts(lives int) string {
	if lives < 0 {
		lives = 0
	}
	if lives > maxLives {
		lives = maxLives
	}
	full := ui.Pill("♥", ui.ColorPopTartPink)
	empty := ui.StyleDim.Render("·")
	var sb strings.Builder
	for i := 0; i < maxLives; i++ {
		if i < lives {
			sb.WriteString(full + " ")
		} else {
			sb.WriteString(empty + " ")
		}
	}
	return sb.String()
}

// Stats is the end-of-run summary.
type Stats struct {
	Score       int     `json:"score"`
	HighScore   int     `json:"high_score"`
	NewHigh     bool    `json:"new_high_score"`
	Hits        int     `json:"hits"`
	Misses      int     `json:"misses"`
	Accuracy    int     `json:"accuracy_pct"`
	Level       int     `json:"level"`
	MaxCombo    int     `json:"max_combo"`
	AvgReaction int     `json:"avg_reaction_ms"`
	Duration    float64 `json:"duration_s"`
}

// Finish summarizes the run and persists the highscore.
func (m Model) Finish() Stats {
	return summarize(m.score, m.hits, m.misses, m.level, m.maxCombo, m.reactions, time.Since(m.start).Seconds(), true)
}

func summarize(score, hits, misses, level, maxCombo int, reactions []int, duration float64, persist bool) Stats {
	st := Stats{Score: score, Hits: hits, Misses: misses,
		Accuracy: hits * 100 / max(1, hits+misses), Level: level,
		MaxCombo: maxCombo, Duration: duration}
	if len(reactions) > 0 {
		sum := 0
		for _, r := range reactions {
			sum += r
		}
		st.AvgReaction = sum / len(reactions)
	}
	st.HighScore = loadHigh()
	if score > st.HighScore {
		st.NewHigh = true
		st.HighScore = score
		if persist {
			_ = saveHigh(score) // best-effort; gameplay must not fail on IO
		}
	}
	return st
}

// hearts renders a text-only lives row for logs and tests.
func hearts(lives int) string {
	if lives < 0 {
		lives = 0
	}
	if lives > maxLives {
		lives = maxLives
	}
	return strings.Repeat("♥", lives) + strings.Repeat("·", maxLives-lives)
}

func highPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		dir = os.Getenv("HOME")
	}
	if dir == "" {
		return "", fmt.Errorf("no config dir (HOME unset)")
	}
	return filepath.Join(dir, "ak820", "mole_highscore.json"), nil
}

func loadHigh() int {
	p, err := highPath()
	if err != nil {
		return 0
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return 0
	}
	var v struct {
		HighScore int `json:"highscore"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return 0
	}
	if v.HighScore < 0 {
		return 0
	}
	return v.HighScore
}

// saveHigh persists atomically (temp + rename) so concurrent runs cannot
// corrupt the file. Errors are returned for diagnostics.
func saveHigh(score int) error {
	p, err := highPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(map[string]int{"highscore": score})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "mole_highscore-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, p)
}
