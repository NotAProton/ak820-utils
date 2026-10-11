// Command ak820-utils is a collection of playful keyboard front-ends built on
// the ak820 core command. It owns no HID or protocol knowledge: everything
// goes through `ak820 agent-context` (discovery), `ak820 set mode`
// (lighting/mode changes) and `ak820 pipe` (per-key transport).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/NotAProton/ak820-utils/internal/kb"
	"github.com/NotAProton/ak820-utils/internal/mole"
	"github.com/NotAProton/ak820-utils/internal/viz"
)

const version = "0.1.0"

var usage = `ak820-utils — playful front-ends for the ak820 keyboard core

Usage: ak820-utils <command> [args]

Commands:
  mole                        Whack-a-mole on the physical keyboard
  visualizer [--theme T]      Audio spectrum (requires cava); T: ` + themeList() + `
  version                     Print version

Both commands drive the keyboard through the ` + "`ak820`" + ` core binary
(https://github.com/NotAProton/ak820-go-drivers), which must be in PATH.
`

func themeList() string {
	out := ""
	for i, t := range viz.ThemeNames() {
		if i > 0 {
			out += "|"
		}
		out += t
	}
	return out
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Print(usage)
		return 0
	}
	switch args[0] {
	case "version", "--version":
		if len(args) > 1 {
			fmt.Fprintf(os.Stderr, "error: version takes no arguments (got %q)\n\n%s", args[1:], usage)
			return 1
		}
		fmt.Println("ak820-utils", version)
		return 0
	case "help", "--help", "-h":
		fmt.Print(usage)
		return 0
	case "mole":
		if len(args) > 1 {
			fmt.Fprintf(os.Stderr, "error: mole takes no arguments (got %q); valid: ak820-utils mole\n\n%s", args[1:], usage)
			return 1
		}
		return runMole()
	case "visualizer":
		theme, err := parseVisualizerArgs(args[1:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n\n%s", err, usage)
			return 1
		}
		return runVisualizer(theme)
	default:
		fmt.Fprintf(os.Stderr, "error: unknown command %q\n\n%s", args[0], usage)
		return 1
	}
}

// parseVisualizerArgs accepts `--theme T` and `--theme=T`, rejects unknown
// flags and stray positionals so typos never silently run a default.
func parseVisualizerArgs(args []string) (string, error) {
	theme := "synthwave"
	seen := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--theme" {
			if i+1 >= len(args) {
				return "", fmt.Errorf("--theme needs a value; valid: %s", themeList())
			}
			i++
			theme = args[i]
			seen = true
		} else if rest, ok := strings.CutPrefix(a, "--theme="); ok {
			if rest == "" {
				return "", fmt.Errorf("--theme needs a value; valid: %s", themeList())
			}
			theme = rest
			seen = true
		} else if a == "--help" || a == "-h" || a == "help" {
			fmt.Print(usage)
			os.Exit(0)
		} else {
			return "", fmt.Errorf("unknown argument %q for visualizer; valid: --theme %s", a, themeList())
		}
	}
	_ = seen
	valid := false
	for _, t := range viz.ThemeNames() {
		if theme == t {
			valid = true
			break
		}
	}
	if !valid {
		return "", fmt.Errorf("unknown theme %q; valid: %s", theme, themeList())
	}
	return theme, nil
}

func openBoard() (*kb.KB, int) {
	board, err := kb.Open()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return nil, 1
	}
	return board, 0
}

// closeAndRestore releases the pipe (and its device lock) before running
// the restore command. Call it on every exit path once the pipe is open.
func closeAndRestore(board *kb.KB) error {
	board.Close()
	return board.Restore()
}

// withSignalRestore installs SIGINT/SIGTERM handlers that restore the
// keyboard lighting before re-raising the signal. It returns a stop func.
func withSignalRestore(board *kb.KB) func() {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case sig := <-ch:
			// The pipe holds the core's exclusive device lock, so it
			// must be closed before the restore process can open it.
			board.Close()
			_ = board.Restore()
			signal.Stop(ch)
			// Re-raise with default disposition.
			p, _ := os.FindProcess(os.Getpid())
			_ = p.Signal(sig)
		case <-done:
		}
	}()
	return func() {
		close(done)
		signal.Stop(ch)
	}
}

func runMole() (code int) {
	board, c := openBoard()
	if board == nil {
		return c
	}
	defer board.Close()
	stopSignals := withSignalRestore(board)
	defer stopSignals()
	// Panics must still restore the keyboard lighting before crashing.
	defer func() {
		if r := recover(); r != nil {
			_ = closeAndRestore(board)
			panic(r)
		}
	}()
	if err := board.EnterCustomMode(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		_ = board.Restore()
		return 2
	}
	if err := board.SetBrightness(5); err != nil {
		fmt.Fprintln(os.Stderr, "warning: brightness:", err)
	}
	if err := board.StartPipe(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		_ = board.Restore()
		return 2
	}

	model, err := mole.New(board)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		_ = closeAndRestore(board)
		return 2
	}
	prog := tea.NewProgram(model, tea.WithAltScreen())
	final, err := prog.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		_ = closeAndRestore(board)
		return 2
	}
	if rerr := closeAndRestore(board); rerr != nil {
		fmt.Fprintln(os.Stderr, "warning: restore failed:", rerr)
	}

	m, ok := final.(mole.Model)
	if !ok {
		fmt.Fprintln(os.Stderr, "error: unexpected end of game (no stats)")
		return 2
	}
	stats := m.Finish()
	emit(stats)
	if os.Getenv("AK820_UTILS_JSON") == "" {
		high := ""
		if stats.NewHigh {
			high = "  ★ new high score!"
		}
		fmt.Printf("\nscore %d (best %d)%s  hits %d  misses %d  accuracy %d%%  avg reaction %dms\n",
			stats.Score, stats.HighScore, high, stats.Hits, stats.Misses,
			stats.Accuracy, stats.AvgReaction)
	}
	return 0
}

func runVisualizer(theme string) (code int) {
	board, c := openBoard()
	if board == nil {
		return c
	}
	defer board.Close()
	stopSignals := withSignalRestore(board)
	defer stopSignals()
	// Panics must still restore the keyboard lighting before crashing.
	defer func() {
		if r := recover(); r != nil {
			_ = closeAndRestore(board)
			panic(r)
		}
	}()
	if err := board.EnterCustomMode(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		_ = board.Restore()
		return 2
	}
	if err := board.SetBrightness(5); err != nil {
		fmt.Fprintln(os.Stderr, "warning: brightness:", err)
	}
	if err := board.StartPipe(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		_ = board.Restore()
		return 2
	}

	m, err := viz.New(board, theme)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		_ = closeAndRestore(board)
		return 2
	}
	prog := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := prog.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		m.Close()
		_ = closeAndRestore(board)
		return 2
	}
	m.Close()
	if err := closeAndRestore(board); err != nil {
		fmt.Fprintln(os.Stderr, "warning: restore failed:", err)
	}
	return 0
}

func emit(v any) {
	if os.Getenv("AK820_UTILS_JSON") != "" {
		b, err := json.Marshal(v)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error encoding stats:", err)
			return
		}
		fmt.Println(string(b))
	}
}
