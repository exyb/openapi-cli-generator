# 运行时框架详解

运行时框架（`cli` 包及其依赖包）是生成的 CLI 代码在执行时调用的库。它提供配置管理、HTTP 客户端、认证、格式化、中间件等基础设施。

## cli/cli.go — 框架初始化

### 全局变量

| 变量 | 类型 | 说明 |
|------|------|------|
| `Root` | `*cobra.Command` | Cobra 根命令 |
| `Cache` | `*viper.Viper` | 缓存实例（Token 等临时数据） |
| `Client` | `*gentleman.Client` | HTTP 客户端 |
| `Formatter` | `ResponseFormatter` | 响应格式化器 |
| `PreRun` | `func(*cobra.Command, []string) error` | 命令执行前钩子 |
| `Stdout` | `io.Writer` | 跨平台彩色安全输出 |
| `Stderr` | `io.Writer` | 跨平台彩色安全错误输出 |

### Init 函数

`Init(config *Config)` 是框架的核心初始化函数，执行以下步骤：

1. **初始化配置** (`initConfig`)
   - 创建 `~/.appname/` 配置目录
   - 加载配置文件（`/etc/appname/` 和 `~/.appname/`）
   - 设置环境变量前缀和自动绑定

2. **初始化缓存** (`initCache`)
   - 创建 `~/.appname/cache.json` 缓存文件
   - 用于存储 Token 等临时数据

3. **TTY 检测**
   - 检测 stdout 是否为终端
   - 支持 `--color` 强制开启和 `--nocolor` 强制关闭
   - TTY 模式下使用 `go-colorable` 跨平台彩色输出

4. **初始化日志**
   - 设置 zerolog 输出为自定义 `ConsoleWriter`
   - 默认级别 `WarnLevel`，`--verbose` 时切换为 `DebugLevel`

5. **初始化 HTTP 客户端**
   - 创建 gentleman 客户端
   - 安装 `UserAgentMiddleware`（设置 User-Agent 头）
   - 安装 `LogMiddleware`（verbose 模式下记录请求/响应详情）

6. **初始化格式化器**
   - 创建 `DefaultFormatter`，传入 TTY 标志

7. **创建根命令**
   - 设置 `PersistentPreRunE`：verbose 时打印配置（隐藏 secret/password 字段）
   - 设置自定义帮助模板

8. **注册内置命令**
   - `help-config` — 显示配置帮助
   - `help-input` — 显示输入帮助
   - `search` — 按关键字搜索命令
   - `tree` — 树形显示命令结构
   - `mcp serve` — 启动 MCP 服务器

9. **注册全局标志**
   - `--verbose` / `-v` — 启用详细日志
   - `--output-format` / `-o` — 输出格式（json/yaml）
   - `--query` / `-q` — JMESPath 查询过滤
   - `--raw` — 原始输出（不转义）
   - `--server` — 覆盖服务器 URL

### 内置命令详解

#### search

```bash
my-cli search whitelist
# 输出: policy whitelist list # 策略白名单列表
```

搜索逻辑：
- 关键字与命令完整路径、名称、别名、短描述进行匹配
- 所有关键字必须匹配（AND 逻辑）
- 输出格式：`command-path # short-description`

#### tree

```bash
my-cli tree -L 2
```

树形显示逻辑：
- 使用 `├──`/`��──`/`│` 绘制树形结构
- 按名称去重
- 过滤内置命令（help/setup/help-config/help-input/search/tree/mcp）
- `-L` 控制显示深度（0=无限制）

## cli/credentials.go — 凭据管理

### AuthHandler 接口

```go
type AuthHandler interface {
    ProfileKeys() []string
    OnRequest(log *zerolog.Logger, request *http.Request) error
}
```

所有认证处理器必须实现此接口：
- `ProfileKeys()` — 返回 profile 中需要存储的键名列表
- `OnRequest()` — 在 HTTP 请求发出前注入认证信息

### 认证注册流程

```go
account.Init("MY_APP", account.ServerURL(...))
// 内部调用: cli.UseAuth("account", &account.Handler{...})
```

`UseAuth()` 执行：
1. 调用 `initAuth()` 懒初始化认证系统
2. 注册 Handler 到 `AuthHandlers` 映射
3. 创建 `setup add-profile <type>` 子命令
4. 安装认证中间件到 gentleman 客户端

### 认证中间件

在 `initAuth()` 中安装的 gentleman 请求中间件：

```go
Client.UseRequest(func(ctx *context.Context, h context.Handler) {
    profile := GetProfile()
    handler := AuthHandlers[profile["type"]]
    handler.OnRequest(ctx.Get("log").(*zerolog.Logger), ctx.Request)
    h.Next(ctx)
})
```

### Profile 系统

- 凭据存储在 `~/.appname/credentials.json`
- 支持多个命名 profile（默认 `default`）
- `--profile` 全局标志切换当前 profile
- `GetProfile()` 返回 `map[string]string`

### Profile 管理命令

```bash
# 添加 account 类型 profile
my-cli setup add-profile account my-profile <credentials> <login-url>

# 添加 apikey 类型 profile
my-cli setup add-profile apikey my-profile <api-key>

# 列出所有 profile
my-cli setup list-profiles
```

## cli/flags.go — 标志管理

### 全局标志

`AddGlobalFlag()` 在根命令上添加持久标志，自动绑定 viper：

```go
AddGlobalFlag("verbose", "", "Enable verbose log output", false)
```

内部逻辑：
1. `viper.SetDefault(name, defaultValue)` — 设置默认值
2. 根据类型调用 `Root.PersistentFlags().BoolP/IntP/StringP(...)`
3. `viper.BindPFlag(name, flags.Lookup(name))` — 绑定到 viper

### 自定义标志

`AddFlag()` 为特定命令路径注册自定义标志：

```go
cli.AddFlag("my-server list", "limit", "l", "Max items to return", 100)
```

`SetCustomFlags()` 在命令创建时被调用，将注册的自定义标志绑定到 cobra 命令。

## cli/formatter.go — 输出格式化

### 格式化流程

```
输入数据 (interface{})
    │
    ▼ JMESPath 查询（如果 --query 设置）
过滤后数据
    │
    ▼ --raw 模式处理
    │   ├── string → 原样输出
    │   └── []scalar → 逐行输出
    │
    ▼ 编码
    │   ├── --output-format=yaml → yaml.Marshal
    │   └── --output-format=json → json.MarshalIndent
    │
    ▼ 语法高亮（TTY 模式）
    │   └── chroma.Quick.Highlight(lexer, "terminal256", "cli-dark")
    │
    ▼ 输出到 Stdout
```

### cli-dark 主题

自定义 256 色语法高亮主题：

| 元素 | 颜色 | 用途 |
|------|------|------|
| Keyword | `#ff5f87` | JSON true/false/null |
| NameTag | `#5fafd7` | JSON 键名 |
| Number | `#d78700` | 数字 |
| String | `#afd787` | 字符串值 |
| Comment/Punctuation | `#9e9e9e` | 注释/标点 |

## cli/http.go — HTTP 客户端

### 中间件链

gentleman HTTP 客户端的中间件执行顺序：

```
请求方向 →
1. UserAgentMiddleware     → 设置 User-Agent: appname-cli-version
2. LogMiddleware (request) → 生成 request-id，记录请求详情
3. 认证中间件              → 注入 Authorization 头
4. LogMiddleware (handler) → 记录请求开始时间，缓存请求体
5. [HTTP 请求发送]

← 响应方向
6. LogMiddleware (response) → 记录响应详情和耗时
```

### UnmarshalResponse

```go
func UnmarshalResponse(resp *gentleman.Response, s interface{}) error
```

- 状态码 >= 400 → 返回错误信息
- 根据 Content-Type 选择 JSON 或 YAML 解码
- 不识别的 Content-Type → 返回错误

## cli/input.go — 请求体输入

### GetBody 流程

```
GetBody(mediaType, args)
    │
    ├── 检查 stdin 是否有数据
    │       └── 有 → ioutil.ReadAll(os.Stdin) → body
    │
    ├── 检查 args 是否有 shorthand 参数
    │       └── 有 → shorthand.ParseAndBuild(args) → result
    │
    ├── 合并 stdin 和 shorthand
    │       ├── JSON: json.Unmarshal(stdin) → DeepAssign(result) → json.Marshal
    │       └── YAML: yaml.Unmarshal(stdin) → DeepAssign(result) → yaml.Marshal
    │
    └── 返回合并后的 body 字符串
```

### DeepAssign

递归合并 source map 到 target map，类似深度版本的 `Object.assign`：
- source 中的 map → 递归合并到 target 的对应 key
- source 中的其他值 → 直接覆盖 target 的对应 key

## cli/log.go — 日志系统

### ConsoleWriter

自定义 zerolog `ConsoleWriter`，提供彩色终端日志输出：

- 使用 sync.Pool 优化 buffer 分配
- 根据日志级别着色（debug=青色, info=绿色, warn=黄色, error=红色）
- 格式：`[LEVEL] message key=value ...`

## cli/markdown.go — Markdown 渲染

```go
func Markdown(input string) string
```

TTY 模式下使用 Chroma 高亮 Markdown，否则原样返回。

## cli/matcher.go — Waiter 条件匹配

### GetMatchValue

从请求/响应中提取值，支持以下选择器格式：

| 选择器 | 说明 | 示例 |
|--------|------|------|
| `request.param#name` | 请求参数值 | `request.param#userId` |
| `request.body#jmespath` | 请求体中的值 | `request.body#user.id` |
| `response.status` | HTTP 状态码 | `response.status` |
| `response.header#name` | 响应头值 | `response.header#X-Request-Id` |
| `response.body#jmespath` | 响应体中的值 | `response.body#data.status` |

### Match

比较期望值与实际值，支持三种测试模式：

| 模式 | 说明 |
|------|------|
| `equal` | 精确相等比较（带类型转换） |
| `any` | 列表中任一元素匹配 |
| `all` | 列表中所有元素都匹配 |

## cli/middleware.go — 请求钩子

### Before/After 处理器

```go
// 注册请求前处理器
cli.RegisterBefore("my-server list", func(path string, params *viper.Viper, req *gentleman.Request) {
    req.AddHeader("X-Custom", "value")
})

// 注册响应后处理器
cli.RegisterAfter("my-server list", func(path string, params *viper.Viper, resp *gentleman.Response, data interface{}) interface{} {
    // 修改响应数据
    return modifiedData
})
```

### 命令路径匹配

处理器按命令路径匹配，路径格式为 cobra 命令的完整路径：

```
"my-server list"         → my-server 命令的 list 子命令
"delivery task preview"  → delivery → task → preview
```

### 与自定义标志配合

```go
// 注册自定义标志和对应的处理器
cli.AddFlag("my-server list", "limit", "l", "Max items", 100)
cli.RegisterBefore("my-server list", func(path string, params *viper.Viper, req *gentleman.Request) {
    if limit := params.GetInt("limit"); limit > 0 {
        req.AddQuery("limit", fmt.Sprintf("%d", limit))
    }
})
```
