package kb

import (
	"os"
	"sync"
	"testing"
)

// Concurrent Set/Snapshot/Flush/Close must never panic or deadlock. Flush
// after Close errors; double Close is idempotent.
func TestConcurrentAccessRace(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	k := &KB{
		frames: 2, perFrame: 2, cmdBase: 0xF6,
		slots: [][]byte{{0, 0, 0}, {0, 0, 0}, {0, 0, 0}, {0, 0, 0}},
		stdin: w, stderr: &syncBuffer{},
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k.SetSlot(i%4, byte(i), byte(i), byte(i))
			k.SetAll(byte(i), 0, 1)
			_ = k.snapshot()
			_ = k.Flush()
			_ = k.Diagnostics()
			_ = k.SlotCount()
			_, _ = k.Key("SPACE")
		}(i)
	}
	wg.Wait()
	_ = w.Close()
	k.Close()
	k.Close()
	if err := k.Flush(); err == nil {
		t.Error("Flush after Close must fail")
	}
}

// SetSlot out-of-range must report false under concurrency.
func TestSetSlotBoundsConcurrent(t *testing.T) {
	k := &KB{slots: make([][]byte, 2), stderr: &syncBuffer{}}
	for i := range k.slots {
		k.slots[i] = []byte{0, 0, 0}
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if k.SetSlot(-1, 1, 2, 3) || k.SetSlot(99, 1, 2, 3) {
				t.Error("out-of-range accepted")
			}
		}(i)
	}
	wg.Wait()
}

// Key lookup is case-insensitive and safe on a nil map.
func TestKeyNilMap(t *testing.T) {
	k := &KB{stderr: &syncBuffer{}}
	if _, ok := k.Key("space"); ok {
		t.Error("nil keymap must miss")
	}
}
