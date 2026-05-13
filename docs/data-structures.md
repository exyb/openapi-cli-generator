# 核心数据结构与类型参考

本文档列出 OpenAPI Toolkit 中所有核心数据结构，供开发者和大模型快速查阅。

## 代码生成器类型（main.go）

### Param — OpenAPI 参数

```go
type Param struct {
    Name        string  // 原始参数名（如 "userId"）
    CLIName     string  // CLI 标志名（如 "user-id"）
    GoName      string  // Go 变量名（如 "paramUserId"）
    Description string  // 参数描述
    In          string  // 参数位置："path" / "query" / "header"
    Required    bool    // 是否必填
    Type        string  // Go 类型："string" / "bool" / "int64" / "float64"
    TypeNil     string  // 零值："" / "false" / "0" / "0.0"
    Style       string  // 参数样式
    Explode     bool    // 是否展开
    Redeclare   bool    // 是否与必填参数重名（需重新声明）
}
```

### Operation — OpenAPI 操作

```go
type Operation struct {
    HandlerName    string      // CLI 命令名（slug 格式，如 "get-user"）
    GoName         string      // Go 函数名（如 "GetUser"）
    Use            string      // Cobra Use 字符串（如 "get-user <user-id>"）
    Aliases        []string    // 命令别名
    Short          string      // 短描述
    Long           string      // 长描述（含请求 schema 和示例）
    Method         string      // HTTP 方法（如 "Get" / "Post"）
    CanHaveBody    bool        // 是否有请求体（Post/PUT/PATCH）
    ReturnType     string      // 返回类型（"interface{}" 或 "map[string]interface{}"）
    Path           string      // URL 路径模板（如 "/api/v1/users/{userId}"）
    AllParams      []*Param    // 所有参数
    RequiredParams []*Param    // 必填参数（路径参数）
    OptionalParams []*Param    // 可选参数（query/header）
    MediaType      string      // 请求体 Content-Type（如 "application/json"）
    Examples       []string    // 请求体示例（shorthand 格式）
    Hidden         bool        // 是否隐藏
    NeedsResponse  bool        // Waiter 是否需要原始响应
    Waiters        []*WaiterParams  // 关联的 Waiter
    Tag            string      // 所属 tag（如 "user" 或 "delivery/task"）
    MCPToolName    string      // MCP 工具名（如 "user_get-user"）
}
```

### TagGroup — Tag 分组

```go
type TagGroup struct {
    Name        string  // 分组名（如 "user"）
    Description string  // 分组描述
    Path        string  // 完整路径（如 "delivery/task"）
    ParentPath  string  // 父路径（如 "delivery"）
}
```

### XCli — x-cli 扩展

```go
type XCli struct {
    Domain   string          // 一级命令分组
    Resource string          // 二级命令分组（支持 / 嵌套）
    Action   json.RawMessage // 命令名称
    Verb     string          // HTTP 方法提示
    Hidden   bool            // 是否隐藏
}
```

### Waiter — 等待器

```go
type Waiter struct {
    CLIName     string        // CLI 命令名
    GoName      string        // Go 函数名
    Use         string        // Cobra Use 字符串
    Aliases     []string      // 别名
    Short       string        // 短描述
    Long        string        // 长描述
    Delay       int           // 轮询间隔（秒）
    Attempts    int           // 最大尝试次数
    OperationID string        // 关联的操作 ID
    Operation   *Operation    // 关联的操作对象
    Matchers    []*Matcher    // 匹配条件列表
    After       map[string]map[string]string  // 操作后等待映射
}
```

### Matcher — 匹配条件

```go
type Matcher struct {
    Select   string          // 值选择器（如 "response.body#status"）
    Test     string          // 测试类型："equal" / "any" / "all"
    Expected json.RawMessage // 期望值
    State    string          // 状态："failure" 或空
}
```

### WaiterParams — Waiter 参数映射

```go
type WaiterParams struct {
    Waiter *Waiter           // 关联的 Waiter
    Args   []string          // 必填参数的选择器列表
    Params map[string]string // 可选参数的选择器映射
}
```

### OpenAPI — 模板数据根结构

```go
type OpenAPI struct {
    Imports         Imports       // 条件导入标记
    Name            string        // 应用名称
    GoName          string        // 函数名前缀（小写）
    PublicGoName    string        // 函数名前缀（大写）
    Title           string        // API 标题
    Description     string        // API 描述
    Servers         []*Server     // 服务器列表
    Operations      []*Operation  // 操作列表
    Waiters         []*Waiter     // Waiter 列表
    TagGroups       []*TagGroup   // Tag 分组列表
    EnableXCliDravh bool          // 是否启用 x-cli-dravh 模式
    AccountAESKey   string        // AES 密钥覆盖
    AccountAESIV    string        // AES IV 覆盖
}
```

### Server — 服务器端点

```go
type Server struct {
    Description string  // 服务器描述
    URL         string  // 服务器 URL
}
```

### Imports — 条件导入

```go
type Imports struct {
    Fmt     bool  // 需要 "fmt"（query/header 可选参数）
    Strings bool  // 需要 "strings"（路径参数）
    Time    bool  // 需要 "time"（Waiter）
}
```

### PathPattern / PathFilter — 路径过滤

```go
type PathPattern struct {
    PathRegex *regexp.Regexp  // 编译后的路径正则
    Methods   map[string]bool // 允许的 HTTP 方法（nil=所有）
    RawLine   string          // 原始行文本
}

type PathFilter struct {
    Patterns []PathPattern  // 模式列表
    IsAllow  bool           // true=白名单, false=黑名单
}
```

## 运行时框架类型

### cli.Config — 初始化配置

```go
type Config struct {
    AppName   string  // 应用名称（如 "my-cli"）
    EnvPrefix string  // 环境变量前缀（如 "MY_CLI"）
    Version   string  // 版本号
}
```

### cli.AuthHandler — 认证处理器接口

```go
type AuthHandler interface {
    ProfileKeys() []string
    OnRequest(log *zerolog.Logger, request *http.Request) error
}
```

### cli.CredentialsFile — 凭据文件

```go
type CredentialsFile struct {
    *viper.Viper
    keys     []string  // profile 键名
    listKeys []string  // 列表显示键名
}
```

## 认证类型

### account.Handler — 账户认证

```go
type Handler struct {
    TypeName     string          // 认证类型名
    Keys         []string        // profile 键名列表
    GetServerURL func() string   // 获取服务器 URL 的函数
}
```

### apikey.Location — API Key 位置

```go
type Location int

const (
    LocationHeader Location = iota  // Header 注入
    LocationQuery                    // Query 参数注入
    LocationCookie                   // Cookie 注入
)
```

### apikey.Handler — API Key 认证

```go
type Handler struct {
    Name string          // API Key 名称
    In   Location        // 注入位置
    Keys []string        // profile 键名列表
}
```

### oauth.AuthCodeHandler — 授权码认证

```go
type AuthCodeHandler struct {
    ClientID       string
    AuthorizeURL   string
    TokenURL       string
    Keys           []string
    Params         []string
    Scopes         []string
    getParamsFunc  func(profile map[string]string) url.Values
}
```

### oauth.AuthorizationCodeTokenSource — PKCE Token 源

```go
type AuthorizationCodeTokenSource struct {
    ClientID       string
    AuthorizeURL   string
    TokenURL       string
    EndpointParams *url.Values
    Scopes         []string
}
```

### oauth.ClientCredentialsHandler — 客户端凭据认证

```go
type ClientCredentialsHandler struct {
    TokenURL      string
    Keys          []string
    Params        []string
    Scopes        []string
    getParamsFunc func(profile map[string]string) url.Values
}
```

### oauth.RefreshTokenSource — 刷新 Token 源

```go
type RefreshTokenSource struct {
    ClientID       string
    TokenURL       string
    EndpointParams *url.Values
    RefreshToken   string
    TokenSource    oauth2.TokenSource
}
```

## MCP 类型

### cli.MCPToolInfo — MCP 工具信息

```go
type MCPToolInfo struct {
    Name        string
    Description string
    Params      []MCPParamInfo
    HasBody     bool
    Handler     func(args map[string]interface{}) (interface{}, error)
}
```

### cli.MCPParamInfo — MCP 工具参数

```go
type MCPParamInfo struct {
    Name        string
    Type        string   // "string" / "int64" / "float64" / "boolean"
    Description string
    Required    bool
}
```

## 中间件类型

### cli.BeforeHandlerFunc — 请求前处理器

```go
type BeforeHandlerFunc func(path string, params *viper.Viper, req *gentleman.Request)
```

### cli.AfterHandlerFunc — 响应后处理器

```go
type AfterHandlerFunc func(path string, params *viper.Viper, resp *gentleman.Response, data interface{}) interface{}
```

## Shorthand 类型

### shorthand.AST — 语法树

```go
type AST []*KeyValue
```

### shorthand.KeyValue — 键值对

```go
type KeyValue struct {
    PostProcess bool          // 是否需要后处理
    Key         *Key          // 键
    Value       interface{}   // 值
}
```

### shorthand.Key — 键

```go
type Key struct {
    ResetContext bool        // 是否重置到根上下文
    Parts        []*KeyPart  // 键部分列表
}
```

### shorthand.KeyPart — 键部分

```go
type KeyPart struct {
    Key   string   // 键名
    Index []int    // 数组索引（-1=追加）
}
```

## 格式化类型

### cli.ResponseFormatter — 格式化器接口

```go
type ResponseFormatter interface {
    Format(interface{}) error
}
```

### cli.DefaultFormatter — 默认格式化器

```go
type DefaultFormatter struct {
    tty bool  // 是否为终端输出
}
```

## 命名转换函数

| 函数 | 输入 | 输出 | 用途 |
|------|------|------|------|
| `toGoName("get-user", true)` | `"get-user"` | `"GetUser"` | 导出函数名 |
| `toGoName("get-user", false)` | `"get-user"` | `"getUser"` | 未导出变量名 |
| `slug("getUser")` | `"getUser"` | `"get-user"` | CLI 命令名 |
| `usage("getUser", params)` | `"getUser"` | `"get-user <user-id>"` | Cobra Use 字符串 |
| `escapeString("hello\nworld")` | `"hello\nworld"` | `"hello\\nworld"` | 模板字符串转义 |
