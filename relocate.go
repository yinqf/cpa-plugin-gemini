package main

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/MoeForge/moego/ojson"
)

type relocation struct {
	changed bool
	chars   int
	reason  string
}

// relocateDoc changes only the translated Antigravity request. Callers discard
// the document on error and serialize it once, after all mutations succeed.
func relocateDoc(doc *ojson.Doc, cfg *config) (relocation, error) {
	result := relocation{}
	if !uniqueObject(doc.Get("request")) {
		return result, nil
	}
	systemPath := "request.systemInstruction"
	system := doc.Get(systemPath)
	alias := doc.Get("request.system_instruction")
	if system.Exists() && alias.Exists() {
		return result, nil
	}
	if !system.Exists() && alias.Exists() {
		systemPath, system = "request.system_instruction", alias
	}
	if !uniqueObject(system) {
		return result, nil
	}
	validSystem := true
	system.ForEach(func(key, value ojson.Result) bool {
		if key.String() != "parts" && (key.String() != "role" || value.Type != ojson.String) {
			validSystem = false
		}
		return validSystem
	})
	parts := system.Get("parts")
	if !validSystem || !parts.IsArray() || len(parts.Array()) == 0 {
		return result, nil
	}
	var text strings.Builder
	for _, part := range parts.Array() {
		if !uniqueObject(part) || part.Get("text").Type != ojson.String {
			return result, nil
		}
		validPart := true
		part.ForEach(func(key, _ ojson.Result) bool {
			switch key.String() {
			case "text", "cache_control", "cacheControl":
			default:
				validPart = false
			}
			return validPart
		})
		if !validPart {
			return result, nil
		}
		value := part.Get("text").String()
		result.chars += utf8.RuneCountInString(value)
		text.WriteString(value)
		text.WriteByte('\n')
	}
	if strings.TrimSpace(text.String()) == "" {
		return result, nil
	}
	switch {
	case cfg.Mode == modeAlways:
		result.reason = "always"
	case cfg.KeepMaxChars > 0 && result.chars >= cfg.KeepMaxChars:
		result.reason = "length"
	default:
		lower := strings.ToLower(text.String())
		for _, word := range cfg.triggers {
			if strings.Contains(lower, word) {
				result.reason = "trigger"
				break
			}
		}
	}
	if result.reason == "" {
		return result, nil
	}
	contents := doc.Get("request.contents")
	if contents.Exists() && !contents.IsArray() {
		return relocation{}, nil
	}
	target := -1
	items := contents.Array()
	for i, content := range items {
		if !uniqueObject(content) || !content.Get("parts").IsArray() {
			return relocation{}, nil
		}
		role := content.Get("role")
		if role.Exists() && (role.Type != ojson.String || (role.String() != "user" && role.String() != "model" && role.String() != "")) {
			return relocation{}, nil
		}
		ordinary := role.String() == "user" || role.String() == ""
		for _, part := range content.Get("parts").Array() {
			if !uniqueObject(part) {
				return relocation{}, nil
			}
			if part.Get("functionResponse").Exists() || part.Get("function_response").Exists() ||
				part.Get("functionCall").Exists() || part.Get("function_call").Exists() ||
				part.Get("thoughtSignature").Exists() || part.Get("thought_signature").Exists() || part.Get("thought").Bool() {
				ordinary = false
			}
		}
		if target < 0 && ordinary {
			target = i
		}
	}
	if target >= 0 {
		partPath := "request.contents." + strconv.Itoa(target) + ".parts"
		// Copy the original JSON parts without flattening text or decoding numbers.
		combined := append([]ojson.Result(nil), parts.Array()...)
		original := items[target].Get("parts").Array()
		if len(original) > 0 {
			combined = append(combined, ojson.Parse(`{"text":"\n\n"}`))
		}
		combined = append(combined, original...)
		if errSet := doc.SetRawBytes(partPath, rawArray(combined)); errSet != nil {
			return relocation{}, fmt.Errorf("insert system parts: %w", errSet)
		}
	} else {
		user := ojson.NewDocString(`{"role":"user","parts":[]}`)
		if errSet := user.SetRaw("parts", parts.Raw); errSet != nil {
			return relocation{}, fmt.Errorf("create user content: %w", errSet)
		}
		// Append the child document directly; the business payload is serialized only
		// at the caller's final exit. Original tool-call/result turns stay adjacent.
		out := user.AppendTo([]byte{'['})
		for _, item := range items {
			out = append(out, ',')
			out = append(out, item.Raw...)
		}
		out = append(out, ']')
		if errSet := doc.SetRawBytes("request.contents", out); errSet != nil {
			return relocation{}, fmt.Errorf("insert user content: %w", errSet)
		}
	}
	// Removing the source is the last mutation, after successful insertion.
	if errDelete := doc.Delete(systemPath); errDelete != nil {
		return relocation{}, fmt.Errorf("remove relocated system: %w", errDelete)
	}
	result.changed = true
	return result, nil
}

// Duplicate keys make field selection ambiguous, so leave such structures alone.
func uniqueObject(value ojson.Result) bool {
	if !value.IsObject() {
		return false
	}
	seen := make(map[string]struct{})
	valid := true
	value.ForEach(func(key, _ ojson.Result) bool {
		if _, exists := seen[key.String()]; exists {
			valid = false
			return false
		}
		seen[key.String()] = struct{}{}
		return true
	})
	return valid
}

func rawArray(items []ojson.Result) []byte {
	out := []byte{'['}
	for i, item := range items {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, item.Raw...)
	}
	return append(out, ']')
}
