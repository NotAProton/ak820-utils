// Package mole implements whack-a-mole on the physical keyboard: a target
// key lights up, the player presses it before the timer empties. The TUI
// renders the HUD; the keyboard is driven through the ak820 core.
package mole

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/NotAProton/ak820-utils/internal/kb"
)

type tickMsg time.Time

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	barStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("46"))
	barLowStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	hitStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("46"))
	missStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196"))
	goldStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
)

const (
	ambientR, ambientG, ambientB = 6, 2, 22
	moleR, moleG, moleB          = 255, 0, 30
	goldR, goldG, goldB          = 255, 215, 0
	maxLives                     = 3
	barWidth                     = 24
)

// Model is the whack-a-mole bubbletea model.
type Model struct {
	kb        *kb.KB
	rng       *rand.Rand
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
	quitting   bool
	start      time.Time
}

// New builds the model; the playable set is the intersection of the game's
// key choices and the keyboard's addressable keys.
func New(board *kb.KB) Model {
	m := Model{
		kb: board, rng: rand.New(rand.NewSource(time.Now().UnixNano())),
		level: 1, lives: maxLives, start: time.Now(),
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
	return m
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
	return func() tea.Msg {
		target := m.playable[m.rng.Intn(len(m.playable))]
		return spawnRoundMsg{target: target, golden: m.rng.Float64() < 0.15}
	}
}

// Update advances the game.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case spawnRoundMsg:
		m.target = msg.target
		m.golden = msg.golden
		m.roundStart = time.Now()
		m.maxTime = 1.4 - float64(m.level-1)*0.12
		if m.maxTime < 0.45 {
			m.maxTime = 0.45
		}
		m.paint()
		return m, m.tick()

	case tickMsg:
		if m.quitting || m.lives <= 0 || m.target == "" {
			return m, nil
		}
		if m.maxTime-time.Since(m.roundStart).Seconds() <= 0 {
			m.registerMiss("too slow")
			return m, m.after()
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
		react := time.Since(m.roundStart).Milliseconds()
		if name == m.target {
			m.registerHit(react)
		} else {
			m.registerMiss(fmt.Sprintf("pressed %s", label(name)))
		}
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

func (m *Model) registerHit(reactMS int64) {
	m.hits++
	m.combo++
	if m.combo > m.maxCombo {
		m.maxCombo = m.combo
	}
	base := 500
	if m.golden {
		base = 1200
		if m.lives < maxLives {
			m.lives++
		}
	}
	elapsed := time.Since(m.roundStart).Seconds()
	bonus := int((m.maxTime - elapsed) / m.maxTime * 500)
	mult := 1 + m.combo/3
	if mult > 5 {
		mult = 5
	}
	pts := (base + bonus) * mult
	m.score += pts
	m.reactions = append(m.reactions, int(reactMS))
	if m.hits%5 == 0 {
		m.level++
	}
	m.feedback = hitStyle.Render(fmt.Sprintf("HIT %s  +%d  (%d ms)", label(m.target), pts, reactMS))
	m.paint()
}

func (m *Model) registerMiss(why string) {
	m.misses++
	m.combo = 0
	m.lives--
	m.feedback = missStyle.Render(fmt.Sprintf("MISS — %s", why))
}

// paint refreshes the board: ambient everywhere, target lit.
func (m Model) paint() {
	m.kb.SetAll(ambientR, ambientG, ambientB)
	r, g, b := moleR, moleG, moleB
	if m.golden {
		r, g, b = goldR, goldG, goldB
	}
	if slot, ok := m.kb.Key(m.target); ok {
		m.kb.SetSlot(slot, byte(r), byte(g), byte(b))
	}
	_ = m.kb.Flush()
}

// View renders the HUD.
func (m Model) View() string {
	if m.quitting || m.lives <= 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(titleStyle.Render(" WHACK-A-MOLE ") + dimStyle.Render("  q / esc quits") + "\n\n")
	b.WriteString(fmt.Sprintf(" score %d   combo %dx   lives %s   level %d\n",
		m.score, m.combo, hearts(m.lives), m.level))
	if m.target == "" || m.maxTime <= 0 {
		b.WriteString("\n starting…\n")
		return b.String()
	}
	left := m.maxTime - time.Since(m.roundStart).Seconds()
	if left < 0 {
		left = 0
	}
	filled := int(left / m.maxTime * float64(barWidth))
	if filled > barWidth {
		filled = barWidth
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
	barS := barStyle.Render(bar)
	if left < m.maxTime*0.3 {
		barS = barLowStyle.Render(bar)
	}
	target := label(m.target)
	if m.golden {
		target = goldStyle.Render(target + " ★")
	}
	b.WriteString("\n target " + target + "  " + barS + "\n")
	if m.feedback != "" {
		b.WriteString("\n " + m.feedback + "\n")
	}
	return b.String()
}

func hearts(lives int) string {
	if lives < 0 {
		lives = 0
	}
	if lives > maxLives {
		lives = maxLives
	}
	return strings.Repeat("♥", lives) + strings.Repeat("·", maxLives-lives)
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
	st := Stats{Score: m.score, Hits: m.hits, Misses: m.misses,
		Accuracy: m.hits * 100 / max(1, m.hits+m.misses), Level: m.level,
		MaxCombo: m.maxCombo, Duration: time.Since(m.start).Seconds()}
	if len(m.reactions) > 0 {
		sum := 0
		for _, r := range m.reactions {
			sum += r
		}
		st.AvgReaction = sum / len(m.reactions)
	}
	st.HighScore = loadHigh()
	if st.Score > st.HighScore {
		st.NewHigh = true
		st.HighScore = st.Score
		saveHigh(st.Score)
	}
	return st
}

func highPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.Getenv("HOME")
	}
	return filepath.Join(dir, "ak820", "mole_highscore.json")
}

func loadHigh() int {
	b, err := os.ReadFile(highPath())
	if err != nil {
		return 0
	}
	var v struct {
		HighScore int `json:"highscore"`
	}
	_ = json.Unmarshal(b, &v)
	return v.HighScore
}

func saveHigh(score int) {
	p := highPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	b, _ := json.Marshal(map[string]int{"highscore": score})
	_ = os.WriteFile(p, b, 0o644)
}
