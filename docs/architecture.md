# 系统架构

## 整体架构图

```
┌─────────────────────────────────────────────────────────────────────┐
│                     代码生成阶段（开发时）                             │
│                                                                     │
│  OpenAPI Spec ──→ main.go (ProcessAPI) ──→ OpenAPI 结构体           │
│       (YAML/JSON)                          │                        │
│                                            ▼                        │
│                                  templates/commands.tmpl            │
│                                  templates/main.tmpl                │
│                                            │                        │
│                                            ▼                        │
│                                  生成的 openapi.go + main.go        │
└─────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────┐
│                     运行时阶段（生成的 CLI 执行时）                    │
│                                                                     │
│  main.go ──→ cli.Init() ──→ 配置/缓存/HTTP客户端/日志/格式化器      │
│       │                                                             │
│       ├──→ account.Init()  ──→ cli.UseAuth(account.Handler)         │
│       ├──→ apikey.Init()   ──→ cli.UseAuth(apikey.Handler)          │
│       ├──→ auth0.Init*()   ──→ cli.UseAuth(oauth.Handler)           │
│       │                                                             │
│       └──→ openapiRegister() ──→ 注册 cobra 命令 + MCP 工具         │
│                    │                                                │
│                    ▼                                                │
│  用户执行命令 → cobra 路由 → 操作函数                                │
│       │                                                             │
│       ├── cli.GetBody() ← stdin + shorthand                         │
│       ├── cli.HandleBefore() ← 自定义中间件                          │
│       ├── HTTP 请求（gentleman + 认证中间件 + 日志中间件）            │
│       │       ├── AuthHandler.OnRequest() → 注入认证信息             │
│       │       └── 401 → account.ForceRefresh() → 重试               │
│       ├── cli.UnmarshalResponse() → 反序列化                        │
│       ├── cli.HandleAfter() ← 自定义中间件                          │
│       ├── Waiter 匹配（cli.GetMatchValue + cli.Match）              │
│       └── cli.Formatter.Format() → JMESPath + JSON/YAML + 高亮      │
│                                                                     │
│  MCP 模式：cli mcp serve → 暴露操作为 MCP 工具                      │
└─────────────────────────────────────────────────────────────────────┘
```

## 模块依赖关系

```
main.go (代码生成器)
├── shorthand (示例数据转换)
├── openapi3 (规范解析)
├── bindata.go (嵌入模板)
└── text/template (模板渲染)

生成的代码 (openapi.go + main.go)
├── cli (运行时框架核心)
│   ├── credentials (凭据管理)
│   ├── flags (标志注册)
│   ├── formatter (输出格式化)
│   ├── http (HTTP 中间件)
│   ├── input (请求体输入)
│   ├── log (日志)
│   ├── markdown (Markdown 渲染)
│   ├── matcher (Waiter 匹配)
│   ├── mcp (MCP 服务器)
│   └── middleware (请求钩子)
├── account (账户认证)
├── apikey (API Key 认证)
├── oauth (OAuth2 认证)
│   ├── authcode (授权码流程)
│   ├── clientcredentials (客户端凭据流程)
│   ├── refresh (Token 刷新)
│   └── request (Token HTTP 请求)
├── auth0 (Auth0 封装)
└── shorthand (输入解析)
```

## 核心数据流

### 1. 代码生成数据流

```
OpenAPI Spec 文件
    │
    ▼ ioutil.ReadFile()
原始字节数据 (rawData)
    │
    ▼ openapi3.LoadSwaggerFromData()
*openapi3.Swagger (已解引用的规范对象)
    │
    ▼ ProcessAPI()
*OpenAPI (模板数据结构)
    │  ├── Name, GoName, PublicGoName
    │  ├── Title, Description
    │  ├── Servers []*Server
    │  ├── Operations []*Operation
    │  │       ├── HandlerName, GoName, Use, Aliases
    │  │       ├── Method, Path, CanHaveBody
    │  │       ├── AllParams, RequiredParams, OptionalParams
    │  │       ├── Waiters, MCPToolName
    │  │       └── ...
    │  ├── Waiters []*Waiter
    │  ├── TagGroups []*TagGroup
    │  ├── AccountAESKey, AccountAESIV
    │  └── Imports (Fmt/Strings/Time)
    │
    ▼ template.Execute()
生成的 Go 源代码字符串
    │
    ▼ writeFormattedFile() → go/format.Source()
格式化的 .go 文件
```

### 2. HTTP 请求处理数据流

```
cobra 命令 Run 函数
    │
    ├── 解析位置参数 (args[]) → RequiredParams
    ├── 解析标志参数 → viper.Viper (params)
    ├── 构建请求体 → cli.GetBody(mediaType, args)
    │       ├── 读取 stdin
    │       ├── 解析 shorthand 参数 → shorthand.ParseAndBuild()
    │       └── 合并 stdin + shorthand → DeepAssign()
    │
    ▼ 调用操作函数 XxxYyy(requiredArgs, params, body)
    │
    ├── 构建 URL（server + path + 路径参数替换）
    ├── 创建 gentleman 请求 → cli.Client.Method().URL(url)
    ├── 添加 query/header 参数
    ├── 设置请求体（Content-Type + Body）
    ├── cli.HandleBefore() → 执行注册的前置钩子
    │
    ▼ req.Do()
    │
    ├── [认证中间件] AuthHandler.OnRequest() → 注入 Authorization 头
    ├── [日志中间件] 记录请求/响应详情（verbose 模式）
    ├── [UA 中间件] 设置 User-Agent
    │
    ▼ HTTP 响应
    │
    ├── 401 处理 → account.ForceRefresh() → 重建请求 → 重试
    ├── cli.UnmarshalResponse() → 反序列化响应体
    ├── cli.HandleAfter() → 执行注册的后置钩子
    │
    ▼ 返回 (resp, decoded, err)
    │
    ├── Waiter 处理（如果启用）
    │       ├── cli.GetMatchValue() → 从请求/响应提取值
    │       └── cli.Match() → 比较期望值
    │
    └── cli.Formatter.Format(decoded)
            ├── JMESPath 查询过滤
            ├── JSON/YAML 编码
            └── Chroma 语法高亮输出
```

### 3. 认证数据流

```
HTTP 请求发出前
    │
    ▼ gentleman 认证中间件
    │
    ├── GetProfile() → 从 credentials.json 读取当前 profile
    │
    ├── 根据 profile["type"] 查找 AuthHandler
    │
    ▼ AuthHandler.OnRequest(log, request)
    │
    ├── [account.Handler]
    │       ├── 检查缓存 Token → cli.Cache
    │       ├── Token 过期 → decryptCredentials() → AES-CBC 解密
    │       ├── loginAndFetchToken() → HTTP POST 登录
    │       ├── 缓存新 Token → cli.Cache.WriteConfig()
    │       └── request.Header.Set("Authorization", "Bearer "+token)
    │
    ├── [apikey.Handler]
    │       ├── 根据 Location 类型注入
    │       ├── Header → request.Header.Set(key, value)
    │       ├── Query → 添加 URL 查询参数
    │       └── Cookie → 添加 Cookie 头
    │
    └── [oauth.AuthCodeHandler / ClientCredentialsHandler]
            ├── 检查缓存 Token → cli.Cache
            ├── Token 过期 → RefreshTokenSource.Token()
            │       ├── 尝试 refresh_token → requestToken()
            │       └── 失败 → 原始 TokenSource.Token()
            ├── 缓存新 Token → cli.Cache.WriteConfig()
            └── token.SetAuthHeader(request)
```

### 4. MCP 请求处理数据流

```
AI 代理发送 MCP 请求 (JSON-RPC 2.0)
    │
    ▼ mcp-go SDK 路由
    │
    ├── tools/list → 返回已注册工具列表
    │
    ▼ tools/call { name, arguments }
    │
    ├── 查找 MCPToolInfo.Handler
    │
    ▼ Handler(args map[string]interface{})
    │
    ├── 提取 RequiredParams → 类型断言 string
    ├── 创建 viper.Viper → 设置 OptionalParams
    ├── 提取 body → MCPBodyArg(args) → json.Marshal
    │
    ▼ 调用操作函数 XxxYyy(requiredArgs, params, body)
    │
    └── 返回 (decoded, nil) → mcp.NewToolResultText(json)
```

## 配置层次

配置值按以下优先级从高到低加载：

```
1. 命令行标志 (--verbose, --server, --output-format, ...)
2. 环境变量 (PREFIX_VERBOSE, PREFIX_SERVER, ...)
3. 配置文件 (~/.appname/config.json 或 /etc/appname/config.json)
4. 默认值 (代码中 viper.SetDefault)
```

## 文件存储

| 文件 | 路径 | 用途 |
|------|------|------|
| 配置文件 | `~/.appname/config.json` | CLI 配置 |
| 缓存文件 | `~/.appname/cache.json` | Token 缓存等临时数据 |
| 凭据文件 | `~/.appname/credentials.json` | 认证凭据（profile 信息） |

## 内置命令体系

生成的 CLI 包含以下内置命令：

| 命令 | 类型 | 说明 |
|------|------|------|
| `help` | 内置 | Cobra 默认帮助 |
| `setup` | 内置 | 认证配置入口 |
| `setup add-profile` | 内置 | 添加认证 profile |
| `setup list-profiles` | 内置 | 列出所有 profile |
| `help-config` | 内置 | 显示配置帮助 |
| `help-input` | 内置 | 显示输入帮助（shorthand 语法） |
| `search <keyword>` | 内置 | 按关键字搜索命令 |
| `tree [-L N]` | 内置 | 树形显示命令结构 |
| `mcp serve` | 内置 | 启动 MCP 服务器 |
| `<tag> <operation>` | 生成 | API 操作命令 |
| `wait <waiter>` | 生成 | Waiter 等待命令 |
