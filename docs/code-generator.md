# 代码生成器详解

代码生成器是 `openapi-toolkit` 的核心功能，负责读取 OpenAPI 规范文件并生成完整的 Go CLI 应用程序代码。

## 入口与子命令

代码生成器入口在 `main.go` 的 `main()` 函数中，注册了两个子命令：

### `init <app-name>`

初始化项目，生成 `main.go` 入口文件。

```bash
openapi-toolkit init my-cli
```

生成流程：
1. 检查当前目录是否已有 `main.go`，有则拒绝覆盖
2. 读取 `templates/main.tmpl` 模板
3. 传入 `{Name: "my-cli", NameEnv: "MY_CLI"}` 渲染模板
4. 调用 `writeFormattedFile()` 写入并格式化

### `generate <api-spec>`

从 OpenAPI 规范生成 CLI 命令代码。

```bash
openapi-toolkit generate openapi.yaml [flags]
```

支持的标志：

| 标志 | 说明 |
|------|------|
| `--x-cli-dravh` | 启用 x-cli 驱动的命令生成模式（按 domain/resource/action 组织） |
| `--allow-list <file>` | API 路径白名单文件 |
| `--disallow-list <file>` | API 路径黑名单文件 |
| `--account-aes-key <key>` | 覆盖账户凭据加密的 AES 密钥 |
| `--account-aes-iv <iv>` | 覆盖账户凭据加密的 AES IV |

## 生成流程详解

### 步骤 1：读取与解析规范

```go
data, err := ioutil.ReadFile(args[0])        // 读取文件
swagger, err = loader.LoadSwaggerFromData(data)  // 解析为 Swagger 对象
```

- 支持 YAML 和 JSON 格式（`yaml.Unmarshal` 是 JSON 的超集）
- 自动解引用 `$ref` 引用
- 解析失败时提供 YAML 行号定位（`findYAMLProblemLine`）

### 步骤 2：路径过滤

如果指定了 `--allow-list` 或 `--disallow-list`，会加载路径过滤器：

```go
pathFilter := loadPathFilter(allowListFile, true)   // 白名单
pathFilter := loadPathFilter(disallowListFile, false) // 黑名单
```

过滤文件语法：
```
/api/v1/users          # 匹配路径，所有方法
/api/v1/users:GET      # 仅匹配 GET 方法
/api/v1/users:GET|POST # 匹配 GET 或 POST
/api/*/settings        # * 匹配单个路径段
/api/**/config         # ** 匹配多个路径段
/api/v1/[id]           # [id] 匹配路径参数 {id}
```

### 步骤 3：ProcessAPI — 核心转换

`ProcessAPI()` 是代码生成的核心函数，将 `*openapi3.Swagger` 转换为 `*OpenAPI` 模板数据结构。

#### 3.1 基本信息

```go
result := &OpenAPI{
    Name:         apiName,       // 来自 x-cli-name 或 shortName
    GoName:       toGoName(shortName, false),  // 如 "openapi"
    PublicGoName: toGoName(shortName, true),   // 如 "Openapi"
    Title:        api.Info.Title,
    Description:  api.Info.Description,
}
```

#### 3.2 服务器列表

遍历 `api.Servers`，提取 URL 和描述。

#### 3.3 操作处理

对每个路径的每个 HTTP 方法：

1. **跳过条件**：`x-cli-ignore` 扩展、路径过滤不通过、`x-cli-dravh` 模式下无 `x-cli` 或 `x-cli.hidden=true`

2. **命名生成**：
   - `HandlerName` = `slug(name)` — 短横线命名，如 `get-user`
   - `GoName` = `toGoName(input, true)` — 驼峰命名，如 `GetUser`
   - `Use` = `usage(name, requiredParams)` — Cobra Use 字符串
   - `MCPToolName` = `tag_slug(name)` — MCP 工具名，如 `user_get-user`

3. **x-cli 扩展处理**（当 `--x-cli-dravh` 启用时）：
   - `domain` → 命令一级分类
   - `resource` → 命令二级分类（支持 `/` 分隔嵌套）
   - `action` → 命令名称
   - `hidden` → 隐藏命令

4. **参数提取**：
   - `getParams()` — 提取所有参数（path + operation 级别）
   - `getRequiredParams()` — 路径参数（必填）
   - `getOptionalParams()` — query/header 参数（选填）

5. **去重**：
   - `goNameCount` — Go 函数名去重（追加数字后缀）
   - `mcpToolNameCount` — MCP 工具名去重

6. **请求体信息**：
   - `getRequestInfo()` — 提取 media type、schema、examples
   - `CanHaveBody` — POST/PUT/PATCH 为 true

7. **返回类型推断**：
   - 默认 `interface{}`
   - 2xx 响应含 object 类型 → `map[string]interface{}`

#### 3.4 Tag 分组

**标准模式**（`--x-cli-dravh` 未启用）：
- 每个 tag 成为一个一级子命令
- tag 描述来自 OpenAPI 顶级 tags 定义

**x-cli-dravh 模式**：
- tag 路径按 `/` 分割，构建多级嵌套命令树
- 如 `domain/resource/sub` → 三级命令嵌套
- 自动补全中间层级

#### 3.5 Waiter 处理

从 OpenAPI 顶级 `x-cli-waiters` 扩展解析：
- 每个 Waiter 关联一个 OperationID
- 定义匹配器（Matcher）：Select（JMESPath 选择器）、Test（equal/any/all）、Expected、State
- 定义 After 映射：操作完成后要等待的参数传递

### 步骤 4：模板渲染

```go
templateData := ProcessAPI(shortName, swagger, rawData, enableXCliDravh, pathFilter)
templateData.AccountAESKey = accountAESKey
templateData.AccountAESIV = accountAESIV

tmpl.Execute(&sb, templateData)
writeFormattedFile(shortName+".go", []byte(sb.String()))
```

### 步骤 5：输出格式化

`writeFormattedFile()` 使用 `go/format.Source()` 对生成的代码进行格式化。如果格式化失败，会输出错误上下文帮助调试模板问题。

## 命名转换规则

### toGoName

将操作名转换为 Go 驼峰命名：

```
get-user       → GetUser (public) / getUser (private)
list_all_items → ListAllItems
createOrder    → CreateOrder
```

规则：将 `-` 和 `_` 替换为空格 → `strings.Title` → 去除空格

### slug

将操作名转换为 CLI 短横线命名：

```
getUser       → get-user
list_all_items → list-all-items
createOrder   → create-order
```

规则：去除数字后缀 → 将 `_` 和空格替换为 `-`

## OpenAPI 扩展（Extensions）

| 扩展名 | 位置 | 说明 |
|--------|------|------|
| `x-cli-aliases` | Operation | 命令别名列表 |
| `x-cli-description` | Operation/Info | 覆盖描述文本 |
| `x-cli-ignore` | PathItem/Operation | 忽略该路径/操作 |
| `x-cli-hidden` | PathItem/Operation | 隐藏命令（不显示在帮助中） |
| `x-cli-name` | Operation/Info | 覆盖命令/API 名称 |
| `x-cli-waiters` | 根级 | Waiter 定义 |
| `x-cli` | Operation | 高级命令生成扩展 |

### x-cli 扩展结构

```yaml
x-cli:
  domain: delivery        # 一级命令分组
  resource: task/result   # 二级命令分组（支持 / 嵌套）
  action: preview         # 命令名称
  verb: get               # HTTP 方法提示
  hidden: false           # 是否隐藏
```

## 路径过滤详解

### PathFilter 结构

```go
type PathFilter struct {
    Patterns []PathPattern
    IsAllow  bool  // true=白名单, false=黑名单
}

type PathPattern struct {
    PathRegex *regexp.Regexp
    Methods   map[string]bool  // nil=所有方法
    RawLine   string
}
```

### 匹配逻辑

```go
func (f *PathFilter) IsAllowed(path, method string) bool {
    matched := false
    for _, pattern := range f.Patterns {
        if pattern.PathRegex.MatchString(path) {
            if pattern.Methods == nil || pattern.Methods[method] {
                matched = true
                break
            }
        }
    }
    if f.IsAllow {
        return matched    // 白名单：匹配则允许
    }
    return !matched       // 黑名单：匹配则拒绝
}
```

### 通配符转正则

| 模式语法 | 正则 | 说明 |
|----------|------|------|
| `*` | `[^/]+` | 匹配单个路径段 |
| `**` | `.+` | 匹配多个路径段 |
| `[name]` | `\{[^/]+\}` | 匹配路径参数 |
