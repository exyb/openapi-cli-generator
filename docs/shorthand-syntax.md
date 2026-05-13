# Shorthand 语法详解

Shorthand 是 OpenAPI Toolkit 的 CLI 输入语法，允许用户在命令行中直接构建结构化请求数据，无需编写 JSON/YAML 文件。

## 概述

Shorthand 语法用于 CLI 命令的位置参数（必填参数之后的部分），与 stdin 管道输入互补，两者可以合并使用。

```bash
# 使用 shorthand 参数
my-cli create-user name: John, email: john@example.com

# 使用 stdin 管道
echo '{"name": "John"}' | my-cli create-user email: john@example.com

# 两者合并：stdin 作为基础，shorthand 覆盖/补充
```

## 语法规则

### 基本键值对

```
key: value
```

值类型自动推断：
- `null` → null
- `true` / `false` → boolean
- `123` → integer
- `1.5` → float
- 其他 → string

### 强制字符串

使用 `~` 修饰符防止自动类型转换：

```
count:~ 42      # 字符串 "42"，而非整数
active:~ true   # 字符串 "true"，而非布尔值
```

### 嵌套对象

使用 `.` 分隔嵌套层级：

```
user.name: John
user.email: john@example.com
```

等价于 JSON：
```json
{"user": {"name": "John", "email": "john@example.com"}}
```

### 分组语法

使用 `{}` 分组同一对象的多个属性：

```
user{name: John, email: john@example.com}
```

等价于上面的嵌套对象。

### 简单数组

使用 `,` 分隔标量值：

```
tags: red, green, blue
```

等价于 JSON：
```json
{"tags": ["red", "green", "blue"]}
```

### 追加数组

使用 `[]` 追加数组元素：

```
items[]: apple
items[]: banana
items[]: cherry
```

等价于 JSON：
```json
{"items": ["apple", "banana", "cherry"]}
```

### 嵌套数组

使用多个 `[]` 表示嵌套数组：

```
matrix[][]: 1
```

### 索引访问

指定数组索引位置：

```
items[0]: first
items[2]: third
```

### 反向引用

以 `.` 开头的键继承上一个键的上下文：

```
user{name: John}
.age: 30
```

等价于：
```
user{name: John, age: 30}
```

以 `[` 开头的键继承上一个数组的上下文：

```
items[]: first
[1]: second
```

### 文件引用

使用 `@` 加载文件内容：

```
data: @input.json        # 加载 JSON 文件（自动解析为结构化数据）
content: @readme.txt     # 加载文本文件（作为字符串）
icon: @%image.png        # 加载文件并 base64 编码
```

文件引用修饰符：
- `@filename` — 加载文件，`.json` 后缀自动解析
- `@~filename` — 强制作为字符串加载（不解析 JSON）
- `@%filename` — 加载并 base64 编码

### 多键值对

使用空格分隔多个键值对：

```
name: John age: 30 active: true
```

## 解析器实现

### PEG 语法

Shorthand 使用 PEG (Parsing Expression Grammar) 语法定义，由 `pigeon` 工具生成解析器。

语法文件：`shorthand/shorthand.peg`
生成文件：`shorthand/generated.go`

### AST 结构

```
AST = []*KeyValue

KeyValue {
    PostProcess bool      // 是否需要后处理（文件引用等）
    Key         *Key
    Value       interface{}
}

Key {
    ResetContext bool      // 是否重置到根上下文
    Parts        []*KeyPart
}

KeyPart {
    Key   string          // 键名
    Index []int           // 数组索引（-1 表示追加）
}
```

### Build 流程

`Build(ast AST)` 将 AST 构建为 `map[string]interface{}`：

1. 遍历每个 KeyValue
2. 如果 Value 是子 AST → 递归 Build
3. 如果 Value 需要后处理 → 处理文件引用
4. 根据 ResetContext 决定是否重置上下文到根
5. 遍历 Key.Parts：
   - 有 Index → 创建/访问数组
   - 无 Index → 创建/访问 map
   - 最后一个 Part → 设置值

### Get — 反向转换

`Get(input map[string]interface{}) string` 将 map 转换回 shorthand 字符串：

- 单键嵌套对象 → `key.subkey: value`
- 多键对象 → `key{a: 1, b: 2}`
- 标量数组 → `key: 1, 2, 3`
- 复杂数组 → `key[]: value`
- 需要转义的字符串 → `key:~ value`
- 长字符串/含换行 → `key: @file`

## 与 stdin 合并

`cli.GetBody()` 实现 stdin 和 shorthand 的合并：

```
1. 读取 stdin 数据
2. 解析 shorthand 参数
3. 如果两者都有数据：
   a. 反序列化 stdin 数据为 map
   b. DeepAssign(stdinMap, shorthandMap) — shorthand 覆盖 stdin
   c. 重新序列化
4. 如果只有一方有数据，直接使用
```

### DeepAssign 规则

- source 中的 map 值 → 递归合并到 target 的对应 key
- source 中的非 map 值 → 直接覆盖 target 的对应 key
- source 中有而 target 中没有的 key → 直接添加

## j 工具

`j/` 目录下提供一个独立的命令行工具，用于将 shorthand 输入转换为 JSON/YAML/TOML 格式：

```bash
# 安装
go install ./j

# 使用
j 'foo.bar: 1, .baz: true'
# 输出: {"foo":{"bar":1},"baz":true}

j --format yaml 'name: John, age: 30'
# 输出: name: John
#       age: 30
```
