// Package kb is the client for the ak820 core command. It discovers the
// key geometry from `ak820 agent-context` and streams per-key frames through
// `ak820 pipe`, so no device or protocol knowledge lives in this repository.
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

// KB streams per-key color updates to the keyboard through the ak820 core.
type KB struct {
	keymap   map[string]int
	cmdBase  byte
	frames   int
	perFrame int
	slots    [][]byte // [slots][3]RGB

	mu     sync.Mutex
	proc   *exec.Cmd
	stdin  io.WriteCloser
	stderr *bytes.Buffer
}

// Open resolves the keymap and starts a persistent pipe to the core binary.
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
	if m.PerKey.Slots == 0 {
		return nil, fmt.Errorf("ak820 agent-context has no per_key section; upgrade the core binary")
	}
	base, err := strconv.ParseUint(strings.TrimPrefix(m.PerKey.CmdBase, "0x"), 16, 8)
	if err != nil {
		return nil, fmt.Errorf("bad per-key cmd base %q: %w", m.PerKey.CmdBase, err)
	}

	k := &KB{
		keymap:   m.PerKey.Keys,
		cmdBase:  byte(base),
		frames:   m.PerKey.Frames,
		perFrame: m.PerKey.KeysPerFrame,
		slots:    make([][]byte, m.PerKey.Slots),
		stderr:   &bytes.Buffer{},
	}
	for i := range k.slots {
		k.slots[i] = []byte{0, 0, 0}
	}

	k.proc = exec.Command("ak820", "pipe", "--yes", "--quiet")
	k.proc.Stderr = k.stderr
	stdin, err := k.proc.StdinPipe()
	if err != nil {
		return nil, err
	}
	k.stdin = stdin
	if err := k.proc.Start(); err != nil {
		return nil, fmt.Errorf("starting ak820 pipe: %w", err)
	}
	return k, nil
}

// Key returns the slot index for a key name.
func (k *KB) Key(name string) (int, bool) {
	s, ok := k.keymap[strings.ToLower(name)]
	return s, ok
}

// SlotCount returns the number of addressable per-key RGB slots.
func (k *KB) SlotCount() int { return len(k.slots) }

// SetSlot queues a color for one slot; Flush transmits.
func (k *KB) SetSlot(slot int, r, g, b byte) {
	if slot < 0 || slot >= len(k.slots) {
		return
	}
	k.slots[slot] = []byte{r, g, b}
}

// SetAll queues one color for every slot.
func (k *KB) SetAll(r, g, b byte) {
	for i := range k.slots {
		k.slots[i] = []byte{r, g, b}
	}
}

// Flush sends the queued per-key colors through the core pipe.
func (k *KB) Flush() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	var buf bytes.Buffer
	for f := 0; f < k.frames; f++ {
		payload := make([]byte, 0, k.perFrame*3)
		for s := f * k.perFrame; s < (f+1)*k.perFrame && s < len(k.slots); s++ {
			payload = append(payload, k.slots[s]...)
		}
		fmt.Fprintf(&buf, "%02x %s\n", int(k.cmdBase)+f, hex.EncodeToString(payload))
	}
	if _, err := k.stdin.Write(buf.Bytes()); err != nil {
		return k.pipeError(err)
	}
	return nil
}

// sendRaw queues nothing; it writes one explicit command line (cmd + payload).
func (k *KB) sendRaw(cmd byte, payload []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	_, err := fmt.Fprintf(k.stdin, "%02x %s\n", cmd, hex.EncodeToString(payload))
	return k.pipeError(err)
}

// EnterCustomMode switches the keyboard to custom per-key lighting.
func (k *KB) EnterCustomMode() error {
	if err := k.sendRaw(0x07, []byte{0x17, 4, 3, 0, 0}); err != nil {
		return err
	}
	if err := k.sendRaw(0x0B, nil); err != nil {
		return err
	}
	return k.sendRaw(0x09, nil)
}

// Restore returns the keyboard to the factory-default lighting.
func (k *KB) Restore() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.stdin == nil {
		return nil
	}
	cmd := exec.Command("ak820", "set", "mode", "1", "--quiet")
	cmd.Stderr = k.stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ak820 set mode 1: %w (%s)", err, strings.TrimSpace(k.stderr.String()))
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

// Close stops the pipe process.
func (k *KB) Close() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.stdin != nil {
		k.stdin.Close()
		k.stdin = nil
	}
	if k.proc != nil {
		_ = k.proc.Wait()
	}
}

// Scanner exposes the pipe stderr for diagnostics.
func (k *KB) Diagnostics() string { return strings.TrimSpace(k.stderr.String()) }
