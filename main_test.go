package main

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
	log "github.com/sirupsen/logrus"
)

const exampleBody = `{"model":"gemini-example","request":{"systemInstruction":{"parts":[{"text":"Keep answers concise."}]},"contents":[{"role":"user","parts":[{"text":"Hello"}]}]}}`

func setTest(t *testing.T, doc *ojson.Doc, path string, value any) {
	t.Helper()
	if err := doc.Set(path, value); err != nil {
		t.Fatal(err)
	}
}

func callTest(t *testing.T, p *plugin, method string, request *ojson.Doc) ojson.Result {
	t.Helper()
	raw, err := p.handle(method, request.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !ojson.ValidBytes(raw) || !ojson.GetBytes(raw, "ok").Bool() {
		t.Fatalf("invalid response: %s", raw)
	}
	return ojson.GetBytes(raw, "result")
}

func configureTest(t *testing.T, p *plugin, yaml string) {
	t.Helper()
	req := ojson.NewDocString(`{"schema_version":10}`)
	setTest(t, req, "config_yaml", base64.StdEncoding.EncodeToString([]byte(yaml)))
	callTest(t, p, "plugin.reconfigure", req)
}

func normalizationTestRequest(t *testing.T, to, model, from string, body []byte, stream bool) *ojson.Doc {
	t.Helper()
	doc := ojson.NewDocString(`{}`)
	for path, value := range map[string]any{"ToFormat": to, "FromFormat": from, "Model": model, "Stream": stream, "Body": base64.StdEncoding.EncodeToString(body)} {
		setTest(t, doc, path, value)
	}
	return doc
}

func normalizeTest(t *testing.T, p *plugin, to, model, from string, body []byte, stream bool) ([]byte, bool) {
	t.Helper()
	result := callTest(t, p, "request.normalize", normalizationTestRequest(t, to, model, from, body, stream))
	if !result.Get("Body").Exists() {
		return body, false
	}
	out, err := base64.StdEncoding.DecodeString(result.Get("Body").String())
	if err != nil || !ojson.ValidBytes(out) {
		t.Fatalf("invalid normalized body: %v", err)
	}
	return out, true
}

func TestRegistrationAdvertisesOnlyNormalizer(t *testing.T) {
	result := callTest(t, newPlugin(), "plugin.register", ojson.NewDocString(`{"schema_version":10}`))
	if result.Get("metadata.Name").String() != pluginName || result.Get("metadata.Version").String() != pluginVersion || result.Get("schema_version").Int() != 1 {
		t.Fatalf("unexpected registration: %s", result.Raw)
	}
	capabilities := result.Get("capabilities").Map()
	if len(capabilities) != 1 || !capabilities["request_normalizer"].Bool() {
		t.Fatalf("unexpected capabilities: %v", capabilities)
	}
	fields := map[string]ojson.Result{}
	for _, field := range result.Get("metadata.ConfigFields").Array() {
		fields[field.Get("Name").String()] = field
	}
	if len(fields) != 5 || fields["models"].Get("Default").String() != "*" || fields["mode"].Get("Default").String() != modeAuto || fields["keep_max_chars"].Get("Default").Int() != 2000 {
		t.Fatalf("unexpected configuration fields: %v", fields)
	}
	if fields["keep_max_chars"].Get("Type").String() != "integer" || fields["trigger_words"].Get("Default").String() != "" || fields["debug"].Get("Default").Bool() {
		t.Fatal("invalid configuration field defaults")
	}
}

func TestOnlyAntigravityIsChanged(t *testing.T) {
	p := newPlugin()
	configureTest(t, p, "mode: always")
	for _, to := range []string{"antigravity", " Antigravity ", "gemini", "vertex", "aistudio", "claude", "codex", "openai", "antigravity-extra", ""} {
		t.Run(to, func(t *testing.T) {
			out, changed := normalizeTest(t, p, to, "gemini-example", "openai", []byte(exampleBody), false)
			want := strings.EqualFold(strings.TrimSpace(to), "antigravity")
			if changed != want {
				t.Fatalf("changed = %v, want %v", changed, want)
			}
			if !want && string(out) != exampleBody {
				t.Fatal("non-Antigravity payload changed")
			}
		})
	}
}

func TestTranslatedOriginsAndStreamingShareNormalizer(t *testing.T) {
	p := newPlugin()
	configureTest(t, p, "mode: always")
	for _, source := range []string{"openai", "openai-response", "claude", "gemini", "codex", "interactions"} {
		for _, stream := range []bool{false, true} {
			out, changed := normalizeTest(t, p, "antigravity", "claude-example", source, []byte(exampleBody), stream)
			if !changed || ojson.GetBytes(out, "request.systemInstruction").Exists() || ojson.GetBytes(out, "request.contents.0.parts.0.text").String() != "Keep answers concise." {
				t.Fatalf("source=%s stream=%v: %s", source, stream, out)
			}
		}
	}
}

func TestConfigFilteringAndAtomicReconfigure(t *testing.T) {
	p := newPlugin()
	for _, cfg := range []string{"mode: off", "mode: always\nenabled: false", "mode: always\nmodels: claude-*"} {
		configureTest(t, p, cfg)
		if _, changed := normalizeTest(t, p, "antigravity", "gemini-example", "gemini", []byte(exampleBody), false); changed {
			t.Fatalf("filter ignored: %s", cfg)
		}
	}
	configureTest(t, p, "mode: always")
	bad := ojson.NewDocString(`{}`)
	setTest(t, bad, "config_yaml", base64.StdEncoding.EncodeToString([]byte("mode: invalid")))
	if _, err := p.handle("plugin.reconfigure", bad.Bytes()); err == nil {
		t.Fatal("invalid configuration accepted")
	}
	if _, changed := normalizeTest(t, p, "antigravity", "gemini-example", "gemini", []byte(exampleBody), false); !changed {
		t.Fatal("invalid reconfiguration replaced the previous configuration")
	}
	for _, method := range []string{"plugin.quiesce", "plugin.shutdown"} {
		callTest(t, p, method, ojson.NewDocString(`{}`))
		if _, changed := normalizeTest(t, p, "antigravity", "gemini-example", "gemini", []byte(exampleBody), false); changed {
			t.Fatal("quiesced plugin changed a request")
		}
		configureTest(t, p, "mode: always")
	}
}

func TestRPCValidationAndPrivacy(t *testing.T) {
	p := newPlugin()
	for _, tc := range []struct{ method, body string }{
		{"unknown", `{}`}, {"plugin.register", `[]`}, {"plugin.register", `{`},
		{"plugin.register", `{"schema_version":0}`}, {"plugin.register", `{"schema_version":"10"}`},
		{"plugin.register", `{"schema_version":1.5}`},
		{"plugin.register", `{"config_yaml":"not base64"}`}, {"plugin.register", `{"config_yaml":42}`},
		{"request.normalize", `null`}, {"request.normalize", `{`},
	} {
		if _, err := p.handle(tc.method, []byte(tc.body)); err == nil {
			t.Errorf("accepted invalid RPC: %s %s", tc.method, tc.body)
		}
	}
	configureTest(t, p, "mode: always\ndebug: true")
	var output bytes.Buffer
	logger := log.StandardLogger()
	previous := logger.Out
	logger.SetOutput(&output)
	t.Cleanup(func() { logger.SetOutput(previous) })
	body := strings.ReplaceAll(exampleBody, "Keep answers concise.", "private-prompt-unique")
	normalizeTest(t, p, "antigravity", "private-model-unique", "gemini", []byte(body), false)
	if strings.Contains(output.String(), "private-") || !strings.Contains(output.String(), "system_chars") {
		t.Fatalf("unexpected log content: %s", output.String())
	}
	if !ojson.ValidBytes(rpcFailure()) || ojson.GetBytes(rpcFailure(), "ok").Bool() {
		t.Fatal("invalid native failure envelope")
	}
}

func TestConcurrentReconfigureAndNormalize(t *testing.T) {
	p := newPlugin()
	request := normalizationTestRequest(t, "antigravity", "gemini-example", "gemini", []byte(exampleBody), true).Bytes()
	cfg := ojson.NewDocString(`{}`)
	setTest(t, cfg, "config_yaml", base64.StdEncoding.EncodeToString([]byte("mode: always")))
	configuration := cfg.Bytes()
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Go(func() {
			for j := 0; j < 50; j++ {
				if _, err := p.handle("plugin.reconfigure", configuration); err != nil {
					t.Error(err)
				}
				if raw, err := p.handle("request.normalize", request); err != nil || !ojson.ValidBytes(raw) {
					t.Errorf("normalize: %v", err)
				}
			}
		})
	}
	workers.Wait()
}
