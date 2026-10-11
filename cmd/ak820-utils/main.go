// Command ak820-utils is a collection of playful keyboard front-ends built on
// the ak820 core command. It owns no protocol knowledge: everything goes
// through `ak820 agent-context` (discovery) and `ak820 pipe` (transport).
package main

import (
	"encoding/json"
	"fmt"
	"os"

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
		fmt.Println("ak820-utils", version)
		return 0
	case "help", "--help", "-h":
		fmt.Print(usage)
		return 0
	case "mole":
		return runMole()
	case "visualizer":
		theme := "synthwave"
		for i := 1; i < len(args); i++ {
			if args[i] == "--theme" && i+1 < len(args) {
				i++
				theme = args[i]
			}
		}
		return runVisualizer(theme)
	default:
		fmt.Fprintf(os.Stderr, "error: unknown command %q\n\n%s", args[0], usage)
		return 1
	}
}

func openBoard() (*kb.KB, int) {
	board, err := kb.Open()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return nil, 1
	}
	return board, 0
}

func runMole() int {
	board, code := openBoard()
	if board == nil {
		return code
	}
	defer board.Close()

	prog := tea.NewProgram(mole.New(board), tea.WithAltScreen())
	final, err := prog.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	board.Close()
	_ = board.Restore()

	m, ok := final.(mole.Model)
	if !ok {
		return 0
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

func runVisualizer(theme string) int {
	board, code := openBoard()
	if board == nil {
		return code
	}
	defer board.Close()

	m, err := viz.New(board, theme)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		board.Close()
		return 2
	}
	prog := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := prog.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		m.Close()
		return 2
	}
	m.Close()
	board.Close()
	if err := board.Restore(); err != nil {
		fmt.Fprintln(os.Stderr, "warning: restore failed:", err)
	}
	return 0
}

func emit(v any) {
	if os.Getenv("AK820_UTILS_JSON") != "" {
		b, _ := json.Marshal(v)
		fmt.Println(string(b))
	}
}
