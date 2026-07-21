# ScriptCat 桥接协议(WS 版,protocol v1)

> 常量的唯一权威是 [`internal/pkg/protocol/protocol.json`](../internal/pkg/protocol/protocol.json),本文只解释语义;两者冲突以 json 为准。
> 该文件与扩展侧镜像的同步方式见 [development.md](./development.md#协议单源)。本文只讲协议;安全取舍见
> [threat-model.md](./threat-model.md)。英文版后补。

## 1. 角色与拓扑

```text
MCP 客户端 ─ stdio ─→ sctl mcp ──┐(进程内/本机内部,不属于本协议)
CLI 动词(sctl install …)────────┤
                                 ▼
                     sctl serve(daemon,WS server,仅绑定 127.0.0.1:8643)
                                 ▲  本协议 = 这条 WS 连接上的消息
                     ScriptCat 扩展(offscreen WS client)
```

本协议只定义 **扩展 ↔ daemon** 这条 WS 连接。`sctl mcp` / CLI 动词与 daemon 之间是同一二进制的内部通信,不需要跨实现互操作,不在此约定。

角色约定:daemon 是 server 但**不是权威**——脚本数据与写操作决策的权威在扩展侧;daemon 是 MCP 客户端授权(token/scope)的权威,扩展保存镜像用于 UI 与二次校验。

## 2. 传输与 Envelope

- WebSocket,JSON text frame,单帧上限 `limits.maxFrameBytes`(4 MiB),超限即 `PAYLOAD_TOO_LARGE` 或直接断开;
- daemon 仅监听 loopback;`ws://` 明文仅允许 loopback,远程必须 `wss://`(v1 不实现);
- 所有消息共用信封:

```jsonc
{ "v": 1, "type": "<envelopeTypes 之一>", "requestId": "<uuid v4>", "payload": { } }
```

- `requestId` 由发起方生成,应答方原样回填;推送类消息(`pair.request`、`client.sync` 等)也带独立 `requestId` 仅作日志关联;
- 未知 `type`:忽略并记日志(前向兼容);`v` 不等于 1:立即断开;
- 心跳:双方任一侧可发 `ping`(空 payload),对端回 `pong`;建议间隔 `limits.pingIntervalMs`,两个周期无响应视为断线。

## 3. 连接生命周期

```text
connect → auth.challenge → auth.response → auth.ok → hello → [业务消息…] → close
```

握手完成前,除 `auth.*` 外的任何消息导致立即断开。认证在 `limits.authTimeoutMs`(5s)内未完成即断开。**认证失败不回显原因**:统一以 close code 1008 关闭,无详细 reason(不给探测者信息;不做 Origin 判别,唯一闸门是握手本身)。

### 3.1 会话握手(已配对,双向 HMAC)

K = 配对建立的 256-bit 共享密钥。nonce 为 32 字节随机数,一律小写 hex;比较一律恒定时间。

```text
daemon → ext   auth.challenge { nonceD }
ext → daemon   auth.response  { mode: "session", nonceE,
                                hmac: HMAC(K, ctx.sessionExt || nonceD || nonceE) }
daemon 验证 →  auth.ok        { hmac: HMAC(K, ctx.sessionDaemon || nonceE || nonceD) }
ext 验证 → 握手完成
```

任一侧验证失败即断开。nonce 每连接新生成,重放无效。

### 3.2 配对握手(首次/重新配对)

前置:用户在终端执行 `sctl pair`,daemon 生成一次性配对码(Crockford base32,8 字符,展示为 `XXXX-XXXX`,TTL 2 分钟、验证 3 次失败即作废)并打开配对窗口;用户将码填入扩展设置。

从配对码派生临时密钥(salt/info 见 `crypto.context`):

```text
Kp_mac = HKDF-SHA-256(code, salt=pairKdfSalt, info=pairKdfInfoMac)
Kp_enc = HKDF-SHA-256(code, salt=pairKdfSalt, info=pairKdfInfoEnc)
```

握手同 3.1,但 `mode: "pairing"`、MAC 上下文换用 `ctx.pairExt` / `ctx.pairDaemon`、密钥用 `Kp_mac`;daemon 校验通过后生成新的长期密钥 K,在 `auth.ok` 中附加:

```jsonc
{ "hmac": "…", "key": { "ciphertext": "<base64 AES-256-GCM(Kp_enc, K)>", "iv": "<base64>" } }
```

扩展解密并持久化 K(`chrome.storage.local`),daemon 落盘(0600)。配对窗口随即关闭。**重新配对即替换**:daemon 只保存一份 K(单扩展实例),替换时立即断开旧密钥的存活连接。

### 3.3 hello 与版本协商

握手成功后 daemon 立即推送:

```jsonc
{ "type": "hello", "payload": { "daemonVersion": "0.1.0", "protocolVersion": 1 } }
```

扩展校验:`protocolVersion` 不符,或 `daemonVersion < versions.minDaemonVersion` → 断开并将 UI 状态置为「版本过旧」。

### 3.4 关闭

- daemon 计划退出 → 推送 `bridge.shutdown`(空 payload)后关闭;扩展进入指数退避重连;
- 扩展禁用桥接/kill switch → 直接关闭连接即可(无专用消息);kill switch 另通过 `client.revoke` 逐个撤销(见 §6)。

## 4. bridge actions(daemon → ext 请求,扩展执行)

daemon 转发 MCP 工具调用 / CLI 动词为 `bridge.request`:

```jsonc
{ "type": "bridge.request", "requestId": "…", "payload": {
    "protocolVersion": 1,
    "clientId": "<发起客户端,内建 CLI 为 'sctl-cli'>",
    "action": "<actions 之一>",
    "input": { }
} }
```

应答 `bridge.response`(payload 二选一):

```jsonc
{ "ok": true,  "result": { } }
{ "ok": false, "error": { "code": "<errorCodes 之一>", "message": "…" } }
```

鉴权双层:daemon 先按自己的 token/scope 记录过滤;扩展再按镜像的客户端记录独立复核(`INSUFFICIENT_SCOPE`)。`input` 严格白名单校验,多余字段即 `INVALID_REQUEST`。

| action | input | result(ok 时) |
|---|---|---|
| `scripts.list` | `{}` | `{ scripts: ScriptSummary[] }` |
| `scripts.metadata.get` | `{ uuid }` | `ScriptMetadata` |
| `scripts.source.get` | `{ uuid }` | `ScriptSource`(≤ `limits.maxSourceBytes`,超出 `PAYLOAD_TOO_LARGE`) |
| `scripts.install.request` | `{ url }` 或 `{ code }`(二选一,同给或全缺 = `INVALID_REQUEST`) | `{ uuid, name, version?, enabled }`(enabled 默认 false,用户在确认页勾选「立即启用」才为 true) |
| `scripts.toggle.request` | `{ uuid, enable: boolean }` | `{ uuid, enabled }` |
| `scripts.delete.request` | `{ uuid }` | `{ uuid, deleted: true }` |

`ScriptSummary` / `ScriptMetadata` / `ScriptSource` 结构沿用 #1573(`ScriptSource.contentTrust` 恒为 `"untrusted-user-script-source"`,脚本控制的文本永远作为结构化数据返回,不得拼进工具描述或 Markdown)。metadata 层不返回真实更新 URL(可能含 token),只返回 `hasUpdateUrl`。

## 5. 写操作与阻塞语义

写类 action(`actions[*].write == true`)与源码披露(`blocking: "disclosure"`)在扩展侧**挂起**,直到用户在确认页/披露弹窗决策后才回 `bridge.response`:

- 批准 → 执行(执行前重验 TOCTOU 锚点:staged `contentHash`、目标脚本 `existingCodeHash`、客户端未撤销;不符 → `CONFLICT`)→ `ok: true`;
- 拒绝 → `USER_REJECTED`;
- 挂起达 `limits.writeDecisionTtlMs`(5 分钟)→ 作废 → `OPERATION_EXPIRED`;
- 写策略为「直接允许」(全局或该客户端覆盖)→ 不挂起,立即执行返回(源码披露不受此豁免;内建 `sctl-cli` 的源码读取例外,见 [threat-model.md](./threat-model.md) §3)。

**断开即作废(bridge.cancel)**:请求方链路任何一环消失(MCP 客户端超时或 `notifications/cancelled`、shim 退出、CLI Ctrl-C、内部会话断开),daemon 立即发:

```jsonc
{ "type": "bridge.cancel", "requestId": "<原 bridge.request 的 requestId>", "payload": {} }
```

扩展作废对应操作、关闭/失效确认页,且**不再**对该 requestId 发送 `bridge.response`;daemon 忽略取消后迟到的应答。批准与作废在扩展侧**串行仲裁,先到先得,单次生效**——不存在批准已死请求。WS 连接整体断开 = 隐式取消全部在途请求。

确认页语义:误关标签页 ≠ 拒绝,请求保持挂起(至作废/超时),扩展从 popup/设置页「待确认」入口可重新打开;多个待决请求串行展示。

## 6. MCP 客户端管理(daemon 权威,扩展 UI 决策)

**配对**:未知客户端首次调用工具时,daemon 生成 `pairingId` + 8 字符核对码,在请求方终端展示,同时推送:

```jsonc
{ "type": "pair.request", "payload": {
    "pairingId": "…", "clientName": "…", "requestedScopes": ["…"], "code": "XXXXXXXX"
} }
```

扩展弹配对对话框(用户核对两端码一致、勾选授予的 scope),回:

```jsonc
{ "type": "pair.decision", "payload": { "pairingId": "…", "approved": true, "grantedScopes": ["…"] } }
```

批准后 daemon 铸造 clientId/token(token 只存 SHA-256,原文永不过线、不进日志/URL/扩展存储),TTL 2 分钟未决即作废。

**同步**:客户端集合任何变化(新配对、撤销、scope 调整、lastUsed)后,daemon 推送全量镜像:

```jsonc
{ "type": "client.sync", "payload": [ McpClientRecord, … ] }
```

`McpClientRecord = { clientId, displayName, tokenHash, scopes, createdAt, lastUsedAt, revoked }`(#1573 原样)。扩展逐字镜像存储,用于 UI 展示与请求二次校验。

**撤销**:用户在扩展 UI 撤销某客户端 → `client.revoke { clientId }`(ext → daemon)→ daemon 立即失效 token、断开该客户端在途请求(触发其 `bridge.cancel` 路径)、回推 `client.sync`。kill switch = 逐个 revoke + 扩展断开连接。

内建 `sctl-cli` 身份不在 `client.sync` 列表中,不可撤销(与桥接开关同生命周期),审计照常记录。

## 7. 错误码

| code | 触发 |
|---|---|
| `INVALID_REQUEST` | envelope/input 校验失败、未知 action、多余字段 |
| `UNAUTHENTICATED` | daemon 侧 token 无效/已撤销(握手失败不走错误码,直接断开) |
| `INSUFFICIENT_SCOPE` | daemon 或扩展任一层 scope 校验失败 |
| `WRITE_MODE_DISABLED` | 桥接写功能被全局禁用(kill switch 后) |
| `USER_APPROVAL_REQUIRED` | 保留(阻塞模型下正常流程不返回;供非阻塞客户端未来使用) |
| `USER_REJECTED` | 用户在确认页/披露弹窗点了拒绝 |
| `OPERATION_EXPIRED` | 挂起超过 writeDecisionTtlMs 未决 |
| `CONFLICT` | 批准瞬间 TOCTOU 复核失败(staged/目标哈希不符、脚本已变) |
| `NOT_FOUND` | uuid 不存在 |
| `RATE_LIMITED` | 超过 daemon 或扩展侧限流 |
| `PAYLOAD_TOO_LARGE` | 源码超 maxSourceBytes / 帧超 maxFrameBytes |
| `INTERNAL_ERROR` | 其余异常;message 不携带内部细节 |

限流默认值(实现可调,非协议常量):配对尝试 5/分;每客户端读 60/分、写 10/分;审计事件永不记录 token、源码或含凭据的 URL。

## 8. 一致性与安全注记

- **一致性**:扩展侧 conformance test 与 sctl CI 均逐字节比对各自打包的 `protocol.json` 与权威副本;枚举新增走 `protocolVersion` 递增或前向兼容的「未知即忽略」路径;
- **不做 Origin 判别**:防小人不防君子(非浏览器进程可任意伪造),闸门只有握手;
- **威胁边界**:同用户完整权限的本机进程不设防(可读 daemon 密钥文件),见 [threat-model.md](./threat-model.md);
- **扩展实现注记**(非协议约束):挂起请求不得依赖 SW 内存悬挂 Promise(MV3 SW 可休眠),`bridge.response` 由决策事件驱动经 offscreen 回发;offscreen 由 WS 连接保活。
