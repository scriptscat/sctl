# daemon 同时接入多个 ScriptCat 实例

<!-- File: docs/specs/2026-11-02-scriptcat-instances.md -->

> Status: Approved
> Owner: sctl maintainers
> Last updated: 2026-11-02

**目标：** 装在不同浏览器或不同浏览器配置里的 ScriptCat 能同时连上同一个 daemon，各自独立配对、独立命名、可被
CLI 与 MCP 单独指定，互不顶替。

**不可退化的约束：**

- 已发布、握手不声明实例身份的 ScriptCat 继续可用，已有配对不失效。
- 写操作与源码披露的审批权仍在收到请求的那个 ScriptCat 实例上；daemon 不替任何实例批准写入或披露源码。
- sctl Browser 的配对、命名、目标选择和错误码行为不变；配对或忘记一个 ScriptCat 实例不影响任何 sctl Browser
  实例，反之亦然。
- 密钥只经 `fsutil.WriteFileAtomic` 落盘；daemon 侧不向 stdout 写任何东西。

## 问题

1. **daemon 只保留一条 ScriptCat 连接，第二个 ScriptCat 会和第一个互相顶替。** 已验证。
   `internal/daemon/bridge/server.go` 的 `setScriptCat` 只存一个连接，新连接完成登记时以
   `replaced by new connection` 关闭旧连接；`docs/protocol.md` §2.2 也写明 “A new ScriptCat connection replaces the
   previous ScriptCat connection”。ScriptCat 每次握手成功都把重连间隔重置为 2 秒
   （ScriptCat `src/app/service/offscreen/external-access-connect.ts` 的 `verifyAuthOk`），被顶掉的一方 2 秒后重连
   又顶掉另一方。issue #14 报告了普通与隐身（`"incognito": "split"`）两个进程每 2 秒互踢；本地用真实 daemon 加
   照抄 ScriptCat 重连策略的模拟客户端复现：单实例 12 秒内 `handshake.ok` 1 次，两实例 12 秒内 7 次，`sctl status`
   显示 `handshake.ok×8`。
2. **不同浏览器里的 ScriptCat 无法同时被管理。** 已验证。ScriptCat 只有一把全局长期密钥（`<dataDir>/pairing.key`），
   重新配对会覆盖它（`internal/daemon/bridge/conn.go` 中配对成功后 `s.keys.Save`），所以同一时刻只有最后配对的
   ScriptCat 能连上；`scripts.*` 调用带目标会被拒（`internal/daemon/bridge/call.go`，`a target browser does not
   apply to`）。
3. **sctl Browser 已有一套多实例模型，ScriptCat 没有对应能力。** 已验证。sctl Browser 实例按实例 ID 独立配对和
   登记（`browsers.json`），按名称或 ID 前缀指定，列表方法可跨实例合并（`docs/protocol.md` §2.1、§3.1）。

## 角色与用户故事

1. 作为在 Chrome 和 Edge 里都装了 ScriptCat 的用户，我希望两边都能同时连着 daemon，这样我能用一个 sctl 同时管理
   两边的脚本。
2. 作为让 agent 管理脚本的用户，我希望 agent 能看清每个脚本来自哪个浏览器，并只对我指定的那个浏览器写入，这样
   不会误改另一个浏览器的脚本。
3. 作为已经配对过老版 ScriptCat 的用户，我希望升级 sctl 或 ScriptCat 后不用重新配对，这样日常使用不被打断。

## 设计决策

| # | 决策 | 依据与被否方案 |
|---|---|---|
| 1 | 按“安装”划分 ScriptCat 实例：每个浏览器/配置里的 ScriptCat 是一个实例。同一配置里的隐身进程不算独立实例，由 ScriptCat 一侧让它不连接 daemon（不在本 spec 内）。 | 用户决定。普通与隐身进程共用同一份 `chrome.storage.local`（脚本、配对密钥都在里面，issue #14 中两者用同一密钥都能握手成功即为证据），隐身进程看到的数据与普通进程完全相同。被否：隐身进程单列一个实例——列表合并出现重复，审批窗口弹进隐身窗口、窗口一关实例就下线；同一实例下按普通/隐身各留一条连接——CLI 与协议多一个维度却没有可见收益。 |
| 2 | 新版 ScriptCat 在认证响应里声明 `peer: {kind: "scriptcat", instanceId}`，HMAC 用新增的 ScriptCat 实例上下文并绑定实例 ID；能力声明带名称与自报信息。规则与 sctl Browser 对称。 | 复用已有、已在威胁模型中论证过的身份绑定方式（`docs/threat-model.md` 中 “A browser instance's MAC also binds its peer kind and instance ID”）。被否：沿用全局密钥只加一个实例标签——一个实例录下的 MAC 可被另一个实例重放，也无法单独撤销。 |
| 3 | 老版 ScriptCat（认证响应不带 `peer`）照常用 `pairing.key` 连接，作为一个“老版实例”出现；新版 ScriptCat 带实例 ID 的会话握手若未登记，就用 `pairing.key` 校验，通过后把这把密钥归到该实例下并删除 `pairing.key`。 | 用户决定（旧版照常连，升级后免重新配对）。ScriptCat 配对会覆盖 `pairing.key`，同一时刻只有一个 ScriptCat 持有有效的旧密钥，不存在两个实例争夺接管。被否：升级后要求重新配对；不兼容旧版。 |
| 4 | scripts 类命令新增 `--scriptcat <名称|ID前缀>`（环境变量 `SCTL_SCRIPTCAT`），MCP 的 scripts 类工具新增 `scriptcat` 字段；目标解析、合并与错误规则照搬 sctl Browser；`scripts.list` 可合并。 | 用户决定。规则一致降低学习成本，现有合并实现可复用。被否：与 sctl Browser 共用 `--browser`——daemon 判断不出两个扩展是否在同一配置里；多实例时一律要求指定——`list` 的常见用法变得啰嗦。 |
| 5 | ScriptCat 实例名称与 sctl Browser 名称是两套独立命名空间；名称由扩展首次配对时提议，在扩展里改名后重连生效，重名由 daemon 拒绝。 | 用户决定（照搬）。被否：合并进 `sctl browsers`——“browsers” 里出现 ScriptCat 会让人误解，两类名称也得挤进同一命名空间；固定名称不可改。 |
| 6 | 新增 `sctl scriptcats [list]` 与 `sctl scriptcats forget <name|id>`；`sctl status` 逐个列出 ScriptCat 实例。 | 用户决定。与 `sctl browsers` 对称。 |
| 7 | ScriptCat 实例的登记与密钥存在新文件 `<dataDir>/scriptcats.json`，与 `browsers.json` 分开。 | 两套命名空间分开存放，忘记或损坏一方不波及另一方。被否：在 `browsers.json` 里加类型字段——两类实例的名称唯一性规则要在同一文件里分区处理，容易误判。 |

## 握手与身份

**新版 ScriptCat 会话握手。** 前提：实例已登记。ScriptCat 在 `$session.authenticate` 的应答里带
`peer: {kind: "scriptcat", instanceId}`，`instanceId` 为 32 位小写十六进制，HMAC 使用新增的 ScriptCat 实例会话上下文，
签名材料包含实例 ID；daemon 用该实例的密钥校验，回应同样绑定实例 ID。结果：连接以该实例身份进入能力声明。
失败：实例 ID 格式非法、`kind` 未知、MAC 不符，或实例未登记且不满足下文的接管条件，按握手失败处理（以 1008 关闭、
记 `handshake.failed`）。

**新版 ScriptCat 首次配对。** 前提：`sctl connect` 打开了接入窗口。ScriptCat 以配对模式带 `peer` 完成握手，
HMAC 使用新增的 ScriptCat 实例配对上下文并绑定实例 ID。结果：daemon 下发该实例专属的新密钥；登记在能力声明被接受
时才落盘。失败：能力声明因重名被拒时这次配对不落盘。配对一个 ScriptCat 实例不改动 `pairing.key`、其他 ScriptCat
实例或任何 sctl Browser 实例的密钥。

**能力声明。** 新版 ScriptCat 在 `$session.capabilities` 里带 `peer: {name, product, productVersion,
extensionVersion}`，字段格式与 sctl Browser 相同（名称 1–32 位小写字母、数字和 `-`，其余为可选的可打印文本，
最长 64 字节）。daemon 的登记为准：名称在全部已配对 ScriptCat 实例（在线与否）以及老版实例的保留名 `scriptcat`
之间唯一；冲突时以应用错误 `CONFLICT` 应答、保留原名并断开连接。缺失或非法的 `peer` 断开连接。

**老版 ScriptCat。** 前提：认证响应不带 `peer`。行为与现在一致：用 `pairing.key` 校验，作为“老版实例”在线，
保留名为 `scriptcat`、没有实例 ID；新的老版连接顶替旧的老版连接，但从不顶替任何新版实例的连接。

**升级接管。** 前提：新版 ScriptCat 以会话模式带一个未登记的实例 ID 握手，且 `pairing.key` 存在。daemon 改用
`pairing.key` 的密钥按 ScriptCat 实例会话上下文校验。结果：校验通过、能力声明被接受后，daemon 先把这把密钥登记到
该实例名下，再删除 `pairing.key`；此后老版握手不再成立，若此时有老版连接在线则将其断开。失败：校验不通过按握手
失败处理，`pairing.key` 不变；能力声明因重名被拒时不登记、不删除 `pairing.key`；登记写入成功但删除失败时，
登记保留，下次启动或下次接管检查时再删除，不出现两边都失效的状态。

**审计。** 声明了合法 ScriptCat 实例身份的连接，审计事件带 `client: scriptcat:<instanceId>`；老版连接仍为空。

## 路由与目标选择

`scripts.*` 调用可带一个 ScriptCat 目标（实例名称或实例 ID 前缀），先按名称精确匹配，没有命中时才按 ID 前缀匹配
全部已配对实例。保留名 `scriptcat` 只按名称匹配老版实例；老版实例不参与 ID 前缀匹配。

| 目标 | 结果 |
|---|---|
| 未指定，没有实例在线 | 与现在 ScriptCat 未连接时相同的错误 |
| 未指定，恰好一个实例在线 | 发给该实例 |
| 未指定，多个在线，`scripts.list` | 发给全部在线实例并合并结果 |
| 未指定，多个在线，其他 `scripts.*` | `SCRIPTCAT_AMBIGUOUS`，消息列出在线实例 |
| 不匹配任何已配对实例 | `SCRIPTCAT_NOT_FOUND` |
| ID 前缀匹配多个已配对实例 | `SCRIPTCAT_AMBIGUOUS`，消息列出匹配的实例 |
| 匹配到一个未在线的实例 | `SCRIPTCAT_OFFLINE` |
| 匹配到一个在线实例 | 发给该实例 |

合并的 `scripts.list` 规则与 sctl Browser 的合并相同：并发发给每个在线实例，结果数组按实例名称顺序拼接，每条脚本
增加 `scriptcat: {id, name}` 标明来源（老版实例没有 `id`）；任一实例失败则整个调用失败，不返回部分结果；任一实例
`hasMore` 为真则合并结果为真。单实例调用原样返回该实例的结果。

`scripts.*` 调用带 sctl Browser 目标、浏览器方法带 ScriptCat 目标，都以 `INVALID_REQUEST` 拒绝。调用进行中目标连接
断开（合并调用中任一实例断开）时调用作废，请求方收到 `OPERATION_EXPIRED`。写审批与源码披露审批由收到请求的那个
实例弹出并决定。

## CLI 与 MCP

- `get`、`grep`、`edit`、`install`、`enable`、`disable`、`delete` 接受 `--scriptcat <name|id>`，未给时读
  `SCTL_SCRIPTCAT`。新增错误码与对应的 `BROWSER_*` 错误一样按一般错误退出；未给目标时的 `SCRIPTCAT_AMBIGUOUS`
  在消息后提示 `pass --scriptcat <name> or set SCTL_SCRIPTCAT`，与 `BROWSER_AMBIGUOUS` 的提示对称。
- MCP 中对应 `scripts.*` 的工具接受可选字符串字段 `scriptcat`，含义与 `--scriptcat` 相同。
- 多实例合并时，`sctl get` 的表格输出增加来源实例一列；只有一个实例时输出与现在相同。
- `sctl scriptcats [list]` 列出全部已配对 ScriptCat 实例（名称、ID、是否在线、自报产品与版本），以及存在
  `pairing.key` 时的老版实例；`-o json` 输出完整列表。
- `sctl scriptcats forget <name|id>` 删除该实例的密钥与登记，若在线则断开；对 `scriptcat` 删除 `pairing.key` 并断开
  老版连接。不匹配时报 `SCRIPTCAT_NOT_FOUND`。
- `sctl status` 逐个列出 ScriptCat 实例是否在线。`-o json` 保留 `extConnected`（任一 ScriptCat 实例在线即为
  `true`），并新增 ScriptCat 实例列表。
- `sctl connect` 的提示文案不变：同一个一次性配对码可以配对 ScriptCat 或 sctl Browser。

## 数据与安全

`<dataDir>/scriptcats.json` 保存每个 ScriptCat 实例的 ID、唯一名称、长期密钥和最近一次自报的产品与版本，权限与
`browsers.json` 相同，经 `fsutil.WriteFileAtomic` 写入。`pairing.key` 降为老版实例专用，接管后删除。
`docs/threat-model.md` 的凭据清单与审计说明、`docs/protocol.md` 的握手、能力声明与路由章节、`docs/cli.md` 的目标
选择与退出码、`skills/sctl/` 的相应说明在同一变更中更新。

## 不在范围内

- ScriptCat 一侧的实现：声明实例身份、名称设置界面、隐身进程不连接、重连退避调整。分别在 ScriptCat 仓库中完成；
  其中“隐身进程不连接”可独立先行发布，用于修复 issue #14。
- 把 ScriptCat 实例与同一浏览器中的 sctl Browser 实例关联为同一个目标。
- 修改审计事件的日志级别或环形缓冲区容量（issue #14 中提到的 `handshake.ok` 占满缓冲区问题另行处理）。

## 测试决策

| 接缝 | 验证什么 | 已有先例 |
|---|---|---|
| bridge 层（模拟扩展 + 真实 `Server`） | 两个不同实例 ID 同时在线互不顶替；老版与新版共存；升级接管成功后老版握手失败、接管失败时 `pairing.key` 不变；重名 `CONFLICT` 且首次配对不落盘；forget 后会话握手失败；目标解析各分支与错误码；`scripts.list` 合并与来源标注；合并调用中一方断开时作废；审计 `client` 标签 | `internal/daemon/bridge/server_test.go`、`routing_test.go`、`merge_test.go`、`browsers_test.go` |
| 存储层 | `scriptcats.json` 的读写、名称唯一、原子写入与权限 | `internal/daemon/store` 中 `BrowserRegistry` 的测试 |
| controlapi | `/control/call` 透传 ScriptCat 目标；新错误码与 HTTP 映射；状态与实例列表、forget 接口 | `internal/daemon/controlapi/routing_test.go`、`controlapi_test.go` |
| CLI | `--scriptcat` 与 `SCTL_SCRIPTCAT`；新错误码到退出码；`sctl scriptcats` 与 `sctl status` 的输出 | `internal/cli/browsers_test.go`、`cli_test.go` |
| MCP | scripts 类工具 schema 含 `scriptcat` 并透传 | `internal/client/mcpserver` 中带 `browser` 字段的工具测试 |
| 协议 | `protocol.json` 新增上下文、错误码、`mergeField` 后生成文件一致 | `make protocol-check` |

不提交依赖真实浏览器的自动化测试。运行层面用 `e2e/scratch/` 下的模拟客户端驱动真实 daemon，观察：两个不同实例 ID
同时在线时 `handshake.ok` 不再周期增长；老版客户端仍能连接；接管后 `pairing.key` 消失且老版握手失败。真实 ScriptCat
的端到端联调在 ScriptCat 一侧实现完成后进行。

## 未决问题

无。
