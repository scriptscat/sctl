# 从 sctl 触发后台/定时脚本运行并读取脚本运行日志

<!-- File: docs/specs/2026-11-04-script-run-logs.md -->

> Status: Approved
> Owner: sctl maintainers（ScriptCat 侧实现由 ScriptCat maintainers 配合）
> Last updated: 2026-11-04

**目标：** 代理和用户能用 `sctl run <uuid>` 让一个后台或定时脚本立即运行一次并拿到本次结果，用
`sctl logs <uuid>` 读取某个脚本的运行日志（含 `GM_log`）；MCP 提供同样的能力和同样的权限。

**不可退化的约束：**

- 已有方法、错误码、退出码表和审批行为不变；已安装脚本的源码、启用状态、存储值不因 `run`/`logs` 改变。
- 旧版 ScriptCat（不认识新方法）继续可用，新方法在它上面给出可理解的失败，而不是挂起。
- CLI 与 MCP 权限一致：同一操作在两条入口上要么都门控、要么都不门控。
- 两个仓库的 `protocol.json` 及生成产物同一版本落地（lockstep）。

## 问题

1. **后台/定时脚本无法从外部验证。** 已验证。它们运行在 ScriptCat 的 offscreen/sandbox 里，不是网页；sctl 无法
   attach 扩展页面（`skills/sctl/SKILL.md` 的 `PAGE_NOT_AUTOMATABLE`），`protocol.json` 的 `methods` 里也没有触发
   运行或读取日志的方法。ScriptCat 仓库 PR #1824 的 `scriptcat-userscript` Skill 因此只能让代理把这类脚本交给
   用户在脚本列表里人工查看。
2. **运行入口和日志已经存在，只是没对外暴露。** 已验证。ScriptCat 编辑器和脚本列表的“运行”走 SW
   `RuntimeService.runScript(uuid)` → offscreen → sandbox `execScript(..., { execOnce: true })`
   （ScriptCat `src/app/service/service_worker/runtime.ts:1726`、`src/app/service/sandbox/runtime.ts:331`）；
   运行状态经 `scriptRunStatus` 消息发布（`src/app/service/service_worker/script.ts` `updateRunStatus`）。日志存
   在 `LoggerDAO`（`logger` 表，`src/app/repo/logger.ts`），`GM_log` 与沙盒的
   `exec script complete` / `exec script error` / `backscript return not promise` 都带 `label.uuid`。

## 参与者与用户故事

1. 作为编写后台脚本的代理，我想在安装后立即运行一次并拿到 complete/error 和本次日志，从而不必请用户去脚本列表
   看结果。
2. 作为编写定时脚本的代理，我想不改 `@crontab` 就能跑一次，从而验证逻辑本身。
3. 作为排查问题的用户或代理，我想按脚本读取最近的运行日志（含页面脚本的 `GM_log`）。

## 设计决策

| # | 决策 | 依据与被否决方案 |
|---|---|---|
| 1 | `run` 与 `logs` 都**不门控**，立即执行 | 维护者决定（2026-11-04）。与现有模型中免审批的页面自动化、`debug console`/`debug request --body` 同级：被运行的代码已在安装时经用户确认。否决：run 走写策略、logs 走源码读取策略 —— 迭代时每次都要审批，维护者选择不采用 |
| 2 | `run` 默认阻塞到本次运行结束，`--no-wait` 触发即返回 | 维护者决定。代理一条命令完成验证。否决：只做触发即返回 —— 代理需要自己轮询 |
| 3 | 只允许后台（`@background`）和定时（`@crontab`）脚本 | 与 ScriptCat 编辑器“只有后台/定时脚本能运行”的现有限制一致（`ScriptEditor/index.tsx` 的 `only_background_scheduled_can_run`）。页面脚本没有“运行一次”的语义 |
| 4 | 复用 ScriptCat 现有“运行”通路（execOnce），不新增执行路径 | 行为与用户点“运行”完全一致，包括 execOnce 下 `CATRetryError` 不排重试。否决：新造带重试的执行模式 —— 与 UI 语义分叉 |
| 5 | 不改退出码表：运行结果为 error 时命令仍以 0 退出，状态在输出里 | `docs/cli.md` 的退出码只表示请求本身的成败（1 拒绝、2 作废、3 其他错误）。否决：error 映射成非零 —— 让“脚本失败”和“命令失败”混淆 |
| 6 | 日志里脚本可控的文本标注 `contentTrust: untrusted-user-script-log` | 与源码的 `untrusted-user-script-source` 同理：脚本可控文本必须作为数据返回，不得拼进工具描述 |

## 运行一次：`sctl run <uuid>` / MCP `scripts_run`

**前置条件：** 目标 ScriptCat 实例在线（多实例时按现有 `--scriptcat` / `SCTL_SCRIPTCAT` 规则选择）；脚本存在且是
后台或定时脚本；脚本是否启用不影响能否运行（与编辑器“运行”一致）。

**动作：** 请求 ScriptCat 立即运行一次该脚本。若该脚本此刻已有一次运行在进行，沿用 ScriptCat 现有行为：先停止
旧运行再开始新运行。

**结果（默认等待）：** 阻塞直到本次运行离开 running 状态，或到达超时（默认 60 秒，`--timeout <duration>`，
MCP `timeoutMs`）。返回：

- `status`：`complete` / `error` / `running`（超时仍未结束，常见于常驻后台脚本，**不是**失败）；
- `error`：status 为 error 时 ScriptCat 记录的错误信息（`CATRetryError` 的消息同样出现在这里）；
- `startedAt`、`endedAt`（未结束则无）；
- `logs`：本次运行开始后该脚本产生的日志，最多 100 条、oldest first，格式同 `logs`。

**结果（`--no-wait`，MCP `wait: false`）：** 运行已被 ScriptCat 接受即返回 `status: running` 与 `startedAt`。

**失败：**

- 脚本不存在 → `NOT_FOUND`（退出码 3）。
- 页面脚本 → `INVALID_REQUEST`（退出码 3），消息说明只有后台/定时脚本可以运行。
- 等待期间 ScriptCat 断开 → 退出码 2（作废），与其他阻塞调用一致。
- Ctrl-C → 退出码 2；已触发的运行不被撤回（ScriptCat 侧继续执行）。
- ScriptCat 不支持该方法（旧版本）→ `METHOD_NOT_FOUND`（退出码 3），CLI 提示升级 ScriptCat。

人类可读输出：一行状态（含耗时或“仍在运行”），随后是本次日志表格。`-o json` 输出上述结构。

## 读取日志：`sctl logs <uuid>` / MCP `scripts_logs`

**前置条件：** 目标 ScriptCat 实例在线；脚本存在（任何类型，页面脚本的 `GM_log` 同样可读）。

**动作与结果：** 返回 ScriptCat 日志中 `label.uuid` 等于该脚本的记录，但不含外部接入审计记录（它们也带 `uuid`，
且每次 `logs` 调用自身就会产生一条），oldest first。每条含时间、级别
（`debug`/`info`/`warn`/`error`）、消息、来源组件（如 `GM_log`）以及 ScriptCat 附带的结构化字段。过滤：

- `--since <duration|RFC3339>`：只看此后的记录，默认最近 1 小时；
- `--level <L>`：该级别及以上；
- `--text <T>`：消息包含子串（不区分大小写）；
- `--limit <N>`：最多 N 条（默认 100），超出时返回最新的 N 条并标注 `truncated: true`。

响应总大小受 `limits.maxFrameBytes` 约束；超限时按条截断并标注 `truncated: true`，不报错。

**失败：** 脚本不存在 → `NOT_FOUND`；旧版 ScriptCat → `METHOD_NOT_FOUND` 并提示升级；无匹配记录 → 空列表，退出 0。

日志保留期由 ScriptCat 现有日志清理设置决定，sctl 不改变它。

## 安全与隐私

- 两个操作都不弹审批页，也不受写策略/源码读取策略影响（决策 1）。`docs/threat-model.md` 的“A process that reaches
  the daemon requests a privileged action”行与相关段落要把“触发后台/定时脚本运行、读取脚本运行日志”加入未门控
  的能力清单，并写明残余风险：持有控制 token 的进程可以反复触发已安装的后台脚本（其副作用如网络请求、通知、
  写存储随之发生），并读到脚本 `GM_log` 过的任何内容。该风险由维护者接受（2026-11-04）。
- 两个操作都写入 ScriptCat 侧外部接入审计（与现有方法相同的审计通道），记录 clientId 与脚本 uuid，不记录日志正文。
- `run` 本身不修改脚本源码和启用状态；运行期间脚本自身的行为（写存储、发请求、发通知等）照常发生。

## 兼容性

- 新方法在 `protocol.json` 中定义（peer 为 `scriptcat`），重新生成两侧产物；ScriptCat 侧生成文件改变，必须与配对的
  ScriptCat 提交一起落地。`schemaVersion` **保持不变**：daemon 握手要求版本完全相等
  （`internal/daemon/bridge/conn.go:367`），升版本会让旧 ScriptCat 连不上；新增 ScriptCat 侧定义不升版本的前例是
  #16。`docs/protocol.md` 生成产物一节中“scriptcat 定义会改变 `schemaVersion`”的说法与此不符，一并更正。
- 按 `docs/protocol.md` §3.1，未声明新方法的旧 ScriptCat 连接对这两个调用返回 `METHOD_NOT_FOUND`，CLI/MCP 据此
  提示升级 ScriptCat。
- MCP 工具描述只描述能力，不包含任何脚本可控文本。

## 不在范围内

- 停止正在运行的后台脚本（`sctl stop`）。
- 持续跟随日志（`--follow`）。
- 页面脚本的“运行一次”、定时脚本的下次运行时间修改。
- 改变 `CATRetryError` 在手动运行下不排重试的现有语义。
- 为这两个操作新增审批或策略开关。

## 测试决策

| 接缝 | 验证什么 | 已有先例 |
|---|---|---|
| sctl 协议一致性测试 | 新方法与类型出现在生成产物中，ScriptCat 侧生成产物与 sctl 一致 | `protocolschema/validate_test.go`；ScriptCat external_access 的协议一致性测试 |
| ScriptCat bridge 单元测试 | `scripts.run` 对后台/定时脚本调用运行通路、对页面脚本返回 `INVALID_REQUEST`、等待模式下按 `scriptRunStatus` 返回 complete/error/超时 running、`--no-wait` 立即返回；`scripts.logs` 按 uuid/since/level/text/limit 过滤与截断 | `external_access/bridge` 现有方法的测试 |
| sctl CLI/daemon 测试 | 参数解析、默认值、超时、断开作废（退出码 2）、`METHOD_NOT_FOUND` 升级提示、人类可读与 JSON 输出 | 现有 `install`/`get` 的 CLI 测试 |
| MCP 工具测试 | `scripts_run`/`scripts_logs` 的参数与 CLI 等价，不含脚本可控文本于描述 | 现有 `scripts_*` 工具测试 |

无法单测覆盖的是真实浏览器中 offscreen/sandbox 的实际运行与日志落盘时序：收尾时用真实 ScriptCat + daemon 跑一次
（安装一个后台脚本 → `sctl run` 拿到 complete 与 `GM_log` → `sctl logs` 读到同样记录 → 常驻脚本超时返回
running），按 sctl `docs/verification.md` 记录。

## 待定问题

（无）
