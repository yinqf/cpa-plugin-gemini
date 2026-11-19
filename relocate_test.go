package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestRelocationPreservesRawPartsAndUnrelatedFields(t *testing.T) {
	p := newPlugin()
	configureTest(t, p, "mode: always")
	body := []byte(`{ "project":"private-project", "seed":9007199254740993, "decimal":1.2300e+12, "requestType":"agent", "request":{"systemInstruction":{"role":"user","parts":[{"text":"First rule.","cache_control":{"type":"ephemeral"}},{"text":"Second rule."}]},"tools":[{"functionDeclarations":[{"name":"lookup"}]}],"generationConfig":{"temperature":0.2500},"contents":[{"role":"user","opaque":{"n":9007199254740993},"parts":[{"inlineData":{"mimeType":"image/png","data":"AQID"}},{"text":"Hello"}]}]}}`)
	before := append([]byte(nil), body...)
	out, changed := normalizeTest(t, p, "antigravity", "gemini-example", "openai", body, true)
	if !changed || !bytes.Equal(before, body) {
		t.Fatal("relocation failed or mutated the caller's buffer")
	}
	for _, path := range []string{"project", "seed", "decimal", "requestType", "request.tools", "request.generationConfig", "request.contents.0.opaque"} {
		if got, want := ojson.GetBytes(out, path).Raw, ojson.GetBytes(before, path).Raw; got != want {
			t.Errorf("%s changed: got %s, want %s", path, got, want)
		}
	}
	parts := ojson.GetBytes(out, "request.contents.0.parts").Array()
	if len(parts) != 5 || parts[0].Raw != ojson.GetBytes(before, "request.systemInstruction.parts.0").Raw || parts[1].Raw != ojson.GetBytes(before, "request.systemInstruction.parts.1").Raw || parts[3].Raw != ojson.GetBytes(before, "request.contents.0.parts.0").Raw || parts[4].Raw != ojson.GetBytes(before, "request.contents.0.parts.1").Raw {
		t.Fatalf("content parts were not preserved: %s", out)
	}
	again, changedAgain := normalizeTest(t, p, "antigravity", "gemini-example", "openai", out, true)
	if changedAgain || !bytes.Equal(again, out) {
		t.Fatal("repeated normalization changed the request")
	}
}

func TestRelocationWithoutOrdinaryUserPreservesHistory(t *testing.T) {
	p := newPlugin()
	configureTest(t, p, "mode: always")
	for _, history := range []string{
		`[]`,
		`[{"role":"model","parts":[{"text":"Earlier answer"}]}]`,
		`[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{"value":1}}}]}]`,
	} {
		body := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"Keep answers concise."}]},"contents":` + history + `}}`)
		out, changed := normalizeTest(t, p, "antigravity", "gemini-example", "gemini", body, false)
		contents := ojson.GetBytes(out, "request.contents").Array()
		if !changed || len(contents) != len(ojson.Parse(history).Array())+1 || contents[0].Get("role").String() != "user" || contents[0].Get("parts.0.text").String() != "Keep answers concise." {
			t.Fatalf("system content was not preserved: %s", out)
		}
		for i, old := range ojson.Parse(history).Array() {
			if old.Raw != contents[i+1].Raw {
				t.Fatal("existing tool/history turn changed")
			}
		}
	}
	body := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"Keep answers concise."}]}}}`)
	if out, changed := normalizeTest(t, p, "antigravity", "gemini-example", "gemini", body, false); !changed || ojson.GetBytes(out, "request.contents.0.parts.0.text").String() != "Keep answers concise." {
		t.Fatalf("missing contents: %s", out)
	}
}

func TestRelocationSkipsToolResultsAndKeepsOrdinaryUser(t *testing.T) {
	p := newPlugin()
	configureTest(t, p, "mode: always")
	body := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"Rule"}]},"contents":[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{}}}]},{"role":"user","parts":[{"text":"Question"}]}]}}`)
	out, changed := normalizeTest(t, p, "antigravity", "gemini-example", "claude", body, false)
	if !changed || len(ojson.GetBytes(out, "request.contents").Array()) != 3 || ojson.GetBytes(out, "request.contents.2.parts.0.text").String() != "Rule" {
		t.Fatalf("wrong relocation target: %s", out)
	}
	for _, path := range []string{"request.contents.0", "request.contents.1"} {
		if ojson.GetBytes(out, path).Raw != ojson.GetBytes(body, path).Raw {
			t.Fatal("tool exchange changed")
		}
	}
}

func TestAutoModeUnicodeThresholdAndExplicitTriggers(t *testing.T) {
	p := newPlugin()
	body := []byte(strings.ReplaceAll(exampleBody, "Keep answers concise.", "你好🙂"))
	for _, tc := range []struct {
		config  string
		changed bool
	}{
		{"mode: auto\nkeep_max_chars: 4", false},
		{"mode: auto\nkeep_max_chars: 3", true},
		{"mode: auto\nkeep_max_chars: 0", false},
		{"mode: auto\nkeep_max_chars: 0\ntrigger_words: '你好'", true},
	} {
		configureTest(t, p, tc.config)
		if _, changed := normalizeTest(t, p, "antigravity", "gemini-example", "gemini", body, false); changed != tc.changed {
			t.Fatalf("config %q: changed=%v", tc.config, changed)
		}
	}
	configureTest(t, p, "keep_max_chars: 0\ntrigger_words: CONCISE")
	if _, changed := normalizeTest(t, p, "antigravity", "gemini-example", "gemini", []byte(exampleBody), false); !changed {
		t.Fatal("case-insensitive custom trigger did not match")
	}
}

func TestUnsupportedOrMalformedPayloadsRemainUntouched(t *testing.T) {
	p := newPlugin()
	configureTest(t, p, "mode: always")
	for _, body := range []string{
		``, `{`, `[]`, `null`, `{}`, `{"messages":[{"role":"system","content":"Rule"}]}`,
		`{"request":{"contents":[]}}`,
		`{"request":{"systemInstruction":{"parts":[]},"contents":[]}}`,
		`{"request":{"systemInstruction":{"parts":[{"text":"   "}]},"contents":[]}}`,
		`{"request":{"systemInstruction":{"parts":[{"inlineData":{"data":"AQID"}}]},"contents":[]}}`,
		`{"request":{"systemInstruction":{"parts":[{"text":"Rule","thoughtSignature":"signed"}]},"contents":[]}}`,
		`{"request":{"systemInstruction":{"unknown":"keep","parts":[{"text":"Rule"}]},"contents":[]}}`,
		`{"request":{"systemInstruction":{"parts":[{"text":"Rule"}]},"system_instruction":{"parts":[{"text":"Other"}]},"contents":[]}}`,
		`{"request":{"systemInstruction":{"parts":[{"text":"Rule"}]},"contents":null}}`,
		`{"request":{"systemInstruction":{"parts":[{"text":"Rule"}]},"contents":"invalid"}}`,
		`{"request":{"systemInstruction":{"parts":[{"text":"Rule"}]},"contents":[{"role":"invalid","parts":[]}]}}`,
		`{"request":{"systemInstruction":{"parts":[{"text":"Rule"}]},"contents":[{"role":"user","parts":[42]}]}}`,
		`{"request":{"systemInstruction":{"parts":[{"text":"Rule"}]},"contents":[{"role":"model","parts":[42]}]}}`,
		`{"request":{"systemInstruction":{"parts":[{"text":42}]},"contents":[]}}`,
		`{"request":{"systemInstruction":{"parts":[{"text":"Rule"},{"text":"Different","text":"Ambiguous"}]},"contents":[]}}`,
		`{"request":{"systemInstruction":{"parts":[{"text":"Rule"}]},"systemInstruction":{"parts":[{"text":"Other"}]},"contents":[]}}`,
		`{"request":{"systemInstruction":{"parts":[{"text":"Rule"}]},"contents":[{"role":"user","role":"model","parts":[]}]}}`,
	} {
		out, changed := normalizeTest(t, p, "antigravity", "gemini-example", "gemini", []byte(body), false)
		if changed || string(out) != body {
			t.Fatalf("unsupported structure changed: %s", body)
		}
	}
}

func TestSystemInstructionAliasAndEmptyUser(t *testing.T) {
	p := newPlugin()
	configureTest(t, p, "mode: always")
	for _, body := range []string{
		`{"request":{"system_instruction":{"parts":[{"text":"Rule"}]},"contents":[{"role":"user","parts":[]}]}}`,
		`{"request":{"systemInstruction":{"parts":[{"text":"Rule"}]},"contents":[{"parts":[]}]}}`,
	} {
		out, changed := normalizeTest(t, p, "antigravity", "gemini-example", "gemini", []byte(body), false)
		if !changed || ojson.GetBytes(out, "request.system_instruction").Exists() || ojson.GetBytes(out, "request.systemInstruction").Exists() || ojson.GetBytes(out, "request.contents.0.parts.0.text").String() != "Rule" {
			t.Fatalf("relocation failed: %s", out)
		}
	}
}

func FuzzNormalizationPreservesValidJSON(f *testing.F) {
	f.Add([]byte(exampleBody))
	f.Add([]byte(`{"request":{"systemInstruction":{"parts":[{"text":"Rule"}]}}}`))
	f.Add([]byte(`{"request":{"contents":[]}}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 1<<20 {
			t.Skip()
		}
		p := newPlugin()
		configureTest(t, p, "mode: always")
		original := append([]byte(nil), body...)
		out, changed := normalizeTest(t, p, "antigravity", "gemini-example", "gemini", body, false)
		if !bytes.Equal(body, original) {
			t.Fatal("input mutated")
		}
		if changed {
			if !ojson.ValidBytes(out) {
				t.Fatal("invalid output JSON")
			}
			if _, again := normalizeTest(t, p, "antigravity", "gemini-example", "gemini", out, false); again {
				t.Fatal("normalization is not idempotent")
			}
		}
	})
}
