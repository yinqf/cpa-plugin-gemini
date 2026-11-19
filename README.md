# cpa-plugin-gemini

A standalone CLIProxyAPI native plugin that relocates textual system content into
a user turn **only when the selected upstream protocol is Antigravity**. It works
with both Gemini and Claude models served by Antigravity. Other providers pass
through unchanged, even when their model names are identical.

[中文说明](README_CN.md)

## Integration

The plugin declares only `request_normalizer` and implements `request.normalize`.
CPA translates the client protocol first; this plugin then processes
`request.systemInstruction.parts` and `request.contents` in the translated
Antigravity envelope. OpenAI Chat, Responses, Claude Messages, Gemini, and
Interactions entry protocols use the same normalization path when CPA supports
their translation. No CPA source changes or upstream credentials are required by
the plugin. The native C ABI version is 1 and the stable JSON RPC schema is 1;
the plugin implements that small wire contract directly instead of linking a
copy of the CPA server or depending on its Go module version.

`cpa-plugin-limit` can remain enabled in `capacity-aware` mode. This plugin does
not register a scheduler, alter headers, or handle responses. It uses the same
native ABI, configuration metadata, and build/release layout as that plugin.

## Configuration

Use **Plugin management → cpa-plugin-gemini → Configuration** in the CPA panel.
The plugin exposes model filtering, mode, length threshold, custom trigger
words, and debug logging. Invalid configuration is rejected without replacing
the last valid configuration.

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    cpa-plugin-gemini:
      enabled: true
      priority: 0
      models: "*"
      mode: auto
      keep_max_chars: 2000
      trigger_words: ""
      debug: false
```

| Setting | Default | Behavior |
| --- | --- | --- |
| `models` | `"*"` | Comma-separated model glob patterns, applied only within Antigravity. `*` matches all models, including prefixed names. |
| `mode` | `auto` | `auto`, `always`, or `off`. |
| `keep_max_chars` | `2000` | In `auto`, relocate when the sum of Unicode characters in system text parts reaches this threshold. Range 0–1,000,000; 0 disables the length rule. |
| `trigger_words` | empty | Optional case-insensitive substrings separated by newlines or `\|`. No built-in trigger list. |
| `debug` | `false` | Log relocation counts and reason categories; never log payloads, prompts, models, or matching trigger words. |

In `auto`, length and custom trigger rules are alternatives. With both disabled,
requests pass through. In `always`, every safely supported textual system is
relocated. Empty model/mode fields restore their defaults. `enabled` and
`priority` are standard CPA plugin controls. Changes apply via CPA's existing
plugin configuration/reload mechanism.

## Payload behavior and boundaries

- Original text parts and supported cache metadata are copied as raw JSON into
  the first ordinary user content. A separate newline text part separates them
  from pre-existing user parts. Images and other existing user parts are kept.
- Tool results, tool calls, and signed/thinking parts are not relocation targets.
  If there is no ordinary user turn, a new leading user turn is inserted while
  retaining every existing turn in order. No artificial instruction or user
  question is added.
- The source system field is deleted only after insertion succeeds. Repeating
  normalization does not insert it again. The snake-case system field is also
  accepted; ambiguous duplicate fields, unknown system metadata, non-text system
  parts, and unsupported/malformed structures pass through.
- No system content, `mode: off`, a disabled plugin, or a non-Antigravity target
  returns an empty `PayloadResponse`, which tells CPA to keep the original body.
- JSON payload mutations use `ojson.Doc`, preserving unrelated raw fields and
  large integer precision. Base64 operations use `moego/base64`.
- Role relocation changes instruction priority and may change model behavior or
  prompt cache reuse. This plugin does not promise an upstream acceptance rate
  or diagnose the cause of upstream 429 responses.
- Normalization runs before later built-in changes and the final user payload
  rules. Structured-output support or user rules can add system instructions
  afterward. The plugin does not enforce an empty final system field.
- Normalizer failures follow CPA's existing pass-through behavior; this is a
  compatibility transformation, not an authorization boundary. The C ABI rejects
  requests larger than 64 MiB and invalid buffers without copying them.

## Build and test

Go 1.26+ and a C compiler are required for native builds. No network calls to an
upstream model are made by the tests or the native smoke test.

```sh
gofmt -w .
go vet ./...
go test -race ./...
python3 -m unittest discover -s scripts -p 'test_release_version.py' -v
mkdir -p build
CGO_ENABLED=1 go build -buildmode=c-shared -o build/cpa-plugin-gemini.so .
python3 scripts/smoke_native.py build/cpa-plugin-gemini.so
```

On macOS, use `.dylib` in the last two commands. Build on the operating system
and architecture of the CPA process; changing the extension does not cross-build
the library. Tests cover target isolation, the translated payload contract for
multiple entry protocols, text/media/tool preservation, integer precision,
Unicode thresholds, malformed data, configuration reload, concurrency, repeated
normalization, lifecycle, and native buffer validation. They do not assert live
provider behavior or re-test CPA's protocol translators.

## Install and release

GitHub Actions follows `cpa-plugin-limit`: pushes to `main`, version tags, or
manual runs validate the project and build **Linux amd64**. Release runs choose
an idempotent version, inject it into `main.pluginVersion`, run the native smoke
test, and publish a ZIP plus `checksums.txt`. A manual run can build an existing
tag without publishing. Review these workflows before pushing if automated
publication is not desired.

Extract `cpa-plugin-gemini.so` from the matching release ZIP into CPA's native
plugin directory for Linux amd64 (commonly `plugins/linux/amd64/`). Reload plugins
or restart CPA, then enable and configure `cpa-plugin-gemini`. It requires the
existing CPA `request_normalizer` hook; verify that hook on older host builds.

The included `registry.json` is plugin-store metadata. No release, upload,
credential changes, or CPA configuration edits are performed by local builds.
