# 把 sctl Browser 扩展内嵌进 sctl 二进制

<!-- File: docs/specs/2026-10-23-embedded-extension.md -->

> Status: Draft
> Owner: sctl maintainers
> Last updated: 2026-10-23

**目标：** 用户不必再去 GitHub Releases 下载、解压扩展：`sctl browsers install` 把二进制里内嵌的扩展释放到一个固定目录，
用户在浏览器里加载这个目录一次即可；之后 sctl 升级时，daemon 发现扩展落后，就更新这个目录并让扩展重新加载，
扩展版本始终跟着 sctl 走。

**不可退化的约束：**

- 不需要 Node 也能 `go build ./...` 和 `go test ./...`；没有内嵌扩展的构建照常可用，只是 `sctl browsers install` 报错、daemon 不做自动更新。
- 从 Release zip 手动加载的扩展行为不变：daemon 不会改它、也不会让它重新加载。
- 给 ScriptCat 的生成 TS 文件逐字节不变。
- 扩展的会话密钥和实例 ID 在自动更新后保留，不需要重新配对。

## 问题

1. **装扩展要五步手动操作。** 已验证。README 快速开始第 2 步：从 Releases 下载 `sctl-browser-extension-<version>.zip`、
   解压、打开开发者模式、加载已解压的扩展程序、在弹窗输入配对码。
2. **扩展不会跟着 sctl 升级。** 已验证。扩展和 sctl 同版本发布（`.github/workflows/release.yaml` 的 `extension` 任务），
   但升级 sctl 不会更新已加载的扩展；daemon 只把 `extensionVersion` 记下来展示（`sctl browsers` 的 EXTENSION 列），不做任何处理。
3. **静默安装不可行。** 普通用户的 Chrome 只能从应用商店一键安装；外部扩展注册（External Extensions JSON）在 Windows 和 macOS
   上只认指向商店的 update URL；企业策略要管理员权限；品牌版 Chrome 不再接受 `--load-extension`。所以“加载已解压的扩展程序”
   这一步只能由用户做，本 spec 让它只做一次。

## 角色与用户故事

1. 作为新用户，我希望装好 sctl 之后用一条命令拿到扩展目录，在浏览器里加载一次就能配对，这样不用再去下载页找对应版本的 zip。
2. 作为升级了 sctl 的用户，我希望扩展自动变成新版本，配对不丢，这样不用每次升级都重新下载、重新加载扩展。
3. 作为从源码构建的开发者，我希望不装 Node 也能编译和测试 Go 代码。

## 设计决策

| # | 决策 | 依据与被否方案 |
|---|---|---|
| 1 | 用 `go:embed` 把扩展的构建产物内嵌进二进制，`sctl browsers install` 把它释放到 `<数据目录>/browser-extension/`。 | 用户决定（先做嵌入）。目录固定，Chrome 按路径识别已解压的扩展，路径不变才能原地更新。被否：上架应用商店（另行推进，审核不受控）；让用户自选目录（daemon 就得根据扩展上报的路径去改用户指定的目录，扩大了写入面）。 |
| 2 | 内嵌目录在源码里只放一个占位文件，扩展产物由 `make extension-embed` 或发布流程在 `go build` 之前拷进去。 | 约束 1：不需要 Node 也能编译。被否：把构建产物提交进仓库——每次改扩展都要提交一大堆带哈希的文件。 |
| 3 | sctl 写出的扩展目录里放一个标记文件 `sctl-bundle.json`，记录这份扩展内容的摘要（全部文件路径与内容的 SHA-256）。扩展加载后第一次读取它并缓存在 `chrome.storage.session`（扩展重新加载或浏览器重启时清空），在能力声明里上报 `bundle`：上报的是正在运行的代码，而不是磁盘现状——目录可能在扩展运行期间被改写，service worker 休眠重启后读到的新标记会掩盖旧代码。没有标记文件的扩展（从 Release zip 加载的）不上报。 | 摘要比版本号可靠：同一版本号的预发布（`0.3.0-rc.1` 与 `-rc.2` 的 manifest `version` 都是 `0.3.0`）和本地开发构建内容不同，版本号区分不出。标记文件只存在于 sctl 管理的目录里，天然区分“sctl 管的”和“用户手动加载的”（约束 2）。 |
| 4 | daemon 在浏览器实例完成握手后比较：上报了 `bundle`、且与 daemon 内嵌的摘要不同，就把内嵌扩展写进 daemon 自己的扩展目录，再调用新的内部方法 `extension.reload` 让扩展重新加载。 | 已解压的扩展调用 `chrome.runtime.reload()` 会从磁盘重新读取全部文件；存储（实例 ID、会话密钥）跟着扩展 ID 走，路径不变 ID 就不变，配对保留（约束 4）。被否：只在 `sctl browsers` 里提示“扩展过期”——用户还得手动点重新加载。 |
| 5 | `extension.reload` 送达某个实例后（收到回复，或连接因扩展已在重新加载而关闭），这个 daemon 进程不再对它发；送达之前的失败（写目录出错、实例已离线）在它下次握手时重试。 | 扩展可能是从另一个数据目录的扩展目录加载的：daemon 更新的是自己的目录，重新加载后扩展上报的摘要不变，没有这条就会无限重新加载。被否：让扩展上报目录路径、daemon 只改那个目录——写入目标由对端决定，安全面更大，且路径比较受符号链接影响。 |
| 6 | 目录整体替换：先写到同级的临时目录，再把旧目录改名移开、新目录改名就位，最后删除旧目录。 | 扩展文件名带内容哈希，新旧文件混在一起会留下垃圾；整体替换让目录在任一时刻要么是完整的旧版、要么是完整的新版。 |
| 7 | 发布流程先构建扩展，再把产物交给每个平台的二进制构建任务；Homebrew formula 用 `resource` 下载同版本的 Release 扩展 zip 放进内嵌目录。 | formula 从源码构建，没有 Node；Release 里的扩展 zip 就是同一份产物。 |

## 命令

- **`sctl browsers install`**
  - 把内嵌扩展写进 `<数据目录>/browser-extension/`；目录里已是同一份内容时不改动。不经过 daemon。
  - 输出目录路径、扩展版本和加载步骤：打开 `chrome://extensions`，打开开发者模式，点“加载已解压的扩展程序”选这个目录
    （macOS 的选择框默认隐藏 `~/Library`，按 Cmd+Shift+G 粘贴路径），然后用 `sctl connect` 的配对码在扩展弹窗里配对。
  - `-o json` 输出 `{dir, version, changed}`。
  - 二进制没有内嵌扩展时退出码 3，说明这个构建不带扩展，可以从 Release 下载 zip。
- 已加载的扩展目录被 `sctl browsers install` 改写后，正在运行的扩展在下次连上 daemon 时由自动更新让它重新加载。

## 自动更新

- **触发：** 浏览器实例完成能力声明之后（daemon 已回复），daemon 检查它上报的 `bundle`。
- **条件：** daemon 内嵌了扩展；实例上报了 `bundle`；上报值与内嵌摘要不同；这个 daemon 进程还没把 reload 送达过这个实例。
- **动作：** 扩展目录的标记或实际内容与内嵌扩展不同时（文件被删改、标记损坏都算），整体替换目录（决策 6）；然后调用 `extension.reload`。扩展先回复，再重新加载自己；
  重新连上后上报新的摘要，不再触发。
- **失败：** 写目录或调用失败只记 warn 日志，不影响连接和其他命令，下次握手时重试（决策 5）。
- **遗留：** 上次中断留下的临时目录在下一次写入前清扫；旧目录删不掉（例如 Windows 上文件仍被占用）不算失败，同样留给下次清扫。
- **daemon 与 CLI 版本不同：** 目录总是被改成 daemon 内嵌的那一份。升级 sctl 后重启 daemon（`sctl service restart` 或重新运行 `sctl serve`）即可。

## 协议

- `$session.capabilities` 的 `peer` 新增可选字段 `bundle`：64 位小写十六进制；格式不对时与其他非法 `peer` 一样关闭连接。
  旧 daemon 忽略未知字段，新扩展连旧 daemon 不受影响。
- 新增内部方法 `extension.reload`（只对浏览器扩展生成；L0；参数与结果都是空对象）：扩展先回复，再调用 `chrome.runtime.reload()`。
  只有 daemon 的自动更新会调用它，不对 `/control/call`、CLI 或 MCP 开放。

## 构建与发布

- **占位：** `internal/pkg/browserext/dist/` 只提交一个 `.gitkeep`；其余内容被忽略。
- **本地：** `make extension-embed` 构建扩展并拷进内嵌目录，之后 `make build` / `go build` 得到带扩展的二进制。
- **发布：** `extension` 任务额外上传构建产物目录；六个平台的构建任务依赖它，下载后放进内嵌目录再编译。扩展 zip 保持不变。
- **Homebrew：** tap 的 formula 增加 `resource "browser-extension"`（同版本的 Release 扩展 zip），安装时放进内嵌目录；发布流程的
  `homebrew` 任务只改这个 resource 块里的 URL 和 SHA-256。tap 仓库的 formula 改动另提 PR；它合并之前发布流程照常更新源码包，
  只给出警告（装出的 sctl 不带扩展）。

## 安全与隐私

- 自动更新只写 daemon 自己数据目录下的固定目录，写入内容来自二进制本身，不来自对端；对端只能通过上报摘要触发“写入内嵌内容 + 重新加载”，
  而且每个实例只触发一次（决策 5）。持有会话密钥的扩展本来就在信任边界内。
- `extension.reload` 只让扩展重新加载自己，不改变权限或配对。
- 扩展目录不含任何凭据，不进入[威胁模型](../threat-model.md)的凭据清单；但它是浏览器以扩展权限执行的代码，威胁模型的攻击面表加一行。
  数据目录还不存在时按惯例以 0700 创建。

## 不在本期范围

- 上架 Chrome 应用商店。
- 用户自选扩展目录（决策 1）。
- Firefox、Safari。

## 文档

- **README.md 与 README_zh-CN.md：** 快速开始第 2 步改为 `sctl browsers install`，Release zip 作为备选。
- **[cli.md](../cli.md)：** `sctl browsers install` 的行为与自动更新。
- **[mcp.md](../mcp.md)：** 安装扩展的步骤。
- **[protocol.md](../protocol.md)：** `peer.bundle`、`extension.reload`。
- **[architecture.md](../architecture.md)：** 新包与自动更新在 daemon 中的位置。
- **[development.md](../development.md)：** `make extension-embed`、发布流程的变化。
- **[threat-model.md](../threat-model.md)：** 攻击面表加上“经扩展目录植入代码”一行。

## 测试决策

| 测试层 | 验证什么 | 可参照的现有测试 |
|---|---|---|
| Go 内嵌包单元测试（`fstest.MapFS`） | 摘要只取决于文件路径和内容；没有 `manifest.json` 视为未内嵌；安装写出全部文件和标记；同摘要不改动；旧目录被整体替换（旧文件消失）；版本取 `version_name` 优先 | — |
| Go 自动更新单元测试（假 bridge） | 摘要不同 → 写目录并调用 `extension.reload`；相同、未上报、未内嵌 → 什么都不做；同一实例同一摘要只尝试一次；写目录失败不调用 reload | `internal/daemon/page/manager_test.go` |
| bridge 测试 | 握手完成后通知监听者并带上 `bundle`；非法 `bundle` 关闭连接 | `internal/daemon/bridge/server_test.go` |
| CLI 测试 | `browsers install` 的输出、`-o json`、未内嵌时退出码 3 | `internal/cli/browsers_test.go` |
| 扩展单元测试（Vitest） | 有标记文件时能力声明带 `bundle`、没有时不带；`extension.reload` 先回复再重新加载 | `extension/src/offscreen/connection.test.ts`、`extension/src/handlers/debugger.test.ts` |
| 协议生成检查 | 只改 Go 与浏览器扩展的生成代码，给 ScriptCat 的逐字节不变 | `make protocol-check` |
| 一次性真机验证，放在 `e2e/scratch/`，不提交 | Chrome for Testing 加载 `sctl browsers install` 写出的目录并配对；换上内嵌另一份扩展的 daemon 后，扩展自动重新加载、上报新版本、配对保留、只重新加载一次 | [verification.md](../verification.md) |

## 未决问题

无。
