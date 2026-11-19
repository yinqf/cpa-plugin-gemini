package main

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
)

func statusTest(t *testing.T, p *plugin) ojson.Result {
	t.Helper()
	raw, err := p.status()
	if err != nil || !ojson.ValidBytes(raw) {
		t.Fatalf("status error: %v", err)
	}
	return ojson.ParseBytes(raw)
}

func resourceTest(t *testing.T, p *plugin, method, path string) (ojson.Result, []byte) {
	t.Helper()
	req := ojson.NewDocString(`{}`)
	setTest(t, req, "Method", method)
	setTest(t, req, "Path", path)
	result := callTest(t, p, "management.handle", req)
	body, err := base64.StdEncoding.DecodeString(result.Get("Body").String())
	if err != nil {
		t.Fatal(err)
	}
	return result, body
}

func assertTotalsTest(t *testing.T, value ojson.Result, processed, relocated, skipped int64) {
	t.Helper()
	if value.Get("processed").Int() != processed || value.Get("relocated").Int() != relocated || value.Get("skipped").Int() != skipped {
		t.Fatalf("unexpected counters: %s", value.Raw)
	}
}

func TestMonitorCountsOnlyCompletedAntigravityCallbacks(t *testing.T) {
	p := newPlugin()
	configureTest(t, p, "mode: always")
	for _, to := range []string{"codex", "gemini", "claude", "", "vertex"} {
		normalizeTest(t, p, to, "gemini-example", "openai", []byte(exampleBody), false)
	}
	assertTotalsTest(t, statusTest(t, p), 0, 0, 0)
	normalizeTest(t, p, "antigravity", "gemini-example", "openai", []byte(exampleBody), false)
	normalizeTest(t, p, "antigravity", "gemini-example", "openai", []byte(`{"request":{"contents":[]}}`), true)
	normalizeTest(t, p, "antigravity", "gemini-example", "openai", []byte(`{`), false)
	bad := normalizationTestRequest(t, "antigravity", "gemini-example", "openai", nil, false)
	setTest(t, bad, "Body", "not-base64!")
	if _, err := p.handle("request.normalize", bad.Bytes()); err == nil {
		t.Fatal("invalid Base64 accepted")
	}
	if _, err := p.handle("request.normalize", []byte(`{`)); err == nil {
		t.Fatal("invalid RPC accepted")
	}
	snapshot := statusTest(t, p)
	assertTotalsTest(t, snapshot, 4, 1, 3)
	if snapshot.Get("move_reasons.always").Int() != 1 || snapshot.Get("skip_reasons.no_change").Int() != 1 || snapshot.Get("skip_reasons.invalid_body").Int() != 2 {
		t.Fatalf("wrong reason counts: %s", snapshot.Raw)
	}
	// Reading the page, polling status, and registering resources are not requests.
	resourceTest(t, p, "GET", "/ui")
	resourceTest(t, p, "GET", "/status")
	callTest(t, p, "management.register", ojson.NewDocString(`{}`))
	assertTotalsTest(t, statusTest(t, p), 4, 1, 3)
}

func TestMonitorReasonsAndConfigurationReload(t *testing.T) {
	p := newPlugin()
	for _, tc := range []struct{ config, reason string }{
		{"mode: auto\nkeep_max_chars: 2", "length"},
		{"mode: auto\nkeep_max_chars: 0\ntrigger_words: concise", "trigger"},
		{"mode: always", "always"},
	} {
		configureTest(t, p, tc.config)
		normalizeTest(t, p, "antigravity", "gemini-example", "openai", []byte(exampleBody), false)
		if statusTest(t, p).Get("move_reasons."+tc.reason).Int() != 1 {
			t.Fatalf("missing reason: %s", tc.reason)
		}
	}
	for _, tc := range []struct{ config, reason string }{
		{"mode: off", skipModeOff},
		{"enabled: false\nmode: always", skipDisabled},
		{"mode: always\nmodels: claude-*", skipModelFiltered},
	} {
		configureTest(t, p, tc.config)
		normalizeTest(t, p, "antigravity", "gemini-example", "openai", []byte(exampleBody), false)
		if statusTest(t, p).Get("skip_reasons."+tc.reason).Int() != 1 {
			t.Fatalf("missing reason: %s", tc.reason)
		}
	}
	configureTest(t, p, "mode: always\nmodels: 'gemini-*, <custom>&'\npriority: 7\nkeep_max_chars: 23\ntrigger_words: 'secret-rule|another-rule'\n")
	callTest(t, p, "plugin.quiesce", ojson.NewDocString(`{}`))
	normalizeTest(t, p, "antigravity", "gemini-example", "openai", []byte(exampleBody), false)
	before := statusTest(t, p)
	assertTotalsTest(t, before, 7, 3, 4)
	if !before.Get("quiescing").Bool() || before.Get("skip_reasons.quiescing").Int() != 1 {
		t.Fatal("quiescing not reflected")
	}
	configureTest(t, p, "mode: auto\nmodels: 'gemini-*, <custom>&'\npriority: 7\nkeep_max_chars: 23\ntrigger_words: 'secret-rule|another-rule'\n")
	result, body := resourceTest(t, p, "GET", "/status")
	after := ojson.ParseBytes(body)
	assertTotalsTest(t, after, 7, 3, 4)
	if after.Get("quiescing").Bool() || after.Get("config.mode").String() != modeAuto || after.Get("config.priority").Int() != 7 || after.Get("config.keep_max_chars").Int() != 23 || after.Get("config.trigger_word_count").Int() != 2 || after.Get("config.models").String() != "gemini-*, <custom>&" {
		t.Fatalf("wrong effective config: %s", after.Raw)
	}
	if result.Get("StatusCode").Int() != 200 || before.Get("started_at").String() != after.Get("started_at").String() {
		t.Fatal("configuration reload reset the monitor")
	}
	bad := ojson.NewDocString(`{"schema_version":10}`)
	setTest(t, bad, "config_yaml", base64.StdEncoding.EncodeToString([]byte("mode: invalid")))
	if _, err := p.handle("plugin.reconfigure", bad.Bytes()); err == nil {
		t.Fatal("invalid configuration accepted")
	}
	if statusTest(t, p).Get("config").Raw != after.Get("config").Raw {
		t.Fatal("failed reload changed reported effective configuration")
	}
}

func TestMonitorClockAndFreshInstance(t *testing.T) {
	now := time.Date(2026, 11, 19, 4, 0, 0, 0, time.UTC)
	p := newPluginWithClock(func() time.Time { return now })
	first := statusTest(t, p)
	if first.Get("last_processed_at").Type != ojson.Null || first.Get("uptime_seconds").Int() != 0 {
		t.Fatal("new monitor is not empty")
	}
	configureTest(t, p, "mode: always")
	now = now.Add(75 * time.Second)
	normalizeTest(t, p, "antigravity", "gemini-example", "gemini", []byte(exampleBody), false)
	after := statusTest(t, p)
	if after.Get("uptime_seconds").Int() != 75 || after.Get("last_processed_at").String() != now.Format(time.RFC3339Nano) || after.Get("started_at").String() != first.Get("started_at").String() {
		t.Fatalf("unexpected timestamps: %s", after.Raw)
	}
	now = now.Add(-100 * time.Second)
	if statusTest(t, p).Get("uptime_seconds").Int() != 0 {
		t.Fatal("negative uptime after clock rollback")
	}
	assertTotalsTest(t, statusTest(t, newPluginWithClock(func() time.Time { return now })), 0, 0, 0)
}

func TestMonitorResourceContractAndPrivacy(t *testing.T) {
	p := newPlugin()
	configureTest(t, p, "mode: always\ntrigger_words: 'private-trigger-unique'\n")
	secret := strings.ReplaceAll(exampleBody, "Keep answers concise.", "private-body-unique")
	req := normalizationTestRequest(t, "antigravity", "private-observed-model", "openai", []byte(secret), false)
	setTest(t, req, "RequestID", "private-request-id")
	setTest(t, req, "Headers.Authorization", "private-api-key")
	callTest(t, p, "request.normalize", req)
	registered := callTest(t, p, "management.register", ojson.NewDocString(`{}`))
	resources := registered.Get("Resources").Array()
	if len(resources) != 2 || resources[0].Get("Path").String() != "/ui" || resources[0].Get("Menu").String() == "" || resources[1].Get("Path").String() != "/status" || registered.Get("Routes").Exists() {
		t.Fatalf("wrong resources: %s", registered.Raw)
	}
	for _, path := range []string{"/status", "/status/", "/v0/resource/plugins/cpa-plugin-gemini/status"} {
		response, body := resourceTest(t, p, "GET", path)
		if response.Get("StatusCode").Int() != 200 || response.Get("Headers.Cache-Control.0").String() != "no-store" || response.Get("Headers.X-Content-Type-Options.0").String() != "nosniff" {
			t.Fatalf("wrong response for %s", path)
		}
		if strings.Contains(string(body), "private-") || strings.Contains(string(body), "trigger_words") || strings.Contains(string(body), "Headers") {
			t.Fatalf("private data exposed: %s", body)
		}
	}
	response, page := resourceTest(t, p, "GET", "/v0/resource/plugins/cpa-plugin-gemini/ui/")
	if response.Get("StatusCode").Int() != 200 || !strings.HasPrefix(response.Get("Headers.Content-Type.0").String(), "text/html") || !strings.Contains(string(page), "System 搬移监控") || !strings.Contains(string(page), "data-monitor") {
		t.Fatal("monitor page missing")
	}
	if !strings.Contains(response.Get("Headers.Content-Security-Policy.0").String(), "connect-src 'self'") {
		t.Fatal("monitor can connect outside its origin")
	}
	for _, tc := range []struct {
		method, path string
		status       int64
	}{
		{"POST", "/status", 405}, {"DELETE", "/ui", 405}, {"GET", "/missing", 404},
		{"GET", "/v0/resource/plugins/cpa-plugin-gemini-other/ui", 404},
	} {
		response, _ := resourceTest(t, p, tc.method, tc.path)
		if response.Get("StatusCode").Int() != tc.status {
			t.Fatalf("wrong status for %+v", tc)
		}
		if tc.status == 405 && response.Get("Headers.Allow.0").String() != "GET" {
			t.Fatal("missing allowed method")
		}
	}
	raw, err := p.handle("management.handle", []byte(`{`))
	if err != nil || ojson.GetBytes(raw, "result.StatusCode").Int() != 400 {
		t.Fatal("invalid resource RPC not rejected")
	}
	assertTotalsTest(t, statusTest(t, p), 1, 1, 0)
}

func TestConcurrentMonitoringAndNormalization(t *testing.T) {
	p := newPlugin()
	configureTest(t, p, "mode: always")
	request := normalizationTestRequest(t, "antigravity", "gemini-example", "gemini", []byte(exampleBody), false).Bytes()
	var workers sync.WaitGroup
	for i := 0; i < 6; i++ {
		workers.Go(func() {
			for j := 0; j < 75; j++ {
				if _, err := p.handle("request.normalize", request); err != nil {
					t.Error(err)
				}
			}
		})
	}
	workers.Go(func() {
		for i := 0; i < 75; i++ {
			snapshot := statusTest(t, p)
			if snapshot.Get("processed").Int() != snapshot.Get("relocated").Int()+snapshot.Get("skipped").Int() {
				t.Error("incoherent concurrent counter snapshot")
			}
		}
	})
	workers.Wait()
	assertTotalsTest(t, statusTest(t, p), 450, 450, 0)
}
