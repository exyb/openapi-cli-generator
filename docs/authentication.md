# 认证体系详解

OpenAPI Toolkit 提供四种认证方式，都通过 `cli.AuthHandler` 接口统一管理。

## 认证体系架构

```
cli.AuthHandler (接口)
├── account.Handler              — 用户名/密码 → Bearer Token
│   └── AES-CBC 加密存储凭据
├── apikey.Handler               — 静态 API Key
│   └── 支持 Header/Query/Cookie 三种注入方式
├── oauth.AuthCodeHandler        — OAuth2 Authorization Code + PKCE
│   └── 内含 RefreshTokenSource → AuthorizationCodeTokenSource
└── oauth.ClientCredentialsHandler — OAuth2 Client Credentials
    └── 内含 clientcredentials.TokenSource
```

所有认证 Handler 共享 `oauth.TokenHandler()` 的 Token 缓存机制（通过 `cli.Cache`），确保 Token 在 profile 间隔离并在过期时自动刷新。

## account — 账户密码认证

### 概述

适用于基于用户名/密码登录获取 Bearer Token 的 API。凭据使用 AES-CBC 加密存储，Token 自动缓存和刷新。

### 初始化

```go
account.Init("MY_APP", account.ServerURL(func() string {
    server := viper.GetString("server")
    if server == "" {
        server = openapiServers()[viper.GetInt("server-index")]["url"]
    }
    return server
}))
```

Functional options：
- `account.ServerURL(f)` — 设置获取服务器 URL 的函数
- `account.Extra(names...)` — 添加额外的 profile 键名
- `account.Type(name)` — 设置认证类型名称（默认 "account"）

### AES 加密

```go
var (
    aesKey = "change-key-here-"   // 16 字节 AES-128 密钥
    aesIV  = "change-iv-here--"   // 16 字节初始化向量
)
```

- 默认密钥和 IV 可通过 `--account-aes-key` 和 `--account-aes-iv` 在代码生成时覆盖
- `SetAESKey(key)` / `SetAESIV(iv)` — 运行时设置函数
- 加密算法：AES-128-CBC + PKCS7 填充
- 凭据格式：`base64(AES-CBC-encrypt(username:password))`

### OnRequest 流程

```
HTTP 请求发出前
    │
    ├── 检查 Authorization 头是否已存在
    │
    ├── 获取当前 profile 的 credentials 和 url
    │
    ├── 检查缓存 Token
    │       ├── cli.Cache.GetString(tokenKey)
    │       └── cli.Cache.GetTime(expiresKey)
    │
    ├── Token 有效 → 直接使用
    │
    └── Token 过期或不存在
            │
            ├── decryptCredentials(credentials)
            │       ├── base64.Decode(encrypted)
            │       ├── aes.NewCipher(aesKey)
            │       ├── cipher.NewCBCDecrypter(block, aesIV)
            │       ├── CryptBlocks(decrypted, ciphertext)
            │       ├── pkcs7Unpad(decrypted)
            │       └── strings.SplitN(decrypted, ":", 2) → username, password
            │
            ├── loginAndFetchToken(loginURL, username, password, getServerURL)
            │       ├── 构建 HTTP POST 请求 {username, password}
            │       ├── 解析 loginResponse {data.token, data.expiresIn}
            │       └── 解析 JWT exp 字段作为备选过期时间
            │
            ├── 缓存新 Token → cli.Cache.Set() + WriteConfig()
            │
            └── request.Header.Set("Authorization", "Bearer "+token)
```

### 401 重试机制

在生成的操作代码中，如果收到 401 响应：

```go
if resp.StatusCode == 401 {
    account.ForceRefresh()  // 清空缓存 Token
    req2 := cli.Client.Method().URL(url)  // 重建请求
    // ... 重新设置参数和请求体
    resp2, err2 := req2.Do()  // 重试
    if resp2.StatusCode == 401 {
        // 二次 401 → 报错
        return errors.Errorf("HTTP 401: authentication failed even after token refresh")
    }
}
```

### ForceRefresh

```go
func ForceRefresh() {
    cli.Cache.Set(tokenKey, "")
    cli.Cache.Set(expiresKey, time.Time{})
}
```

清空缓存中的 Token 和过期时间，强制下次请求重新登录。

### 环境变量注入

```go
// 环境变量格式: MY_APP_ACCOUNT_CREDENTIALS=base64credentials:loginURL
envKey := envPrefix + "_ACCOUNT_CREDENTIALS"
envValue := os.Getenv(envKey)
```

## apikey — API Key 认证

### 概述

最简单的认证方式，将静态 API Key 注入到请求中。支持三种注入位置。

### 初始化

```go
apikey.Init("my-api", apikey.Location(apikey.LocationHeader), "X-API-Key")
```

### Location 类型

| 值 | 常量 | 注入方式 |
|----|------|----------|
| 0 | `LocationHeader` | `request.Header.Set(key, value)` |
| 1 | `LocationQuery` | URL 查询参数 `?key=value` |
| 2 | `LocationCookie` | `request.AddCookie(&http.Cookie{...})` |

### OnRequest 流程

```
HTTP 请求发出前
    │
    ├── 检查 Authorization 头是否已存在
    │
    ├── 获取当前 profile 的 api-key 值
    │
    └── 根据 Location 注入
            ├── Header → request.Header.Set(headerName, apiKey)
            ├── Query  → req.AddQuery(queryName, apiKey)
            └── Cookie → request.AddCookie(...)
```

## oauth — OAuth 2.0 认证

### 核心组件

```
oauth 包
├── oauth.go
│       ├── TokenHandler()    — Token 缓存/刷新/注入核心
│       └── TokenMiddleware() — gentleman 中间件包装
├── authcode.go
│       ├── AuthorizationCodeTokenSource — PKCE 授权码流程
│       └── AuthCodeHandler              — AuthHandler 实现
├── clientcredentials.go
│       └── ClientCredentialsHandler     — 客户端凭据 AuthHandler 实现
├── refresh.go
│       └── RefreshTokenSource           — 刷新 Token 包装层
└── request.go
        └── requestToken()              — 底层 Token HTTP 请求
```

### TokenHandler — Token 缓存核心

所有 OAuth 流程共享的 Token 管理逻辑：

```
TokenHandler(source, log, request)
    │
    ├── 从 cli.Cache 加载缓存 Token
    │       ├── tokenKey   = "profiles.<profile>.token"
    │       ├── refreshKey = "profiles.<profile>.refresh"
    │       ├── expiresKey = "profiles.<profile>.expires"
    │       └── typeKey    = "profiles.<profile>.type"
    │
    ├── 有缓存 → oauth2.ReuseTokenSource(cached, source)
    │
    ├── source.Token() → 获取新 Token
    │
    ├── Token 变化 → 更新缓存
    │       ├── cli.Cache.Set(tokenKey, ...)
    │       ├── cli.Cache.Set(refreshKey, ...)  // 仅当新 refresh_token 非空
    │       ├── cli.Cache.Set(expiresKey, ...)
    │       └── cli.Cache.WriteConfig()
    │
    └── token.SetAuthHeader(request)
            └── request.Header.Set("Authorization", "Bearer "+accessToken)
```

### Authorization Code + PKCE 流程

```
AuthCodeHandler.OnRequest()
    │
    ├── 创建 AuthorizationCodeTokenSource
    │
    ├── 包装为 RefreshTokenSource
    │       ├── 有 refresh_token → 先尝试刷新
    │       └── 刷新失败 → 回退到 AuthorizationCodeTokenSource
    │
    └── TokenHandler(refreshSource, log, request)

AuthorizationCodeTokenSource.Token()
    │
    ├── 生成 PKCE verifier (32 字节随机数)
    │       └── verifier = base64.RawURLEncoding(verifierBytes)
    │
    ├── 生成 PKCE challenge
    │       └── challenge = base64.RawURLEncoding(SHA256(verifier))
    │
    ├── 构建授权 URL
    │       └── authorizeURL?response_type=code&code_challenge=...&client_id=...
    │
    ├── 启动本地 HTTP 服务器 (:8484)
    │
    ├── 打开浏览器 → 用户登录
    │
    ├── 等待回调 → 获取 authorization code
    │
    ├── 关闭本地服务器
    │
    └── 交换 Token
            └── requestToken(tokenURL, "grant_type=authorization_code&code_verifier=...&code=...")
```

### Client Credentials 流程

```
ClientCredentialsHandler.OnRequest()
    │
    ├── 从 profile 获取 client_id 和 client_secret
    │
    ├── 创建 clientcredentials.TokenSource
    │       └── clientcredentials.Config{ClientID, ClientSecret, TokenURL, ...}
    │
    └── TokenHandler(source, log, request)
```

### RefreshTokenSource

Token 刷新包装层，优先使用 refresh_token：

```
RefreshTokenSource.Token()
    │
    ├── 有 refresh_token
    │       ├── requestToken(tokenURL, "grant_type=refresh_token&...")
    │       └── 成功 → 返回新 Token
    │
    └── 无 refresh_token 或刷新失败
            └── 回退到原始 TokenSource.Token()
```

### requestToken — 底层 Token 请求

```go
func requestToken(tokenURL, payload string) (*oauth2.Token, error)
```

- 发送 `POST` 请求到 tokenURL
- Content-Type: `application/x-www-form-urlencoded`
- 解析响应为 `tokenResponse`（兼容 `expires_in` 和 `expiry` 字段）
- 返回 `oauth2.Token` 对象

## auth0 — Auth0 封装

Auth0 包是对 OAuth 包的上层封装，自动拼接 Auth0 特有的 URL 模式。

### InitClientCredentials

```go
auth0.InitClientCredentials("my-api", "https://mytenant.auth0.com", "my-client-id", "my-client-secret")
```

自动拼接：
- TokenURL = `issuer + "oauth/token"`

### InitAuthCode

```go
auth0.InitAuthCode("my-api", "https://mytenant.auth0.com", "my-client-id")
```

自动拼接：
- AuthorizeURL = `issuer + "authorize"`
- TokenURL = `issuer + "oauth/token"`

## 多认证共存

所有认证方式通过 `cli.UseAuth()` 注册到 `AuthHandlers` 映射中，按 `typeName` 区分：

```go
// 注册多种认证方式
account.Init("MY_APP", ...)     // typeName = "account"
apikey.Init("my-api", ...)      // typeName = "my-api"
auth0.InitClientCredentials(...)// typeName = "" (匿名)

// 添加 profile 时指定类型
my-cli setup add-profile account profile1 <credentials> <url>
my-cli setup add-profile my-api profile2 <api-key>
```

### 认证中间件路由

每个 HTTP 请求会根据当前 profile 的 `type` 字段查找对应的 Handler：

```go
handler := AuthHandlers[profile["type"]]
handler.OnRequest(log, request)
```
