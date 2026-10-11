package kb

import (
	"os"
	"sync"
	"testing"
)

func TestSetSlotBounds(t *testing.T) {
	k := &KB{slots: make([][]byte, 4), stderr: &syncBuffer{}}
	for i := range k.slots {
		k.slots[i] = []byte{0, 0, 0}
	}
	if !k.SetSlot(0, 1, 2, 3) {
		t.Fatal("valid slot rejected")
	}
	if k.SetSlot(-1, 1, 2, 3) || k.SetSlot(4, 1, 2, 3) {
		t.Fatal("out-of-range slot accepted")
	}
}

func TestFlushFraming(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	k := &KB{
		frames: 2, perFrame: 2, cmdBase: 0xF6,
		slots: [][]byte{{1, 2, 3}, {4, 5, 6}, {7, 8, 9}, {10, 11, 12}},
		stdin: w, stderr: &syncBuffer{},
	}
	if err := k.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	_ = w.Close()
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	out := string(buf[:n])
	if len(out) == 0 || out[:6] != "0xf6  " && out[:6] != "0xf6 0" {
		t.Fatalf("unexpected framing: %q", out)
	}
	// No trailing-space-only lines for empty payload framing covered by Flush
	// always emitting payload; sendRaw path removed in favor of core CLI.
}

func TestSyncBufferConcurrent(t *testing.T) {
	var sb syncBuffer
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = sb.Write([]byte("x")); _ = sb.String() }()
	}
	wg.Wait()
}

func TestCloseIdempotentNoPipe(t *testing.T) {
	k := &KB{stderr: &syncBuffer{}, closed: true}
	k.Close()
	k.Close()
}
