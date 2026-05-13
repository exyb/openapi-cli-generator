# 配置系统详解

OpenAPI Toolkit 使用 Viper 作为配置管理核心，支持多层配置来源和优先级覆盖。

## 配置来源优先级

从高到低：

```
1. 命令行标志 (--verbose, --server, ...)
2. 环境变量 (PREFIX_VERBOSE, PREFIX_SERVER, ...)
3. 配置文件 (~/.appname/config.json 或 /etc/appname/config.json)
4. 代码默认值 (viper.SetDefault)
```

## 配置文件

### 主配置文件

| 属性 | 值 |
|------|-----|
| 文件名 | `config` (支持 .json/.yaml/.toml) |
| 搜索路径 | `/etc/appname/`, `~/.appname/` |
| 用途 | CLI 全局配置 |

示例 `~/.my-cli/config.json`：
```json
{
    "verbose": true,
    "output-format": "json",
    "server": "https://api.example.com",
    "server-index": 0,
    "profile": "production"
}
```

### 缓存文件

| 属性 | 值 |
|------|-----|
| 文件名 | `cache.json` |
| 路径 | `~/.appname/cache.json` |
| 用途 | Token 等临时数据缓存 |

示例 `~/.my-cli/cache.json`：
```json
{
    "profiles": {
        "default": {
            "token": "eyJhbGciOi...",
            "expires": "2024-01-15T10:30:00Z",
            "type": "account",
            "refresh": "rt_abc123..."
        },
        "production": {
            "token": "eyJhbGciOi...",
            "expires": "2024-01-15T11:00:00Z",
            "type": "account"
        }
    }
}
```

### 凭据文件

| 属性 | 值 |
|------|-----|
| 文件名 | `credentials.json` |
| 路径 | `~/.appname/credentials.json` |
| 用途 | 认证凭据（profile 信息） |

示例 `~/.my-cli/credentials.json`：
```json
{
    "profiles": {
        "default": {
            "type": "account",
            "credentials": "base64encrypted...",
            "url": "/api/v1/auth/login"
        },
        "api-access": {
            "type": "apikey",
            "api_key": "sk-abc123..."
        },
        "oauth-profile": {
            "type": "",
            "client_id": "my-client-id",
            "client_secret": "my-client-secret"
        }
    }
}
```

## 环境变量

环境变量格式：`PREFIX_VARIABLE_NAME`

- 前缀由 `EnvPrefix` 配置决定（通常为大写的应用名）
- 变量名中的 `-` 替换为 `_`
- 自动绑定到 viper 配置

示例：
```bash
export MY_CLI_VERBOSE=true
export MY_CLI_SERVER=https://api.example.com
export MY_CLI_OUTPUT_FORMAT=yaml
export MY_CLI_PROFILE=production
```

### 特殊环境变量

```bash
# 账户凭据注入（account 包）
MY_APP_ACCOUNT_CREDENTIALS=base64credentials:loginURL
```

## 全局标志

| 标志 | 短标志 | 类型 | 默认值 | 说明 |
|------|--------|------|--------|------|
| `--verbose` | | bool | false | 启用详细日志输出 |
| `--output-format` | `-o` | string | "json" | 输出格式 (json/yaml) |
| `--query` | `-q` | string | "" | JMESPath 查询过滤 |
| `--raw` | | bool | false | 原始输出（不转义） |
| `--server` | | string | "" | 覆盖服务器 URL |
| `--profile` | | string | "default" | 认证 profile |
| `--color` | | bool | false | 强制彩色输出 |
| `--nocolor` | | bool | false | 禁用彩色输出 |

## 隐藏配置

以下配置不作为命令行标志暴露，但可通过环境变量或配置文件设置：

| 名称 | 类型 | 说明 |
|------|------|------|
| `color` | bool | 强制彩色输出 |
| `nocolor` | bool | 禁用彩色输出 |
| `app-name` | string | 应用名称（内部使用） |
| `config-directory` | string | 配置目录路径（内部使用） |
| `server-index` | int | 服务器列表索引（默认 0） |

## 生成器标志

`openapi-toolkit generate` 命令的标志：

| 标志 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `--x-cli-dravh` | bool | false | 启用 x-cli 驱动命令生成 |
| `--allow-list` | string | "" | API 路径白名单文件 |
| `--disallow-list` | string | "" | API 路径黑名单文件 |
| `--account-aes-key` | string | "" | 覆盖 AES 加密密钥 |
| `--account-aes-iv` | string | "" | 覆盖 AES 加密 IV |

## MCP 服务器标志

`mcp serve` 子命令的标志：

| 标志 | 短标志 | 类型 | 默认值 | 说明 |
|------|--------|------|--------|------|
| `--transport` | `-t` | string | "stdio" | 传输模式 (stdio/streamable-http/sse) |
| `--port` | `-p` | int | 8080 | HTTP 监听端口 |

## Viper 使用模式

### 全局 viper 实例

用于主配置和标志绑定：

```go
viper.SetConfigName("config")
viper.AddConfigPath("/etc/appname/")
viper.AddConfigPath("$HOME/.appname/")
viper.ReadInConfig()

viper.SetEnvPrefix("MY_CLI")
viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
viper.AutomaticEnv()
```

### Cache viper 实例

独立的 viper 实例，用于 Token 缓存：

```go
Cache = viper.New()
Cache.SetConfigName("cache")
Cache.AddConfigPath("$HOME/.appname/")
Cache.ReadInConfig()
```

### Creds viper 实例

独立的 viper 实例，用于凭据管理：

```go
Creds = &CredentialsFile{viper.New(), []string{}, []string{}}
Creds.SetConfigName("credentials")
Creds.AddConfigPath("$HOME/.appname/")
Creds.ReadInConfig()
```

### 命令级 viper 实例

每个 cobra 命令创建独立的 viper 实例用于参数绑定：

```go
params := viper.New()
cmd.Flags().String("fields", "", "Fields to return")
params.BindPFlags(cmd.Flags())
```

## 配置目录结构

```
~/.my-cli/
├── config.json          # 主配置文件
├── cache.json           # Token 缓存
└── credentials.json     # 认证凭据
```

配置目录在 `cli.Init()` 时自动创建（权限 0700）。
