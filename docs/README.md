# sctl Documentation Index

The entry point is [`AGENTS.md`](../AGENTS.md) at the repository root: engineering principles, the
architecture quick-map, and the routing rules for which doc to read before which kind of change. This file is
the index of everything, and doubles as the **ownership table**.

Each fact is expanded in exactly one doc; everywhere else cross-links to it. The reasoning and the
verification method are in [`doc-maintenance.md`](./doc-maintenance.md).

| Doc | Owns |
|---|---|
| [`../README.md`](../README.md) / [`../README_zh-CN.md`](../README_zh-CN.md) | End-user project overview and quick start in English and Simplified Chinese. |
| [`../AGENTS.md`](../AGENTS.md) | Engineering principles and the architecture quick-map. Single source of truth relative to `CLAUDE.md`, which only `@`-imports it. |
| [`architecture.md`](./architecture.md) | Process model, directory layout, per-package responsibilities, dependency direction. **Read before changing package structure or dependency direction.** |
| [`protocol.md`](./protocol.md) | The extension ↔ daemon JSON-RPC 2.0 protocol: handshake, methods, cancellation, limits, and errors. **Read before changing the protocol.** |
| [`threat-model.md`](./threat-model.md) | Security boundaries, attack surface and trade-offs, credentials on disk, daemon-side auditing. **Read before touching auth, keys, pairing, or auditing.** |
| [`mcp.md`](./mcp.md) | End-user installation, first enrollment, generic MCP client configuration, verification, and troubleshooting. |
| [`development.md`](./development.md) | Build commands, test design and commands, static analysis, environment variables, the version floor, branches / CI / releases. **Read before writing code.** |
| [`verification.md`](./verification.md) | How to confirm a change "actually works": evidence, one-shot scripts under `e2e/scratch/`, reproduction discipline. **Read before claiming something is fixed.** |
| [`doc-maintenance.md`](./doc-maintenance.md) | Documentation ownership rules, truth discipline, and the per-claim verification table. **Read before changing docs.** |

Readers who want to *write* user scripts should go to [docs.scriptcat.org](https://docs.scriptcat.org/)
instead; this directory targets sctl contributors and maintainers only.

## Single source of truth

- The only authority for protocol constants is
  [`internal/pkg/protocol/protocol.json`](../internal/pkg/protocol/protocol.json). Generated bindings are consumed by the extension and daemon.
  Generation and reproducibility checks are described in
  [development.md](./development.md#protocol-source-and-generation).
