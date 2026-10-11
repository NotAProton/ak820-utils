// Package kb is the client for the ak820 core command. It discovers the
// key geometry from `ak820 agent-context` and streams per-key frames through
// `ak820 pipe`, so no HID device handling lives in this repository. Mode
// changes go through `ak820 set mode` (pure consumer); the pipe carries only
// per-key frame lines derived from the agent-context geometry.
package kb

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// manifest is the subset of `ak820 agent-context` this client consumes.
type manifest struct {
	PerKey struct {
		Slots        int            `json:"slots"`
		CmdBase      string         `json:"cmd_base"`
		Frames       int            `json:"frames"`
		KeysPerFrame int            `json:"keys_per_frame"`
		Keys         map[string]int `json:"keys"`
	} `json:"per_key"`
}

// syncBuffer is a goroutine-safe bytes.Buffer for the child stderr stream:
// the pipe process writes from its own goroutine while pipeError/Restore/
// Diagnostics read from the TUI goroutine.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// KB streams per-key color updates to the keyboard through the ak820 core.
type KB struct {
	keymap   map[string]int
	cmdBase  byte
	frames   int
	perFrame int
	slots    [][]byte // [slots][3]RGB

	mu     sync.Mutex // guards stdin/proc/closed below
	proc   *exec.Cmd
	stdin  io.WriteCloser
	closed bool

	stderr *syncBuffer

	slotMu sync.RWMutex // guards slots; Flush may run apart from Set*
}

// Open resolves the keymap from the core binary. It does not touch the
// keyboard lighting and does not start the pipe; call EnterCustomMode /
// SetBrightness first, then StartPipe before Flush.
func Open() (*KB, error) {
	if _, err := exec.LookPath("ak820"); err != nil {
		return nil, fmt.Errorf("the ak820 core binary is required in PATH; build it from github.com/NotAProton/ak820-go-drivers (make install): %w", err)
	}
	out, err := exec.Command("ak820", "agent-context").Output()
	if err != nil {
		return nil, fmt.Errorf("ak820 agent-context: %w", err)
	}
	var m manifest
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, fmt.Errorf("parsing ak820 agent-context: %w", err)
	}
	if m.PerKey.Slots <= 0 {
		return nil, fmt.Errorf("ak820 agent-context has no per_key section; upgrade the core binary")
	}
	if m.PerKey.Frames <= 0 || m.PerKey.KeysPerFrame <= 0 {
		return nil, fmt.Errorf("ak820 agent-context has invalid geometry (frames=%d keys_per_frame=%d)", m.PerKey.Frames, m.PerKey.KeysPerFrame)
	}
	baseStr := m.PerKey.CmdBase
	lower := strings.ToLower(baseStr)
	lower = strings.TrimPrefix(lower, "0x")
	if lower == "" {
		return nil, fmt.Errorf("bad per-key cmd base %q", m.PerKey.CmdBase)
	}
	base, err := strconv.ParseUint(lower, 16, 8)
	if err != nil {
		return nil, fmt.Errorf("bad per-key cmd base %q: %w", m.PerKey.CmdBase, err)
	}
	if int(base)+m.PerKey.Frames > 0x100 {
		return nil, fmt.Errorf("per-key cmd range overflows a byte (base %q frames %d)", m.PerKey.CmdBase, m.PerKey.Frames)
	}

	k := &KB{
		keymap:   m.PerKey.Keys,
		cmdBase:  byte(base),
		frames:   m.PerKey.Frames,
		perFrame: m.PerKey.KeysPerFrame,
		slots:    make([][]byte, m.PerKey.Slots),
		stderr:   &syncBuffer{},
	}
	for i := range k.slots {
		k.slots[i] = []byte{0, 0, 0}
	}
	// NOTE: the pipe is NOT started here. Mode changes (EnterCustomMode /
	// SetBrightness) run as separate `ak820` processes and the core holds an
	// exclusive lock on the hidraw node, so the pipe must start after them.
	// Call StartPipe once the mode is set.
	return k, nil
}

// StartPipe starts the persistent `ak820 pipe` process for per-key frames.
// It must be called after EnterCustomMode/SetBrightness and before Flush.
func (k *KB) StartPipe() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return fmt.Errorf("ak820 pipe: closed")
	}
	if k.stdin != nil {
		return nil // already started
	}
	proc := exec.Command("ak820", "pipe", "--yes", "--quiet")
	proc.Stderr = k.stderr
	stdin, err := proc.StdinPipe()
	if err != nil {
		return err
	}
	if err := proc.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("starting ak820 pipe: %w", err)
	}
	k.proc = proc
	k.stdin = stdin
	return nil
}

// Key returns the slot index for a key name.
func (k *KB) Key(name string) (int, bool) {
	s, ok := k.keymap[strings.ToLower(name)]
	return s, ok
}

// SlotCount returns the number of addressable per-key RGB slots.
func (k *KB) SlotCount() int { return len(k.slots) }

// SetSlot queues a color for one slot; Flush transmits. It reports whether
// the slot was valid so geometry mismatches are detectable.
func (k *KB) SetSlot(slot int, r, g, b byte) bool {
	k.slotMu.Lock()
	defer k.slotMu.Unlock()
	if slot < 0 || slot >= len(k.slots) {
		return false
	}
	k.slots[slot] = []byte{r, g, b}
	return true
}

// SetAll queues one color for every slot.
func (k *KB) SetAll(r, g, b byte) {
	k.slotMu.Lock()
	defer k.slotMu.Unlock()
	for i := range k.slots {
		k.slots[i] = []byte{r, g, b}
	}
}

// snapshot copies the slot buffer for transmission without holding the lock
// during I/O.
func (k *KB) snapshot() [][]byte {
	k.slotMu.RLock()
	defer k.slotMu.RUnlock()
	out := make([][]byte, len(k.slots))
	for i, s := range k.slots {
		c := make([]byte, 3)
		copy(c, s)
		out[i] = c
	}
	return out
}

// Flush sends the queued per-key colors through the core pipe.
func (k *KB) Flush() error {
	slots := k.snapshot()
	var buf bytes.Buffer
	for f := 0; f < k.frames; f++ {
		payload := make([]byte, 0, k.perFrame*3)
		for s := f * k.perFrame; s < (f+1)*k.perFrame && s < len(slots); s++ {
			payload = append(payload, slots[s]...)
		}
		if f == k.frames-1 {
			// Last frame may be short when slots is not a multiple of
			// perFrame; pad with zeros to keep frame length stable.
			for len(payload) < k.perFrame*3 {
				payload = append(payload, 0)
			}
		}
		if len(payload) == 0 {
			fmt.Fprintf(&buf, "0x%02x\n", int(k.cmdBase)+f)
		} else {
			fmt.Fprintf(&buf, "0x%02x %s\n", int(k.cmdBase)+f, hex.EncodeToString(payload))
		}
	}
	k.mu.Lock()
	w := k.stdin
	closed := k.closed
	k.mu.Unlock()
	if closed || w == nil {
		return fmt.Errorf("ak820 pipe: closed")
	}
	if _, err := w.Write(buf.Bytes()); err != nil {
		return k.pipeError(err)
	}
	return nil
}

// EnterCustomMode switches the keyboard to custom per-key lighting via the
// core CLI (no protocol bytes owned here). The pipe stays open for the
// per-key frames that follow.
func (k *KB) EnterCustomMode() error {
	cmd := exec.Command("ak820", "set", "mode", "24",
		"--brightness", "5", "--speed", "5", "--quiet")
	var eb syncBuffer
	cmd.Stderr = &eb
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(eb.String())
		if detail != "" {
			return fmt.Errorf("ak820 set mode 24: %w: %s", err, detail)
		}
		return fmt.Errorf("ak820 set mode 24: %w", err)
	}
	return nil
}

// SetBrightness sets the board's master backlight to level (1-5) while
// staying in custom per-key mode, via the core CLI.
func (k *KB) SetBrightness(level int) error {
	if level < 1 {
		level = 1
	}
	if level > 5 {
		level = 5
	}
	cmd := exec.Command("ak820", "set", "mode", "24",
		"--brightness", strconv.Itoa(level), "--no-apply", "--quiet")
	var eb syncBuffer
	cmd.Stderr = &eb
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(eb.String())
		if detail != "" {
			return fmt.Errorf("ak820 set mode 24 --brightness %d: %w: %s", level, err, detail)
		}
		return fmt.Errorf("ak820 set mode 24 --brightness %d: %w", level, err)
	}
	return nil
}

// Restore returns the keyboard to the factory-default lighting. It is
// independent of the pipe lifecycle so it works before or after Close and
// from signal handlers.
func (k *KB) Restore() error {
	var eb syncBuffer
	cmd := exec.Command("ak820", "set", "mode", "1", "--quiet")
	cmd.Stderr = &eb
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(eb.String())
		if detail != "" {
			return fmt.Errorf("ak820 set mode 1: %w (%s)", err, detail)
		}
		return fmt.Errorf("ak820 set mode 1: %w", err)
	}
	return nil
}

func (k *KB) pipeError(err error) error {
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(k.stderr.String())
	if detail != "" {
		return fmt.Errorf("ak820 pipe: %w: %s", err, detail)
	}
	return fmt.Errorf("ak820 pipe: %w", err)
}

// Close stops the pipe process. It is idempotent. The mutex is not held
// across proc.Wait so a concurrent Flush cannot stall teardown.
func (k *KB) Close() {
	k.mu.Lock()
	w := k.stdin
	p := k.proc
	k.stdin = nil
	k.proc = nil
	k.closed = true
	k.mu.Unlock()
	if w != nil {
		_ = w.Close()
	}
	if p != nil {
		_ = p.Wait()
	}
}

// Diagnostics exposes the pipe stderr for troubleshooting.
func (k *KB) Diagnostics() string { return strings.TrimSpace(k.stderr.String()) }
