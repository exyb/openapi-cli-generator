# MCP 服务器详解

MCP (Model Context Protocol) 服务器将 CLI 操作暴露为 AI 代理可调用的工具，使 LLM 能够通过标准化协议发现和调用 API。

## 概述

MCP 是 Anthropic 提出的 JSON-RPC 2.0 协议，用于 LLM 与外部工具的通信。OpenAPI Toolkit 在生成的 CLI 代码中自动注册 MCP 工具，通过 `mcp serve` 子命令启动服务器。

## 架构

```
┌──────────────────┐     JSON-RPC 2.0     ┌──────────────────┐
│   AI 代理 / LLM  │ ◄──────────────────► │   MCP 服务器      │
│                  │                       │                  │
│  - Claude        │   stdio              │  mcp-go SDK      │
│  - GPT + MCP     │   streamable-http    │  ↓               │
│  - 其他 MCP 客户端│   sse                │  MCPToolInfo     │
└──────────────────┘                       │  ↓               │
                                           │  操作函数         │
                                           │  (生成的代码)     │
                                           └──────────────────┘
```

## 代码位置

MCP 相关代码分布在两个位置：

1. **cli/mcp.go** — MCP 服务器运行时实现
2. **templates/commands.tmpl** — MCP 工具注册代码生成

## 数据结构

### MCPToolInfo

```go
type MCPToolInfo struct {
    Name        string                                  // 工具名称
    Description string                                  // 工具描述
    Params      []MCPParamInfo                          // 参数列表
    HasBody     bool                                    // 是否有请求体
    Handler     func(args map[string]interface{}) (interface{}, error)  // 处理函数
}
```

### MCPParamInfo

```go
type MCPParamInfo struct {
    Name        string  // 参数名
    Type        string  // 参数类型 (string/int64/float64/boolean)
    Description string  // 参数描述
    Required    bool    // 是否必填
}
```

## 工具注册

在 `openapiRegister()` 函数中，为每个 Operation 注册 MCP 工具：

```go
// 生成的代码
cli.RegisterMCPTool(cli.MCPToolInfo{
    Name: "user_get-user",
    Description: "Get user by ID",
    Params: []cli.MCPParamInfo{
        {Name: "user-id", Type: "string", Description: "User ID", Required: true},
        {Name: "fields", Type: "string", Description: "Fields to return", Required: false},
    },
    HasBody: false,
    Handler: func(args map[string]interface{}) (interface{}, error) {
        userId := ""
        if v, ok := args["user-id"].(string); ok {
            userId = v
        }
        params := viper.New()
        if v, ok := args["fields"]; ok {
            switch v := v.(type) {
            case string:
                if v != "" { params.Set("fields", v) }
            case float64:
                params.Set("fields", v)
            case bool:
                params.Set("fields", v)
            }
        }
        _, decoded, err := OpenapiGetUser(userId, params)
        if err != nil {
            return nil, err
        }
        return decoded, nil
    },
})
```

### MCP 工具命名规则

工具名格式：`<tag>_<operation-slug>`

- tag 中的 `/` 和空格替换为 `_`
- operation 使用 slug 命名（短横线转下划线）
- 重复名称追加数字后缀

示例：
- tag=`user`, operation=`get-user` → `user_get-user`
- tag=`delivery/task`, operation=`preview-result` → `delivery_task_preview-result`
- tag=`delivery/task`, operation=`preview-result`（重复）→ `delivery_task_preview-result2`

### MCPBodyArg

对于有请求体的操作，`MCPBodyArg()` 从参数中提取 `body` 字段并序列化为 JSON：

```go
func MCPBodyArg(args map[string]interface{}) string {
    body, ok := args["body"]
    if !ok {
        return ""
    }
    b, _ := json.Marshal(body)
    return string(b)
}
```

## 服务器启动

### 命令

```bash
# stdio 模式（默认）
my-cli mcp serve

# Streamable HTTP 模式（推荐）
my-cli mcp serve --transport streamable-http --port 8080

# SSE 模式（遗留）
my-cli mcp serve --transport sse --port 8080

# 启用详细日志
my-cli mcp serve -v
```

### 启动流程

```
startMCPServer(cmd, args)
    │
    ├── 读取配置
    │       ├── transport (stdio/streamable-http/sse)
    │       ├── port (默认 8080)
    │       └── verbose (全局标志)
    │
    ├── 创建 MCP 服务器
    │       ├── server.WithToolCapabilities(true)
    │       └── [verbose] server.WithToolHandlerMiddleware(日志中间件)
    │
    ├── 注册所有工具
    │       └── 遍历 mcpTools，为每个创建 mcp.Tool 并添加 handler
    │
    ├── 打印注册信息
    │       └── [mcp] Registered N tools
    │
    └── 启动传输层
            ├── stdio → server.ServeStdio(s)
            ├── streamable-http → server.NewStreamableHTTPServer(s).Start(addr)
            └── sse → server.NewSSEServer(s).Start(addr)
```

### 工具创建

```go
// 参数类型映射
switch p.Type {
case "string":    toolOpts = append(toolOpts, mcp.WithString(p.Name, pOpts...))
case "int64":     toolOpts = append(toolOpts, mcp.WithNumber(p.Name, pOpts...))
case "float64":   toolOpts = append(toolOpts, mcp.WithNumber(p.Name, pOpts...))
case "boolean":   toolOpts = append(toolOpts, mcp.WithBoolean(p.Name, pOpts...))
default:          toolOpts = append(toolOpts, mcp.WithString(p.Name, pOpts...))
}

// 有请求体的操作额外添加 body 参数
if info.HasBody {
    toolOpts = append(toolOpts, mcp.WithObject("body",
        mcp.Description("Request body as a JSON object"),
    ))
}
```

### Verbose 日志中间件

当 `--verbose` 启用时，每个工具调用都会记录日志：

```
[mcp] -> tool_name {"param1":"value1"}
[mcp] <- tool_name OK (1.23ms)
[mcp] <- tool_name ERROR: message (456ms)
[mcp] <- tool_name FAIL (789ms)
```

## 三种传输模式

### stdio

- 默认模式，通过标准输入/输出通信
- 适用于 CLI 集成和本地开发
- 无需端口

### Streamable HTTP（推荐）

- MCP 2025-03-26 规范推荐模式
- HTTP 端点：`http://localhost:8080/mcp`
- 需要 `Mcp-Session-Id` 头（从 initialize 响应获取）
- 支持长连接流式响应

### SSE（遗留）

- Server-Sent Events 模式
- HTTP 端点：`http://localhost:8080/sse`
- 需要两步通信：GET /sse 获取消息端点，POST 到消息端点

## JSON-RPC 2.0 协议

### 交互流程

```
1. initialize
   → {"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}
   ← {"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{"tools":{}},"serverInfo":{"name":"my-cli","version":"1.0.0"}}}

2. tools/list
   → {"jsonrpc":"2.0","id":2,"method":"tools/list"}
   ← {"jsonrpc":"2.0","id":2,"result":{"tools":[...]}}

3. tools/call
   → {"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"user_get-user","arguments":{"user-id":"123"}}}
   ← {"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"{\"id\":\"123\",\"name\":\"John\"}"}]}}
```

## 与 x-cli-dravh 和路径过滤的一致性

MCP 工具的范围与 CLI 命令的范围完全一致：

- `--x-cli-dravh` 过滤后只保留有 `x-cli` 扩展且非 hidden 的操作
- `--allow-list` / `--disallow-list` 过滤后的操作才会注册为 MCP 工具
- 这确保了 MCP 暴露的接口与 CLI 可用的接口完全相同

## 测试验证

### stdio 模式

```bash
echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}' | my-cli mcp serve
```

### Streamable HTTP 模式

```bash
# 初始化，获取 session ID
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{...}}'

# 列出工具
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -H "Mcp-Session-Id: <session-id>" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'

# 调用工具
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -H "Mcp-Session-Id: <session-id>" \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"tool-name","arguments":{...}}}'
```

### SSE 模式

```bash
# 步骤1: 连接 SSE 端点获取消息 URL
curl http://localhost:8080/sse
# 输出: event: endpoint
#        data: /message?session_id=xxx

# 步骤2: POST 到消息端点
curl -X POST "http://localhost:8080/message?session_id=xxx" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{...}}'
```
