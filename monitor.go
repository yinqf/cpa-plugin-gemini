package main

import (
	_ "embed"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
)

const (
	skipNoChange      = "no_change"
	skipDisabled      = "disabled"
	skipModeOff       = "mode_off"
	skipModelFiltered = "model_filtered"
	skipQuiescing     = "quiescing"
	skipInvalidBody   = "invalid_body"
	skipError         = "error"
)

// Only aggregate counters and timestamps are retained, never request metadata,
// model selections, headers, payloads, or the matched trigger word.
type monitor struct {
	mu            sync.Mutex
	processed     uint64
	relocated     uint64
	skipped       uint64
	moveReasons   map[string]uint64
	skipReasons   map[string]uint64
	lastProcessed time.Time
}

func (m *monitor) record(now time.Time, moved, skipped string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.processed++
	m.lastProcessed = now.UTC()
	if moved != "" {
		m.relocated++
		if m.moveReasons == nil {
			m.moveReasons = make(map[string]uint64)
		}
		m.moveReasons[moved]++
	} else {
		m.skipped++
		if m.skipReasons == nil {
			m.skipReasons = make(map[string]uint64)
		}
		m.skipReasons[skipped]++
	}
}

type statusField struct {
	path  string
	value any
}

func (p *plugin) status() ([]byte, error) {
	now := p.now().UTC()
	cfg := p.config.Load()
	doc := ojson.NewDocString(`{"last_processed_at":null,"move_reasons":{},"skip_reasons":{},"config":{}}`)
	uptime := max(0, int64(now.Sub(p.startedAt)/time.Second))
	values := []statusField{
		{"plugin", pluginName}, {"version", pluginVersion}, {"scope", "antigravity"},
		{"observed_at", now.Format(time.RFC3339Nano)}, {"started_at", p.startedAt.Format(time.RFC3339Nano)},
		{"uptime_seconds", uptime}, {"quiescing", p.quiescing.Load()},
		{"config.enabled", cfg.Enabled}, {"config.priority", cfg.Priority},
		{"config.models", cfg.Models}, {"config.mode", cfg.Mode},
		{"config.keep_max_chars", cfg.KeepMaxChars}, {"config.trigger_word_count", len(cfg.triggers)},
		{"config.debug", cfg.Debug},
	}
	// Take counters as one completed snapshot so processed == relocated + skipped
	// remains true even while normalization and status polling run concurrently.
	p.monitor.mu.Lock()
	values = append(values, []statusField{
		{"processed", p.monitor.processed}, {"relocated", p.monitor.relocated}, {"skipped", p.monitor.skipped},
	}...)
	if !p.monitor.lastProcessed.IsZero() {
		values = append(values, statusField{"last_processed_at", p.monitor.lastProcessed.Format(time.RFC3339Nano)})
	}
	for _, reason := range []string{"always", "length", "trigger"} {
		values = append(values, statusField{"move_reasons." + reason, p.monitor.moveReasons[reason]})
	}
	for _, reason := range []string{skipNoChange, skipDisabled, skipModeOff, skipModelFiltered, skipQuiescing, skipInvalidBody, skipError} {
		values = append(values, statusField{"skip_reasons." + reason, p.monitor.skipReasons[reason]})
	}
	p.monitor.mu.Unlock()
	for _, item := range values {
		if errSet := doc.Set(item.path, item.value); errSet != nil {
			return nil, fmt.Errorf("encode monitor status: %w", errSet)
		}
	}
	return doc.Bytes(), nil
}

//go:embed ui.html
var statusPage []byte

func (p *plugin) management(raw []byte) ([]byte, error) {
	root, errRoot := rpcObject(raw)
	if errRoot != nil {
		return resourceResponse(http.StatusBadRequest, "application/json", []byte(`{"error":"invalid resource request"}`))
	}
	method := root.Get("Method")
	if method.Exists() && (method.Type != ojson.String || (method.String() != "" && method.String() != http.MethodGet)) {
		return resourceResponse(http.StatusMethodNotAllowed, "application/json", []byte(`{"error":"method not allowed"}`))
	}
	path := strings.TrimSuffix(root.Get("Path").String(), "/")
	path = strings.TrimPrefix(path, "/v0/resource/plugins/"+pluginName)
	switch path {
	case "/ui":
		return resourceResponse(http.StatusOK, "text/html; charset=utf-8", statusPage)
	case "/status":
		body, errStatus := p.status()
		if errStatus != nil {
			return nil, errStatus
		}
		return resourceResponse(http.StatusOK, "application/json; charset=utf-8", body)
	default:
		return resourceResponse(http.StatusNotFound, "application/json", []byte(`{"error":"resource not found"}`))
	}
}

func resourceResponse(status int, contentType string, body []byte) ([]byte, error) {
	doc := ojson.NewDocString(`{"ok":true,"result":{"Headers":{}}}`)
	values := map[string]any{
		"result.StatusCode":                      status,
		"result.Headers.Content-Type":            []string{contentType},
		"result.Headers.Cache-Control":           []string{"no-store"},
		"result.Headers.X-Content-Type-Options":  []string{"nosniff"},
		"result.Headers.Referrer-Policy":         []string{"no-referrer"},
		"result.Headers.Content-Security-Policy": []string{"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'"},
		"result.Body":                            base64.StdEncoding.EncodeToString(body),
	}
	if status == http.StatusMethodNotAllowed {
		values["result.Headers.Allow"] = []string{http.MethodGet}
	}
	for path, value := range values {
		if errSet := doc.Set(path, value); errSet != nil {
			return nil, fmt.Errorf("encode monitor response: %w", errSet)
		}
	}
	return doc.Bytes(), nil
}
