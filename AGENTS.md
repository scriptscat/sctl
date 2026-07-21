# sctl 工程约定

这是入口页,只放**不可协商的原则**与**指路**;细节各有归属文档,别在这里复制一份。

每条原则目前都靠**评审**守住。

## 原则

### 先复现,再修

**评审。** 报上来的 bug 先亲手复现、拿到失败证据,再写一个失败的测试把它钉住,最后才动手修。
顺序不能颠倒:从假设出发的修复经常在修一个不存在的问题,同时把真正的成因埋得更深。
复现不出来就如实说复现不出来并停下——「看起来显然」不是例外,一行的改动也不是。
一次性复现脚本怎么写见 [docs/verification.md](./docs/verification.md)。

### 测试先行

**评审。** 先写失败的测试,再写实现。测试名说的是**行为**(「扩展未连接时 list 返回退出码 3」),
不是实现细节(「调用了 dispatch」)。测试挂了就改代码,不是改测试。
没有意义的测试(同义反复、只断言 mock、纯转发)直接删——但删之前逐个对着源码确认它确实没在保护什么。

### 修根因,不修症状

**评审。** 不用 `//nolint` 掩盖报错、不吞 error、不为了让某个分支闭嘴而加空判断。
`//nolint` 必须写明具体规则与理由(`nolintlint` 强制),而理由得是「这里确实是规则的例外」,
不能是「让它过」。

### 边界校验,内部不设防

**评审。** 不可信数据进入的地方(WS 信封、`/control/*` 请求、扩展下发的 payload、命令行参数)必须校验;
可信的内部层之间不要再加 `if x == nil` 兜底、不要吞 error、不要留「以防万一」的运行时垫片。
守不住的假设应该 panic 或返回 error,而不是被一个永远不会命中的判断掩盖——
那个判断只会让真正会命中的 bug 更难发现。

### 按进程角色分层,依赖只往下走

**评审。** `client/`(请求侧)与 `daemon/`(守卫侧)互不依赖,
跨进程只经 `/control/*`;`internal/pkg/` 是两侧共享层,只能被上层依赖;`bridge` 不感知 HTTP 控制面。
唯一的例外是守卫侧单向引用 `client/control` 里的共享 DTO。
方向一旦被抄近路打破,两个进程角色就会在编译期焊死,再想拆开只能重写。

### 敏感文件只走一处落盘

**评审。** 长期密钥、控制令牌、客户端存储都用 `internal/pkg/fsutil.WriteFileAtomic`。
非原子写会在崩溃时留下截断内容,直接 `os.WriteFile` 还会漏掉 0600 权限位。

### stdout 只属于 CLI

**评审。** stdout 是 `sctl mcp` 的 JSON-RPC 通道,守卫侧与库层往里写一个字节就会污染 MCP 报文。
诊断信息一律用 `internal/pkg/logging`(恒走 stderr)。

### 用既有扩展点扩展

**评审。** 新动作先进 `protocol.json`(两侧共用的事实源);新动词复用 `internal/cli/dispatch.go` 的
`dispatch` / `dispatchBlocking` 骨架,而不是各自重写连接、取消与退出码映射;新的 `/control/*` 处理器
挂在 `internal/daemon/component.go` 的路由组装处。依赖从构造函数注入,面向窄接口而不是具体类型
(`controlapi.Bridge` 就是这个模式的样板)。不要在共享代码里按类型字符串分支。

### 先复用,再造轮子

**评审。** 写新 helper 前先 `git grep` 一遍有没有现成的。同一段逻辑第二次出现时就抽出来——
一处概念一处实现,修一次就到处都对。但也别为假想的需求提前抽象。

### 改动范围自律

**评审。** 修 bug 就只碰这个 bug 需要的文件。顺手把光标底下过期的注释或说错的文档改对,算在范围内;
顺手重构、批量改名,不算。

### 注释写「为什么」

**评审。** 注释说的是代码表达不了的约束与理由(为什么是 0600、为什么这里必须先解引用符号链接)。
复述步骤的注释会被删掉。同理:不留死代码,不留注释掉的代码块,不留 `// 已移除` 标记——git 记得。

### 文档不说没有的事

**评审。** 声称仓库里有某个文件、函数、标志之前,先用 `git grep` / `git ls-files` 在当前分支上验一遍。
纪律与一次性校验块见 [docs/doc-maintenance.md](./docs/doc-maintenance.md)。

## 架构速查

```text
sctl mcp / CLI 动词  ──/control/* HTTP──▶  sctl serve(daemon)  ──WS──▶  ScriptCat 扩展(审批权威端)
internal/client/            internal/daemon/                     internal/pkg/(两侧共享)
```

权威始终在扩展侧:daemon 不自行批准任何写操作,只转发并阻塞等待浏览器里的人工决策。

## 动手之前先读

| 你要做的事 | 先读 |
|---|---|
| 改包结构、加新包、调依赖方向 | [docs/architecture.md](./docs/architecture.md) |
| 改信封 / 动作 / 限值 / 握手 | [docs/protocol.md](./docs/protocol.md) —— `protocol.json` 是与扩展共用的单一事实源,两侧须同步 |
| 碰鉴权、密钥、配对、审计 | [docs/threat-model.md](./docs/threat-model.md) |
| 构建、跑测试、发版 | [docs/development.md](./docs/development.md) |
| 确认改动"真的能用" | [docs/verification.md](./docs/verification.md) |
| 写文档、改文档 | [docs/doc-maintenance.md](./docs/doc-maintenance.md) |

提交前至少跑一遍 `go build ./... && go vet ./... && go test ./... -race && golangci-lint run ./...`;
声称"修好了 / 通过了"之前必须有真实输出作证据,见 [docs/verification.md](./docs/verification.md)。
