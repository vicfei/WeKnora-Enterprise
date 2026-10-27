# WeKnora Enterprise

> Enterprise-grade fork of [Tencent/WeKnora](https://github.com/Tencent/WeKnora) — the open-source LLM knowledge platform — with **13+ production-ready custom features** for corporate deployment.

[![Version](https://img.shields.io/badge/version-v3.1.3%20%2F%20v5.1-blue)](../../tags)
[![Upstream](https://img.shields.io/badge/upstream-v0.8.2%2B102commits-green)](../../releases)
[![License](https://img.shields.io/badge/license-same%20as%20upstream-orange)](../../blob/main/LICENSE)

## What This Fork Adds

### Knowledge Management
| Feature | Since | Description |
|---|---|---|
| **Hierarchical Tag Tree** | v3.1.2 | Tree-structured tags with "tag-what-you-select" semantics; directories can carry tags; subtree union filtering; delete impact preview |
| **Document Version Management** | v5.1 | Upload-as-new-version → pending → confirm → auto-offline old version → rollback; max-3-version retention per KB |
| **Metadata Auto-fill** | v5.1 | LLM extracts doc_date / effective_date / version_label during ingestion (zero extra LLM calls) |
| **KB Routing v2** | v5.1 | LLM-driven knowledge base selection with dictionary cold-start short-circuit and centroid fallback |
| **Time-Aware Retrieval** | v5.1 | Business date governance; time-condition extraction in query rewrite; recency-weighted ranking |

### Security & Compliance
| Feature | Since | Description |
|---|---|---|
| **Portal / Bridge Authentication** | v2 | Per-user JWT via identity bridge (UM号); grants engine with per-request RBAC; multi-space (personal/team/public) |
| **Internet Access Audit Trail** | v3.1.3 | Every web search forwards end-user identity (`X-External-User-ID`) to DMZ gateway for unified audit |
| **Tag Delete Impact Preview** | v3.1.3 | Read-only preview showing exactly how many documents will lose tags before irreversible cascade |
| **HMAC Secret Log Redaction** | v2 | API principal secrets never appear in access logs |

### AI Capabilities
| Feature | Since | Description |
|---|---|---|
| **ReAct Agent Framework** | upstream+ | Multi-step reasoning with tool calling, checkpoints, compaction, human-in-loop approval |
| **Sandbox & Skills** | upstream+ | Docker/Cube/E2B sandbox with skill installation, snapshot management, idle reaping, orphan recovery |
| **Web Search Providers** | v3.1 | Bocha + Doubao (DMZ-compatible) + SearXNG + Tavily + ZhiPu; per-provider config; blacklist filtering |
| **MCP Integration** | upstream+ | MCP client (tool discovery) + MCP server (ask/ingest tools exposed) |
| **Feedback Loop** | v5.1 | Thumbs up/down with reason; Langfuse score dual-write; admin review dashboard |
| **Memory System** | upstream+ | Long-term memory distillation with topic clustering and doc affinity |

## Version Lines

| Line | Branch | Upstream Base | Docker Base | Latest |
|---|---|---|---|---|
| **Production (v3.x)** | `backport/v3.1-bocha-doubao` | v0.8.0 | v0.8.0-base | v3.1.3 |
| **Development (v5.x)** | `main` | v0.8.2 + 102 commits | v0.8.2-base | v5.1 |

Full lineage: [RELEASE-LINEAGE.md](./RELEASE-LINEAGE.md) — authoritative version-to-upstream-base mapping.

## Quick Start

```bash
# Pull images
docker pull wechatopenai/weknora-app:v0.8.0-fork-v3.1.3
docker pull wechatopenai/weknora-ui:v0.8.0-fork-v3.1.3

# Configure
export WEKNORA_VERSION=v0.8.0-fork-v3.1.3
# ... set DB_PASSWORD, REDIS_ADDR, SYSTEM_SIGNING_KEY, etc.

# Deploy
docker compose up -d
```

## Differences from Upstream

This fork maintains a parallel line to [Tencent/WeKnora](https://github.com/Tencent/WeKnora). We periodically merge upstream while preserving our custom features. See [FORK-MIGRATIONS.md](./migrations/FORK-MIGRATIONS.md) for the migration strategy.

Key architectural decisions:
1. **Two-line strategy**: `main` tracks upstream closely; `backport/v3.1-*` is production-stable (locked to v0.8.0 base)
2. **Zero-migration upgrades within a line**: v3.1.1 → v3.1.2 → v3.1.3 require no database changes
3. **Feature flags**: 7 frontend flags (`VITE_ENABLE_TAG_TREE`, `VITE_ENABLE_PORTAL_LIST`, `VITE_ENABLE_BRIDGE_LOGIN`, `VITE_ENABLE_FEEDBACK`, `VITE_ENABLE_DOC_VERSIONS`, etc.) — all must be set at build time
4. **Signed-token API channel**: For Java/Spring backend integration — per-user identity without user provisioning

## Repository Structure

```
├── cmd/server/           # Go backend entry point
├── internal/             # Core logic (agent, sandbox, tag-tree, web-search, ...)
├── frontend/             # Vue 3 + Vite frontend
├── config/               # Prompt templates, agent presets, builtin agents
├── migrations/           # Database migrations (94 for v3.x line)
├── RELEASE-LINEAGE.md    # Version lineage (upstream base ↔ fork version)
├── FORK-MIGRATIONS.md    # Migration strategy for fork ↔ upstream sync
└── docs/                 # Feature documentation
```

## License

Same as upstream [Tencent/WeKnora](https://github.com/Tencent/WeKnora).

## Acknowledgments

- [Tencent/WeKnora](https://github.com/Tencent/WeKnora) — the excellent open-source knowledge platform this fork builds upon
