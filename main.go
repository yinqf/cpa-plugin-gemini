package main

import (
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
	log "github.com/sirupsen/logrus"
)

const (
	pluginName          = "cpa-plugin-gemini"
	pluginSchemaVersion = 6
	nativeABIVersion    = 1
)

// pluginVersion is injected by the release build.
var pluginVersion = "0.1.0"

var state = newPlugin()

type plugin struct {
	config    atomic.Pointer[config]
	quiescing atomic.Bool
	monitor   monitor
	now       func() time.Time
	startedAt time.Time
}

func newPlugin() *plugin {
	return newPluginWithClock(time.Now)
}

func newPluginWithClock(now func() time.Time) *plugin {
	p := &plugin{now: now, startedAt: now().UTC()}
	cfg := defaultConfig()
	p.config.Store(&cfg)
	return p
}

func main() {}

func (p *plugin) handle(method string, raw []byte) ([]byte, error) {
	response := ojson.NewDocString(`{"ok":true,"result":{}}`)
	switch method {
	case "plugin.register", "plugin.reconfigure":
		root, errRoot := rpcObject(raw)
		if errRoot != nil {
			return nil, errRoot
		}
		if version := root.Get("schema_version"); version.Exists() {
			parsed, errVersion := strconv.ParseUint(version.Raw, 10, 32)
			if version.Type != ojson.Number || errVersion != nil || parsed < pluginSchemaVersion {
				return nil, fmt.Errorf("host RPC schema version %d or newer is required", pluginSchemaVersion)
			}
		} else {
			return nil, fmt.Errorf("host RPC schema version %d or newer is required", pluginSchemaVersion)
		}
		configYAML, errConfig := decodeBytes(root.Get("config_yaml"))
		if errConfig != nil {
			return nil, fmt.Errorf("config_yaml must be Base64-encoded YAML")
		}
		cfg, errParse := parseConfig(configYAML)
		if errParse != nil {
			return nil, errParse
		}
		if errRegister := register(response); errRegister != nil {
			return nil, errRegister
		}
		p.config.Store(&cfg)
		p.quiescing.Store(false)
	case "request.normalize":
		root, errRoot := rpcObject(raw)
		if errRoot != nil {
			return nil, errRoot
		}
		if errNormalize := p.normalize(response, root); errNormalize != nil {
			return nil, errNormalize
		}
	case "management.register":
		if errSet := response.Set("result.Resources", []map[string]string{
			{"Path": "/ui", "Menu": "Antigravity 搬移监控", "Description": "处理、搬移、跳过计数及当前生效配置"},
			{"Path": "/status"},
		}); errSet != nil {
			return nil, fmt.Errorf("register monitor resources: %w", errSet)
		}
	case "management.handle":
		return p.management(raw)
	case "plugin.quiesce", "plugin.shutdown":
		p.quiescing.Store(true)
	default:
		return nil, fmt.Errorf("unsupported plugin method")
	}
	return response.Bytes(), nil
}

func (p *plugin) normalize(response *ojson.Doc, root ojson.Result) error {
	if !strings.EqualFold(strings.TrimSpace(root.Get("ToFormat").String()), "antigravity") {
		return nil
	}
	skip, moved := skipNoChange, ""
	defer func() { p.monitor.record(p.now(), moved, skip) }()
	cfg := p.config.Load()
	switch {
	case p.quiescing.Load():
		skip = skipQuiescing
		return nil
	case !cfg.Enabled:
		skip = skipDisabled
		return nil
	case cfg.Mode == modeOff:
		skip = skipModeOff
		return nil
	case !cfg.matchesModel(root.Get("Model").String()):
		skip = skipModelFiltered
		return nil
	}
	body, errBody := decodeBytes(root.Get("Body"))
	if errBody != nil {
		skip = skipInvalidBody
		return fmt.Errorf("Body must be Base64-encoded bytes")
	}
	if !ojson.ValidBytes(body) || !uniqueObject(ojson.ParseBytes(body)) {
		skip = skipInvalidBody
		return nil
	}
	doc := ojson.NewDoc(body)
	result, errRelocate := relocateDoc(doc, cfg)
	if errRelocate != nil {
		skip = skipError
		return errRelocate
	}
	if result.changed {
		if errSet := response.Set("result.Body", base64.StdEncoding.EncodeToString(doc.Bytes())); errSet != nil {
			skip = skipError
			return fmt.Errorf("encode normalized payload: %w", errSet)
		}
		moved = result.reason
		if cfg.Debug {
			log.WithFields(log.Fields{"plugin": pluginName, "system_chars": result.chars, "reason": result.reason}).Info("system content relocated")
		}
	}
	return nil
}

func rpcObject(raw []byte) (ojson.Result, error) {
	if !ojson.ValidBytes(raw) {
		return ojson.Result{}, fmt.Errorf("invalid RPC JSON")
	}
	root := ojson.ParseBytes(raw)
	if !root.IsObject() {
		return ojson.Result{}, fmt.Errorf("RPC request must be an object")
	}
	return root, nil
}

func decodeBytes(value ojson.Result) ([]byte, error) {
	if !value.Exists() || value.Type == ojson.Null {
		return nil, nil
	}
	if value.Type != ojson.String {
		return nil, fmt.Errorf("byte field must be a Base64 string")
	}
	return base64.StdEncoding.DecodeString(value.String())
}

func register(doc *ojson.Doc) error {
	values := []struct {
		path  string
		value any
	}{
		{"result.schema_version", pluginSchemaVersion},
		{"result.metadata.Name", pluginName},
		{"result.metadata.Version", pluginVersion},
		{"result.metadata.Author", "yinqf"},
		{"result.metadata.GitHubRepository", "https://github.com/yinqf/cpa-plugin-gemini"},
		{"result.metadata.ConfigFields", []map[string]any{
			{"Name": "models", "Type": "string", "Default": "*", "Description": "仅在 Antigravity 内筛选模型。逗号分隔，支持通配符；留空恢复 *。不影响其他供应商。"},
			{"Name": "mode", "Type": "enum", "Default": modeAuto, "EnumValues": []string{modeAuto, modeAlways, modeOff}, "Description": "auto：达到长度阈值或命中自定义触发词时搬移；always：搬移所有可安全处理的文本 system；off：关闭。"},
			{"Name": "keep_max_chars", "Type": "integer", "Default": 2000, "Description": "auto 模式的 Unicode 字符数阈值，默认 2000，范围 0～1000000；0 关闭长度触发。"},
			{"Name": "trigger_words", "Type": "string", "Default": "", "Description": "可选自定义触发词，换行或 | 分隔，不区分大小写。默认无内置触发词。"},
			{"Name": "debug", "Type": "boolean", "Default": false, "Description": "记录搬移结果、字符数和触发原因，不记录提示词、请求正文或触发词。"},
		}},
		{"result.capabilities.request_normalizer", true},
		{"result.capabilities.management_api", true},
	}
	for _, item := range values {
		if errSet := doc.Set(item.path, item.value); errSet != nil {
			return fmt.Errorf("create plugin registration: %w", errSet)
		}
	}
	return nil
}

func rpcFailure() []byte {
	return []byte(`{"ok":false,"error":{"code":"plugin_error","message":"The request could not be processed by cpa-plugin-gemini."}}`)
}
