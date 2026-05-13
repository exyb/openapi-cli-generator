# 模板系统详解

OpenAPI Toolkit 使用 Go `text/template` 模板生成 CLI 代码。模板文件通过 `go-bindata` 嵌入到生成器二进制文件中。

## 模板文件

| 文件 | 生成目标 | 说明 |
|------|----------|------|
| `templates/main.tmpl` | `main.go` | CLI 入口文件 |
| `templates/commands.tmpl` | `openapi.go` | CLI 命令代码 |

## bindata 嵌入机制

```go
//go:generate go-bindata ./templates/...
```

- `go-bindata` 将模板文件编译为 Go 代码（`bindata.go`）
- 运行时通过 `Asset("templates/commands.tmpl")` 读取模板内容
- 修改模板后必须重新运行 `go-bindata` 才能生效

## main.tmpl — 入口模板

生成 CLI 应用的 `main.go` 入口文件。

### 模板数据

```go
templateData := map[string]string{
    "Name":    "my-cli",      // 应用名称
    "NameEnv": "MY_CLI",      // 环境变量前缀
}
```

### 生成代码结构

```go
package main

import (
    "github.com/danielgtaylor/openapi-toolkit/account"
    "github.com/danielgtaylor/openapi-toolkit/cli"
    "github.com/spf13/viper"
)

func main() {
    cli.Init(&cli.Config{
        AppName:   "my-cli",
        EnvPrefix: "MY_CLI",
        Version:   "1.0.0",
    })

    account.Init("MY_CLI", account.ServerURL(func() string {
        server := viper.GetString("server")
        if server == "" {
            server = openapiServers()[viper.GetInt("server-index")]["url"]
        }
        return server
    }))

    // TODO: Add register commands here.

    cli.Root.Execute()
}
```

### 使用方式

1. 运行 `openapi-toolkit init my-cli` 生成 `main.go`
2. 手动将 `// TODO` 替换为 `openapiRegister(true)` 或 `openapiRegister(false)`
3. 运行 `openapi-toolkit generate openapi.yaml` 生成 `openapi.go`

## commands.tmpl — 命令模板

这是最复杂的模板，生成所有 CLI 命令代码。

### 模板数据

`OpenAPI` 结构体是模板的渲染数据，包含：

| 字段 | 类型 | 模板中的使用 |
|------|------|-------------|
| `Imports` | `Imports` | 条件导入 fmt/strings/time |
| `Name` | `string` | 子命令模式下的根命令名 |
| `GoName` | `string` | 函数名前缀（如 `openapi`） |
| `PublicGoName` | `string` | 导出函数名前缀（如 `Openapi`） |
| `Title` | `string` | 命令 Short 描述 |
| `Description` | `string` | 命令 Long 描述 |
| `Servers` | `[]*Server` | 服务器列表 |
| `Operations` | `[]*Operation` | 操作列表 |
| `Waiters` | `[]*Waiter` | Waiter 列表 |
| `TagGroups` | `[]*TagGroup` | Tag 分组 |
| `AccountAESKey` | `string` | AES 密钥覆盖 |
| `AccountAESIV` | `string` | AES IV 覆盖 |

### 自定义模板函数

```go
funcs := template.FuncMap{
    "escapeStr": escapeString,   // 转义字符串中的特殊字符
    "slug":      slug,           // 转换为短横线命名
    "title":     strings.Title,  // 首字母大写
}
```

### 生成代码结构

#### 1. 包声明与导入

```go
package main

import (
    "fmt"       // 条件导入：当有 query/header 可选参数时
    "strings"   // 条件导入：当有路径参数时
    "time"      // 条件导入：当有 Waiter 时

    "github.com/danielgtaylor/openapi-toolkit/account"
    "github.com/danielgtaylor/openapi-toolkit/cli"
    "github.com/pkg/errors"
    "github.com/rs/zerolog/log"
    "github.com/spf13/cobra"
    "github.com/spf13/viper"
    "gopkg.in/h2non/gentleman.v2"
)
```

#### 2. params 子模板

定义命令标志注册的可复用模板：

```go
{{ define "params" }}
    // 注册可选参数标志
    cmd.Flags().Bool("flag-name", false, "description")
    cmd.Flags().String("flag-name", "", "description")
    cmd.Flags().Int64("flag-name", 0, "description")

    // 注册必填标志
    cmd.MarkFlagRequired("flag-name")

    // 注册 Waiter 标志
    cmd.Flags().Bool("wait-xxx", false, "Waiter description")

    // 应用自定义标志
    cli.SetCustomFlags(cmd)

    // 绑定到 viper
    params.BindPFlags(cmd.Flags())
{{ end }}
```

#### 3. 服务器列表函数

```go
func openapiServers() []map[string]string {
    return []map[string]string{
        {"description": "Production", "url": "https://api.example.com"},
        {"description": "Staging", "url": "https://staging.api.example.com"},
    }
}
```

#### 4. 操作函数

为每个 Operation 生成一个函数：

```go
func OpenapiGetUser(userId string, params *viper.Viper) (*gentleman.Response, interface{}, error) {
    handlerPath := "user get-user"  // 用于 middleware 路由

    // 获取服务器 URL
    server := viper.GetString("server")
    if server == "" {
        server = openapiServers()[viper.GetInt("server-index")]["url"]
    }

    // 构建 URL + 路径参数替换
    url := server + "/api/v1/users/{userId}"
    url = strings.Replace(url, "{userId}", userId, 1)

    // 创建请求
    req := cli.Client.Get().URL(url)

    // 添加可选参数
    fields := params.GetString("fields")
    if fields != "" {
        req = req.AddQuery("fields", fields)
    }

    // 请求前钩子
    cli.HandleBefore(handlerPath, params, req)

    // 发送请求
    resp, err := req.Do()
    if err != nil {
        return nil, nil, errors.Wrap(err, "Request failed")
    }

    // 401 重试
    if resp.StatusCode == 401 {
        account.ForceRefresh()
        req2 := cli.Client.Get().URL(url)
        // ... 重建请求
        resp2, err2 := req2.Do()
        if resp2.StatusCode == 401 {
            return nil, nil, errors.Errorf("HTTP 401: authentication failed")
        }
        resp = resp2
    }

    // 反序列化响应
    var decoded interface{}
    if resp.StatusCode < 400 {
        cli.UnmarshalResponse(resp, &decoded)
    } else {
        return nil, nil, errors.Errorf("HTTP %d: %s", resp.StatusCode, resp.String())
    }

    // 响应后钩子
    after := cli.HandleAfter(handlerPath, params, resp, decoded)
    if after != nil {
        decoded = after
    }

    return resp, decoded, nil
}
```

#### 5. Waiter 函数

```go
func OpenapiUserActiveWait(userId string, params *viper.Viper) error {
    attempt := 0
    for attempt < 30 {
        attempt++
        resp, decoded, err := OpenapiGetUser(userId, params)
        if err != nil {
            return errors.Wrap(err, "Could not call waiter operation")
        }

        actual, err := cli.GetMatchValue(resp.Context, "response.body#status", params.AllSettings(), decoded)
        match, err := cli.Match("equal", []byte(`"active"`), actual)
        if match {
            break
        }

        time.Sleep(5 * time.Second)
    }
    if attempt >= 30 {
        return errors.New("Maximum attempts exceeded")
    }
    return nil
}
```

#### 6. Register 函数

`openapiRegister(subcommand bool)` 是核心注册函数：

```go
func openapiRegister(subcommand bool) {
    // AES 密钥设置（如果指定）
    account.SetAESKey("custom-key")
    account.SetAESIV("custom-iv")

    // 确定根命令
    root := cli.Root
    if subcommand {
        root = &cobra.Command{
            Use: "my-cli",
            Short: "My API CLI",
            Long: cli.Markdown("Description..."),
        }
    }

    // 创建 Tag 命令层级
    tagCommands := make(map[string]*cobra.Command)
    tagCommands["user"] = &cobra.Command{Use: "user", Short: "User operations"}
    root.AddCommand(tagCommands["user"])

    // 创建 Waiter 命令组
    wait := &cobra.Command{Use: "wait", Short: "Wait for events"}
    root.AddCommand(wait)

    // 为每个 Operation 创建 cobra 命令
    func() {
        params := viper.New()
        cmd := &cobra.Command{
            Use:   "get-user <user-id>",
            Short: "Get user by ID",
            Long:  cli.Markdown("Detailed description..."),
            Args:  cobra.MinimumNArgs(1),
            Run: func(cmd *cobra.Command, args []string) {
                _, decoded, err := OpenapiGetUser(args[0], params)
                if err != nil {
                    log.Fatal().Err(err).Msg("Error calling operation")
                }
                cli.Formatter.Format(decoded)
            },
        }
        tagCommands["user"].AddCommand(cmd)

        // 注册标志
        cmd.Flags().String("fields", "", "Fields to return")
        cli.SetCustomFlags(cmd)
        params.BindPFlags(cmd.Flags())
    }()

    // 为每个 Operation 注册 MCP 工具
    cli.RegisterMCPTool(cli.MCPToolInfo{
        Name: "user_get-user",
        Description: "Get user by ID",
        Params: []cli.MCPParamInfo{...},
        Handler: func(args map[string]interface{}) (interface{}, error) {
            // ... 提取参数，调用操作函数
        },
    })
}
```

### subcommand 模式

`openapiRegister(true)` — 子命令模式：
- 创建新的 cobra 命令作为根
- 适用于一个 CLI 对接多个 OpenAPI 规范的场景
- 命令路径：`my-cli user get-user`

`openapiRegister(false)` — 直接模式：
- 使用 `cli.Root` 作为根
- 适用于单一 OpenAPI 规范的 CLI
- 命令路径：`user get-user`

## 修改模板后的构建流程

```bash
# 1. 修改模板文件
vim templates/commands.tmpl

# 2. 重新生成 bindata
go-bindata ./templates/...

# 3. 重新安装
go install

# 4. 重新生成 CLI 代码
openapi-toolkit generate openapi.yaml
```
