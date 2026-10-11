package viz

import (
	"testing"
	"time"
)

func TestUnknownThemeRejected(t *testing.T) {
	if _, ok := indexOfTheme("nope-not-a-theme"); ok {
		t.Fatal("unknown theme accepted")
	}
	if _, ok := indexOfTheme("ocean"); !ok {
		t.Fatal("valid theme rejected")
	}
}

func TestDeriveSpectrumShortNoPanic(t *testing.T) {
	var s spectrum
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic on short input: %v", r)
		}
	}()
	deriveSpectrum(&s, []int{1, 2, 3}, 1.0/60)
	deriveSpectrum(&s, nil, -1)
}

func TestGradRGBEmpty(t *testing.T) {
	r, g, b := gradRGB(nil, 0.5)
	if r != 0 || g != 0 || b != 0 {
		t.Fatalf("empty gradient must be black, got %d %d %d", r, g, b)
	}
}

func TestColorIndexClamped(t *testing.T) {
	if colorIndex(-1) != 0 || colorIndex(99) != 5 {
		t.Fatal("colorIndex not clamped")
	}
}

func TestBeatDefaultCooldown(t *testing.T) {
	if DefaultBeatCooldown != 120*time.Millisecond {
		t.Fatalf("cooldown = %v", DefaultBeatCooldown)
	}
}
