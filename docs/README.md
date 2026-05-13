# OpenAPI Toolkit — 项目概览

## 项目定位

OpenAPI Toolkit 是一个 **OpenAPI 规范驱动的 CLI 代码生成器与运行时框架**。它读取 OpenAPI 3.0 规范文件（YAML/JSON），自动生成完整的 Go CLI 应用程序代码，并提供运行时框架库供生成的代码调用。

核心能力：
- 从 OpenAPI Spec 自动生成 Cobra CLI 命令代码
- 多种认证方式（账户密码、API Key、OAuth2、Auth0）
- 请求/响应中间件钩子
- Shorthand 语法简化请求体输入
- MCP (Model Context Protocol) 服务器支持，将 CLI 操作暴露为 AI 代理工具
- JMESPath 查询过滤输出
- Waiter 轮询等待机制

## 快速导航

| 文档 | 说明 |
|------|------|
| [architecture.md](architecture.md) | 系统架构、模块关系、数据流 |
| [code-generator.md](code-generator.md) | 代码生成器详解（main.go、模板、ProcessAPI） |
| [runtime-framework.md](runtime-framework.md) | 运行时框架详解（cli 包、认证、中间件、格式化） |
| [authentication.md](authentication.md) | 认证体系详解（account/apikey/oauth/auth0） |
| [mcp-server.md](mcp-server.md) | MCP 服务器详解 |
| [shorthand-syntax.md](shorthand-syntax.md) | Shorthand 语法详解 |
| [templates.md](templates.md) | 模板系统详解 |
| [configuration.md](configuration.md) | 配置系统详解 |
| [data-structures.md](data-structures.md) | 核心数据结构与类型参考 |

## 项目结构

```
openapi-toolkit/
├── main.go              # 代码生成器入口（init / generate 子命令）
├── bindata.go           # go-bindata 生成的嵌入资源
├── go.mod               # Go 模块定义
│
├── account/             # 账户密码认证（AES 加密存储凭据）
│   └── account.go
│
├── apikey/              # API Key 认证（Header/Query/Cookie）
│   └── apikey.go
│
├── auth0/               # Auth0 OAuth2 封装
│   └── auth0.go
│
├── cli/                 # CLI 运行时框架核心
│   ├── cli.go           # 框架初始化、内置命令（search/tree/mcp）
│   ├── credentials.go   # 凭据管理、AuthHandler 接口、profile 系统
│   ├── flags.go         # 全局/自定义标志注册
│   ├── formatter.go     # 响应格式化（JSON/YAML + JMESPath + 语法高亮）
│   ├── http.go          # HTTP 客户端中间件（User-Agent/日志）
│   ├── input.go         # 请求体输入（stdin + shorthand 合并）
│   ├── log.go           # 自定义 zerolog ConsoleWriter
│   ├── markdown.go      # 终端 Markdown 渲染
│   ├── matcher.go       # Waiter 条件匹配
│   ├── mcp.go           # MCP 服务器实现
│   └── middleware.go     # 请求前/响应后钩子注册
│
├── oauth/               # OAuth 2.0 认证
│   ├── oauth.go         # Token 缓存/刷新/注入核心
│   ├── authcode.go      # Authorization Code + PKCE 流程
│   ├── clientcredentials.go  # Client Credentials 流程
│   ├── refresh.go       # Refresh Token 包装层
│   └── request.go       # 底层 Token HTTP 请求
│
├── shorthand/           # Shorthand 语法解析器
│   ├── shorthand.go     # AST 构建、ParseAndBuild、Get
│   ├── shorthand.peg    # PEG 语法定义
│   └── generated.go     # pigeon 生成的 PEG 解析器
│
├── templates/           # 代码生成模板
│   ├── commands.tmpl    # CLI 命令代码模板
│   └── main.tmpl        # main.go 入口模板
│
├── j/                   # 独立的 jq 风格工具
│   └── main.go
│
└── docs/                # 项目文档
```

## 两阶段模型

本项目的工作分为两个明确阶段：

### 阶段一：代码生成（开发时）

```
OpenAPI Spec (YAML/JSON)
    │
    ▼
openapi-toolkit generate
    │
    ├── 解析规范 → ProcessAPI() → OpenAPI 结构体
    ├── 渲染模板 → commands.tmpl → openapi.go（命令代码）
    └── 渲染模板 → main.tmpl   → main.go（入口代码）
```

### 阶段二：运行时（生成的 CLI 执行时）

```
用户执行命令
    │
    ▼
cobra 路由 → 操作函数
    │
    ├── cli.GetBody() ← stdin + shorthand
    ├── cli.HandleBefore() ← 自定义中间件
    ├── HTTP 请求（gentleman + 认证中间件 + 日志中间件）
    │       ├── AuthHandler.OnRequest() → 注入认证信息
    │       └── 401 → account.ForceRefresh() → 重试
    ├── cli.UnmarshalResponse() → 反序列化
    ├── cli.HandleAfter() ← 自定义中间件
    ├── Waiter 匹配（可选）
    └── cli.Formatter.Format() → JMESPath + JSON/YAML + 高亮
```

## 关键依赖

| 依赖 | 用途 |
|------|------|
| `kin-openapi` | OpenAPI 3.0 规范解析 |
| `cobra` | CLI 命令框架 |
| `viper` | 配置管理（文件 + 环境变量 + 标志） |
| `gentleman.v2` | HTTP 客户端（中间件链式调用） |
| `oauth2` | OAuth 2.0 认证 |
| `zerolog` | 结构化日志 |
| `chroma` | 语法高亮（JSON/YAML/HTTP/Markdown） |
| `go-jmespath-plus` | JMESPath 查询 |
| `mcp-go` | MCP 协议服务器 |
| `go-colorable/go-isatty` | 跨平台彩色输出 |
| `tablewriter` | 表格输出 |
| `yaml.v2` | YAML 处理（兼容 JSON） |
| `pigeon` | PEG 解析器生成（shorthand） |
