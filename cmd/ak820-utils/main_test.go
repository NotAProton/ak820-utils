package main

import "testing"

func TestParseVisualizerArgs(t *testing.T) {
	opts, err := parseVisualizerArgs([]string{"--theme", "ocean"})
	if err != nil || opts.theme != "ocean" {
		t.Fatalf("space form: %+v %v", opts, err)
	}
	opts, err = parseVisualizerArgs([]string{"--theme=fire"})
	if err != nil || opts.theme != "fire" {
		t.Fatalf("equals form: %+v %v", opts, err)
	}
	if _, err := parseVisualizerArgs([]string{"--accent-rainbow=false"}); err == nil {
		t.Fatal("removed accent flag accepted")
	}
	if _, err := parseVisualizerArgs([]string{"--accent-rainbow"}); err == nil {
		t.Fatal("removed accent flag accepted")
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
