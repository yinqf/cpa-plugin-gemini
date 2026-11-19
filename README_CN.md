# cpa-plugin-gemini

独立的 CLIProxyAPI 原生插件：**只处理目标协议为 Antigravity 的请求**，把文本
system 内容搬入普通 user 消息。覆盖 Antigravity 提供的 Gemini 和 Claude 模型；
相同模型名走 Gemini、Vertex、AI Studio、Claude、Codex 等其他供应商时原样透传。

## 接入方式

插件只注册 `request_normalizer`，实现 `request.normalize`。CPA 先完成账户选择和
入口协议转换，插件再检查 `ToFormat == antigravity`，处理统一结构中的
`request.systemInstruction.parts` 和 `request.contents`。

不修改 CPA 主程序，不注册调度器，不调用上游，也不读取账户凭据。可以与
`cpa-plugin-limit` 同时启用，保留其 `capacity-aware` 调度、限频和并发控制。
原生 ABI 为 1，JSON RPC schema 为 1；直接实现稳定的轻量协议，无需引入整个 CPA
Go 模块。配置表单和构建发布流程参考 `cpa-plugin-limit`。

## 配置

在 CPA 的 **插件管理 → cpa-plugin-gemini → 配置** 中编辑：

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

| 参数 | 默认值 | 含义 |
| --- | --- | --- |
| `models` | `"*"` | Antigravity 范围内的模型过滤；逗号分隔，支持通配符。`*` 匹配所有模型及带前缀模型。 |
| `mode` | `auto` | `auto` 按长度或触发词处理；`always` 处理所有可可靠搬移的文本 system；`off` 关闭。 |
| `keep_max_chars` | `2000` | system 文本块的 Unicode 字符总数达到该值时触发。范围 0～1000000；0 关闭长度触发。 |
| `trigger_words` | 空 | 可选的自定义子串，换行或竖线分隔，不区分大小写。没有内置触发词。 |
| `debug` | `false` | 只记录搬移结果、字符数和触发原因，不记录提示词、模型名、请求正文及命中的词。 |

`auto` 下长度与触发词任一命中即可；两者都关闭时不搬移。`always` 不受长度和触发词
限制。模型或模式留空恢复默认值。`enabled`、`priority` 为 CPA 原有控制字段。
配置通过 CPA 既有机制应用；无效配置不会覆盖上一份有效配置。

## 内容保留和限制

- 复制原始文本块及受支持的缓存元数据，不压平成字符串。已有 user 的文本、图片和
  其他内容块保留；用独立换行文本块分隔搬入内容与原 user 内容。
- 不把 system 插入工具结果、工具调用或带签名的 thinking 消息。没有普通 user 时，
  创建前置 user，原有历史和工具调用顺序保持不变，不添加虚构问题或额外指令。
- 成功插入后才删除原 system；重复处理不会重复插入。兼容 snake_case 字段；双字段
  歧义、未知 system 元数据、非文本 system 或不支持的结构原样透传。
- JSON 使用 `ojson.Doc` 修改，无关字段原始值及大整数精度保留；Base64 使用
  `moego/base64`。不处理响应，不修改请求头或账户调度。
- 搬移角色会改变指令优先级，可能影响模型行为、工具选择及提示词缓存，不能保证与
  原 system 完全等价，也不承诺解决上游 429。
- 后续 CPA 内置逻辑和用户 payload 规则仍可添加 system。例如结构化输出可能追加
  system 指令；插件不保证最终请求没有 `systemInstruction`。
- 回调错误沿用 CPA 的原样透传策略，插件不是权限隔离机制。原生接口限制单次 RPC
  为 64 MiB，拒绝无效指针及超长缓冲区。

## 构建和验证

需要 Go 1.26+ 及 C 编译器：

```sh
gofmt -w .
go vet ./...
go test -race ./...
python3 -m unittest discover -s scripts -p 'test_release_version.py' -v
mkdir -p build
CGO_ENABLED=1 go build -buildmode=c-shared -o build/cpa-plugin-gemini.so .
python3 scripts/smoke_native.py build/cpa-plugin-gemini.so
```

macOS 将最后两条命令的扩展名改为 `.dylib`。必须在 CPA 对应的操作系统和架构构建，
只改扩展名不能得到其他平台产物。单元测试及 ABI 冒烟测试均不连接上游，覆盖供应商
隔离、多入口的已转换请求结构、文本/图片/工具保留、精度、Unicode、配置并发重载、
重复处理和缓冲区校验；不替代 CPA 翻译器测试或真实上游验证。

## 发布和安装

GitHub Actions 参考限流插件：推送 `main`、版本标签或手动运行时检查格式、执行
`go vet`、竞态测试、版本脚本测试，再构建 **Linux amd64** 原生库。
发布工作流选择可重复执行的版本号，注入版本，运行 ABI 冒烟测试，生成 ZIP 和
`checksums.txt`，再发布 GitHub Release。手动运行可选择已有标签并关闭发布。
推送前请留意：`main` 推送会触发自动发布。

解压对应平台的 ZIP，将 `cpa-plugin-gemini.so` 放入 CPA 原生插件目录，例如
`plugins/linux/amd64/`，重载插件或重启 CPA，再启用配置。旧版本宿主需确认已有
`request_normalizer` 回调。`registry.json` 为插件市场元数据。

本地构建不会发布、上传或修改 CPA 配置和账户。
