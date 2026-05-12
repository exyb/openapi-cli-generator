# CLI 参数输入处理机制

本文档梳理 openapi-cli-generator 生成的 CLI 工具如何处理参数输入，包括命令行参数、标准输入、JSON Body 构建等。

## 整体架构

```
用户输入
  ├── 命令行参数 (args)          ──→ cobra.Args 解析
  ├── 命令行选项 (flags)         ──→ cobra.Flags + viper 绑定
  └── 请求体 (body)             ──→ stdin + CLI Shorthand 合并
                                       ↓
                                   HTTP Request
```

## 1. 参数分类

OpenAPI 中的参数按 `in` 字段分为三类，生成代码的处理方式各不相同：

| 参数位置 (`in`) | 处理方式 | 代码中的体现 |
|---|---|---|
| `path` | **必选参数**，作为函数参数传入 | `func Xxx(paramId string, ...)` 中的函数参数 |
| `query` | **可选/必选 flag**，通过 `--flag` 传入 | `cmd.Flags().String("flag", ...)` |
| `header` | **可选/必选 flag**，通过 `--flag` 传入 | `cmd.Flags().String("flag", ...)` |

### 1.1 Path 参数（必选参数）

Path 参数在生成的函数签名中作为位置参数，在命令行中作为必选的 positional arguments：

```go
// 生成的函数签名
func OpenapiGetById(paramId string, params *viper.Viper, body string) (*gentleman.Response, interface{}, error) {
    url = strings.Replace(url, "{id}", paramId, 1)
    // ...
}
```

命令行用法：
```bash
flux-cli auth token get <id-value>
```

### 1.2 Query/Header 参数（可选参数）

Query 和 Header 参数注册为 cobra flag，通过 viper 绑定实现配置多源合并：

```go
cmd.Flags().String("status", "", "Filter by status")
cmd.MarkFlagRequired("status")  // 如果是必选
params.BindPFlags(cmd.Flags())  // 绑定到 viper
```

在请求构建时根据参数位置添加到对应位置：

```go
status := params.GetString("status")
if status != "" {
    req = req.AddQuery("status", fmt.Sprintf("%v", status))  // query 参数
    // 或
    req = req.AddHeader("X-Token", fmt.Sprintf("%v", token))  // header 参数
}
```

命令行用法：
```bash
flux-cli auth token list --status active --page 1
```

## 2. 请求体 (Request Body)

对于 `POST`、`PUT`、`PATCH` 等需要请求体的操作，生成代码调用 `cli.GetBody()` 获取 body 内容。

### 2.1 GetBody 函数处理流程

```
stdin 数据 ──→ 读取为字符串 (body)
                    ↓
命令行 Shorthand 参数 ──→ shorthand.ParseAndBuild() ──→ map[string]interface{}
                    ↓
              合并 stdin body + shorthand 数据
                    ↓
              按 mediaType 序列化 (JSON/YAML)
                    ↓
              返回最终 body 字符串
```

源码位于 `cli/input.go`：

1. **读取 stdin**：检测 stdin 是否有数据（非字符设备），有则读取全部内容作为基础 body
2. **解析 Shorthand**：将命令行剩余参数（必选参数之后的部分）用 Shorthand 语法解析为结构化数据
3. **合并**：如果同时有 stdin 和 shorthand 数据，使用 `DeepAssign` 递归合并（shorthand 覆盖 stdin 中的同名字段）
4. **序列化**：根据 `Content-Type` 选择 JSON 或 YAML 序列化

### 2.2 标准输入 (stdin)

直接通过管道或重定向传入完整的 JSON/YAML 数据：

```bash
# 重定向
flux-cli auth token create < request.json

# 管道
echo '{"name": "my-token"}' | flux-cli auth token create

# 从其他命令输出
cat template.json | flux-cli auth token create
```

### 2.3 CLI Shorthand 语法

Shorthand 是一种简洁的结构化数据语法，用于在命令行中快速构建 JSON/YAML 请求体，无需编写完整的 JSON。

#### 基本键值对

```bash
flux-cli auth token create name: my-token, expires: 3600
```

生成：
```json
{
  "name": "my-token",
  "expires": 3600
}
```

#### 自动类型推断

Shorthand 自动将值转换为对应类型：

| 输入 | 推断类型 | 结果 |
|---|---|---|
| `null` | null | `null` |
| `true` / `false` | bool | `true` / `false` |
| `123` / `1.5` | number | `123` / `1.5` |
| `hello` | string | `"hello"` |

使用 `~` 修饰符强制为字符串：

```bash
# 不加 ~ 会被推断为 bool
flux-cli cmd status: true        → "status": true

# 加 ~ 强制为字符串
flux-cli cmd status:~ true       → "status": "true"

# 空字符串
flux-cli cmd blank:~             → "blank": ""
```

#### 嵌套对象

使用 `.` 分隔符创建嵌套对象：

```bash
flux-cli cmd foo.bar.baz: 1
```

生成：
```json
{"foo": {"bar": {"baz": 1}}}
```

使用 `{}` 分组属性：

```bash
flux-cli cmd foo.bar{id: 1, count: 5}
```

生成：
```json
{"foo": {"bar": {"id": 1, "count": 5}}}
```

#### 数组

简单数组用 `,` 分隔：

```bash
flux-cli cmd tags: 1, 2, 3
```

生成：
```json
{"tags": [1, 2, 3]}
```

使用 `[]` 追加元素：

```bash
flux-cli cmd items[]: a, items[]: b, items[]: c
```

生成：
```json
{"items": ["a", "b", "c"]}
```

指定索引插入：

```bash
flux-cli cmd items[2]: value
```

#### 反向引用 (Backreference)

使用 `.` 开头引用当前对象上下文，使用 `[]` 开头引用当前数组上下文：

```bash
# 对象反向引用
flux-cli cmd foo.bar: 1, .baz: 2
# 等价于 foo{bar: 1, baz: 2}

# 数组反向引用
flux-cli cmd foo.bar[]: 1, []: 2, []: 3
# 等价于 foo.bar: 1, 2, 3
```

#### 从文件加载

| 语法 | 说明 |
|---|---|
| `key: @filename` | 加载文件内容，`.json` 文件自动解析为结构化数据 |
| `key: @~filename` | 强制作为字符串加载 |
| `key: @%filename` | 作为 base64 编码加载 |
| `key:~ @value` | 禁用文件加载，`@value` 作为普通字符串 |

```bash
# 加载 JSON 文件为结构化数据
flux-cli cmd config: @config.json

# 强制作为字符串
flux-cli cmd raw: @~data.txt

# base64 编码
flux-cli cmd file: @%image.png
```

### 2.4 stdin + Shorthand 合并

当同时使用 stdin 和 shorthand 时，shorthand 数据会**深度合并**到 stdin 数据上（shorthand 优先）：

```bash
# template.json: {"name": "test", "timeout": 30}
cat template.json | flux-cli cmd timeout: 60, tag: demo
```

最终请求体：
```json
{
  "name": "test",
  "timeout": 60,
  "tag": "demo"
}
```

`DeepAssign` 函数递归合并嵌套对象，shorthand 中的字段会覆盖 stdin 中同名字段。

## 3. 配置多源合并

所有 flag 参数通过 viper 绑定，支持三种配置来源（优先级从高到低）：

1. **命令行选项**：`--status active`
2. **环境变量**：`PREFIX_STATUS=active`（前缀 + 下划线分隔）
3. **配置文件**：`~/.appname/config.json` 中的 `"status": "active"`

配置文件支持 JSON、YAML、TOML 格式，搜索路径：
- `$HOME/.appname/config.*`
- `/etc/appname/config.*`

## 4. 全局选项

| 选项 | 短选项 | 类型 | 说明 |
|---|---|---|---|
| `--verbose` | | bool | 启用详细日志输出 |
| `--output-format` | `-o` | string | 输出格式 [json, yaml]，默认 json |
| `--query` | `-q` | string | 使用 JMESPath 过滤/投影结果 |
| `--raw` | | bool | 将查询结果输出为原始值而非转义 JSON |
| `--server` | | string | 覆盖服务器 URL |
| `--profile` | | string | 认证配置文件名，默认 default |

特殊配置项（仅环境变量/配置文件）：

| 名称 | 类型 | 说明 |
|---|---|---|
| `color` | bool | 强制启用彩色输出 |
| `nocolor` | bool | 禁用彩色输出 |
| `server-index` | int | 服务器索引，默认 0 |

## 5. 401 自动重试

生成的代码内置 401 自动重试机制：

1. 发送请求
2. 如果响应状态码为 401，调用 `account.ForceRefresh()` 刷新认证 token
3. 使用新 token 重新构建并发送请求
4. 如果仍然 401，返回错误

```go
if resp.StatusCode == 401 {
    account.ForceRefresh()
    req2 := cli.Client.Method().URL(url)
    // 重新添加参数和 body...
    resp2, err2 := req2.Do()
    // ...
}
```

## 6. 完整请求构建流程

```
1. 解析命令行参数和选项
       ↓
2. 确定服务器 URL (viper "server" 或 server-index)
       ↓
3. 构建请求路径，替换 path 参数
       ↓
4. 添加 query/header 参数
       ↓
5. 获取 body (stdin + shorthand 合并)
       ↓
6. 调用 HandleBefore 钩子
       ↓
7. 发送请求
       ↓
8. 如果 401 → 刷新 token → 重试
       ↓
9. 解析响应
       ↓
10. 调用 HandleAfter 钩子
       ↓
11. 格式化输出 (JSON/YAML + JMESPath)
```
