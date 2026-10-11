package main

import "testing"

func TestParseVisualizerArgs(t *testing.T) {
	opts, err := parseVisualizerArgs([]string{"--theme", "ocean"})
	if err != nil || opts.theme != "ocean" {
		t.Fatalf("space form: %+v %v", opts, err)
	}
	if !opts.accent {
		t.Fatal("accent must default on")
	}
	opts, err = parseVisualizerArgs([]string{"--theme=fire"})
	if err != nil || opts.theme != "fire" {
		t.Fatalf("equals form: %+v %v", opts, err)
	}
	opts, err = parseVisualizerArgs([]string{"--accent-rainbow=false"})
	if err != nil || opts.accent {
		t.Fatalf("equals false: %+v %v", opts, err)
	}
	opts, err = parseVisualizerArgs([]string{"--no-accent-rainbow"})
	if err != nil || opts.accent {
		t.Fatalf("negation: %+v %v", opts, err)
	}
	opts, err = parseVisualizerArgs([]string{"--accent-rainbow", "--theme", "ocean"})
	if err != nil || !opts.accent || opts.theme != "ocean" {
		t.Fatalf("combined: %+v %v", opts, err)
	}
	if _, err := parseVisualizerArgs([]string{"--accent-rainbow=maybe"}); err == nil {
		t.Fatal("bad accent value accepted")
	}
	if _, err := parseVisualizerArgs([]string{"--theme", "ocena"}); err == nil {
		t.Fatal("typo theme accepted")
	}
	if _, err := parseVisualizerArgs([]string{"--theme=ocean"}); err != nil {
		t.Fatalf("equals valid rejected: %v", err)
	}
	if _, err := parseVisualizerArgs([]string{"--bogus"}); err == nil {
		t.Fatal("unknown flag accepted")
	}
	if _, err := parseVisualizerArgs([]string{"extra"}); err == nil {
		t.Fatal("stray positional accepted")
	}
	if _, err := parseVisualizerArgs([]string{"--theme"}); err == nil {
		t.Fatal("bare --theme accepted")
	}
}

func TestRunRejects(t *testing.T) {
	if run([]string{"mole", "extra"}) != 1 {
		t.Fatal("mole extra args accepted")
	}
	if run([]string{"visualizer", "--theme", "bogus"}) != 1 {
		t.Fatal("bad theme accepted")
	}
	if run([]string{"bogus-cmd"}) != 1 {
		t.Fatal("unknown command accepted")
	}
}
