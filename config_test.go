package main

import "testing"

func TestConfigDefaultsAndHostFields(t *testing.T) {
	for _, input := range []string{"", "{}", "enabled: true\npriority: 20\n", "keep_max_chars: null\nmodels: ''\nmode: ''\n"} {
		cfg, err := parseConfig([]byte(input))
		if err != nil {
			t.Fatalf("parse %q: %v", input, err)
		}
		if !cfg.Enabled || cfg.Mode != modeAuto || cfg.KeepMaxChars != 2000 || !cfg.matchesModel("team/model") || len(cfg.triggers) != 0 {
			t.Fatalf("unexpected defaults for %q: %+v", input, cfg)
		}
	}
}

func TestConfigValidation(t *testing.T) {
	for _, input := range []string{
		"mode: unknown", "keep_max_chars: -1", "keep_max_chars: 1000001",
		"keep_max_chars: 1.5", "keep_max_chars: '2000'", "models: '['", "models: 'a,,b'",
		"models: []", "trigger_words: []", "debug: maybe", "enabled: maybe",
		"unknown: true", "[]", "42", "mode: auto\nmode: off", "{}\n---\n{}", "mode: [",
		"<<: &defaults {keep_max_chars: 1.5}",
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := parseConfig([]byte(input)); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestIntegerThresholdWithYAMLMerge(t *testing.T) {
	cfg, err := parseConfig([]byte("<<: &defaults {keep_max_chars: 20}\nmode: always\n"))
	if err != nil || cfg.KeepMaxChars != 20 {
		t.Fatalf("valid YAML merge: %+v, %v", cfg, err)
	}
}

func TestConfigModelsAndTriggers(t *testing.T) {
	cfg, err := parseConfig([]byte("mode: ' ALWAYS '\nmodels: 'gemini-*, team/claude-?'\ntrigger_words: 'Custom| second'\nkeep_max_chars: 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != modeAlways || cfg.KeepMaxChars != 0 || len(cfg.triggers) != 2 || cfg.triggers[0] != "custom" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	for model, want := range map[string]bool{"gemini-example": true, "team/claude-x": true, "claude-x": false, "team/gemini-example": false} {
		if got := cfg.matchesModel(model); got != want {
			t.Errorf("matchesModel(%q) = %v, want %v", model, got, want)
		}
	}
}
