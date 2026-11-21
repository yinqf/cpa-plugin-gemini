package main

import (
	"bytes"
	"fmt"
	"io"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	modeAuto   = "auto"
	modeAlways = "always"
	modeOff    = "off"
)

type config struct {
	Enabled      bool   `yaml:"enabled"`
	Priority     int    `yaml:"priority"`
	Models       string `yaml:"models"`
	Mode         string `yaml:"mode"`
	KeepMaxChars int    `yaml:"keep_max_chars"`
	TriggerWords string `yaml:"trigger_words"`
	Debug        bool   `yaml:"debug"`

	patterns []string
	triggers []string
}

func defaultConfig() config {
	return config{Enabled: true, Models: "*", Mode: modeAuto, KeepMaxChars: 2000, patterns: []string{"*"}}
}

func parseConfig(data []byte) (config, error) {
	// CPA passes its store manifest alongside the plugin settings.
	input := struct {
		config `yaml:",inline"`
		Store  yaml.Node `yaml:"store"`
	}{config: defaultConfig()}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if errDecode := decoder.Decode(&input); errDecode != nil && errDecode != io.EOF {
		return config{}, fmt.Errorf("decode plugin configuration: %w", errDecode)
	}
	var extra yaml.Node
	if errExtra := decoder.Decode(&extra); errExtra != io.EOF {
		return config{}, fmt.Errorf("configuration must contain one YAML document")
	}
	// yaml.v3 can truncate a floating-point scalar when decoding it into an int.
	var document yaml.Node
	if errNode := yaml.Unmarshal(data, &document); errNode != nil {
		return config{}, fmt.Errorf("decode configuration node: %w", errNode)
	}
	if len(document.Content) > 0 {
		root := document.Content[0]
		if root.Kind != yaml.MappingNode && root.Tag != "!!null" {
			return config{}, fmt.Errorf("configuration must be a YAML mapping")
		}
		if errNumeric := validateIntegerNodes(root, make(map[*yaml.Node]bool)); errNumeric != nil {
			return config{}, errNumeric
		}
	}
	cfg := input.config
	if cfg.KeepMaxChars < 0 || cfg.KeepMaxChars > 1_000_000 {
		return config{}, fmt.Errorf("keep_max_chars must be between 0 and 1000000")
	}
	cfg.Mode = strings.ToLower(strings.TrimSpace(cfg.Mode))
	if cfg.Mode == "" {
		cfg.Mode = modeAuto
	}
	switch cfg.Mode {
	case modeAuto, modeAlways, modeOff:
	default:
		return config{}, fmt.Errorf("mode must be auto, always, or off")
	}
	cfg.Models = strings.TrimSpace(cfg.Models)
	if cfg.Models == "" {
		cfg.Models = "*"
	}
	cfg.patterns = nil
	for _, entry := range strings.Split(cfg.Models, ",") {
		pattern := strings.TrimSpace(entry)
		if pattern == "" {
			return config{}, fmt.Errorf("models must contain non-empty comma-separated patterns")
		}
		if _, errPattern := path.Match(pattern, ""); errPattern != nil {
			return config{}, fmt.Errorf("models contains an invalid glob pattern")
		}
		cfg.patterns = append(cfg.patterns, pattern)
	}
	for _, entry := range strings.FieldsFunc(cfg.TriggerWords, func(r rune) bool { return r == '\n' || r == '\r' || r == '|' }) {
		if word := strings.ToLower(strings.TrimSpace(entry)); word != "" {
			cfg.triggers = append(cfg.triggers, word)
		}
	}
	return cfg, nil
}

func validateIntegerNodes(node *yaml.Node, visited map[*yaml.Node]bool) error {
	if node == nil || visited[node] {
		return nil
	}
	visited[node] = true
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			value := node.Content[i+1]
			switch node.Content[i].Value {
			case "<<":
				if errMerge := validateIntegerNodes(value, visited); errMerge != nil {
					return errMerge
				}
			case "keep_max_chars":
				if value.Kind == yaml.AliasNode {
					value = value.Alias
				}
				if value == nil || (value.Tag != "!!int" && value.Tag != "!!null") {
					return fmt.Errorf("keep_max_chars must be an integer")
				}
			}
		}
	} else if node.Kind == yaml.SequenceNode {
		for _, child := range node.Content {
			if errChild := validateIntegerNodes(child, visited); errChild != nil {
				return errChild
			}
		}
	}
	return validateIntegerNodes(node.Alias, visited)
}

func (c *config) matchesModel(model string) bool {
	for _, pattern := range c.patterns {
		if pattern == "*" {
			return true
		}
		if matched, _ := path.Match(pattern, model); matched {
			return true
		}
	}
	return false
}
