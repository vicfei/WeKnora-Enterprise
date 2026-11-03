# 主题 4 设计：知识文档版本管理（Knowledge Revisions）

> 状态：设计稿 v1（待用户拍板后开工）。目标分支 feature/knowledge-revisions（自 feature/weighted-ranking 切出）。
> 纪律：数据库迁移必须 expand-contract 两阶段、可撤销（FORK-MIGRATIONS.md runbook）。

## 1. 问题：今天的编辑是破坏性的

实测代码路径：

| 操作 | 行为 | 旧数据去向 |
|---|---|---|
| 编辑标题/描述/自定义元数据 | `UpdateKnowledge`（knowledge.go:691）原地覆盖行 | **丢失** |
| 重新解析（同源） | `ReparseKnowledge`（knowledge_process.go:2410）→ `DeleteChunksByKnowledgeID`（:297/:577）+ 向量库 `DeleteByKnowledgeIDList` | chunks/向量**丢失**（源未变则可重建，属可接受） |
| 重传文件/编辑 manual 正文 | 源内容（FilePath/Source/FileHash）原地覆盖 | **丢失** |

没有任何"上一版是什么、谁改的、能不能回去"。RAG 场景的特殊伤害：**答案引用的是哪一版资料无从追溯**——合规场景（资管！）这是硬伤。

对照先例：wiki 页面已有完整版本机制（`wiki_page_revisions`，迁移 000075：append-only 修订行，`UNIQUE(page_id, version)`，含 editor_id/edit_source/edited_at）。本设计把它泛化到知识文档，并补上 wiki 没有的检索层联动。

## 2. 目标与非目标

**目标**
1. 任何内容/元数据变更前自动留档：谁、何时、从什么改成什么；
2. 一键回滚（且回滚本身也留档——审计链不断）；
3. 检索默认只查当前版（存量行为零变化），预留"时间点检索"钩子；
4. 迁移可撤销、存量库零回填风暴。

**非目标（v1 明确不做）**
- 每版本向量快照（即时回滚不重解析）——成本 = 每版一份 embedding，v3 再议；
- 协同编辑/冲突合并（单写者模型，行锁串行化）；
- 跨知识 diff、分支。

## 3. 数据模型

### 3.1 新表 `knowledge_revisions`（照 wiki_page_revisions 骨架 + 文档字段）

```sql
CREATE TABLE knowledge_revisions (
    id                 VARCHAR(36) PRIMARY KEY,
    tenant_id          INTEGER NOT NULL,
    knowledge_base_id  VARCHAR(36) NOT NULL,
    knowledge_id       VARCHAR(36) NOT NULL,
    version            INTEGER NOT NULL,           -- 从 1 起，只增不减
    title              VARCHAR(512) NOT NULL DEFAULT '',
    description        TEXT NOT NULL DEFAULT '',
    content            TEXT NOT NULL DEFAULT '',  -- manual 型正文；文件型为空
    file_name          VARCHAR(512) NOT NULL DEFAULT '',
    file_hash          VARCHAR(64)  NOT NULL DEFAULT '',  -- 内容指纹：去重与变更判定
    file_path          VARCHAR(1024) NOT NULL DEFAULT '', -- 文件型：对象存储键
    storage_size       BIGINT NOT NULL DEFAULT 0,
    custom_metadata    JSON,
    edit_source        VARCHAR(16) NOT NULL DEFAULT '',  -- web/api/reparse/restore
    editor_id          VARCHAR(64) NOT NULL DEFAULT '',
    edited_at          DATETIME DEFAULT CURRENT_TIMESTAMP,
    created_at         DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX idx_knowledge_revisions_kv ON knowledge_revisions (knowledge_id, version);
CREATE INDEX idx_knowledge_revisions_kb ON knowledge_revisions (tenant_id, knowledge_base_id, edited_at DESC);
```

**存储去重（关键经济性设计）**：文件型修订只存 `file_hash + file_path`；重解析同源或内容未变
（hash 相同）的"版本"不复制对象、不新增行（见 4.1 判定）。只有内容真正变化的快照才占新空间。

### 3.2 存量表扩张（expand 阶段，全部可逆）

```sql
ALTER TABLE knowledge ADD COLUMN current_version INTEGER;      -- 先可空
ALTER TABLE chunks      ADD COLUMN revision_id VARCHAR(36);     -- v1.5 时间点检索钩子，先可空
```

### 3.3 迁移时序（expand-contract）

| 阶段 | 动作 | 回滚 |
|---|---|---|
| **Expand**（M1，本设计） | 建表 + 加两列（可空），零行为变化，开关默认关 | down.sql：DROP TABLE + DROP COLUMN |
| **Backfill**（M1 内，惰性） | 不做全量回填：每个知识**首次被写版本时**补一条 baseline v1（当前态快照）；可选异步任务预热 | 无需回滚（纯增量行） |
| **Contract**（M3 后，单独迁移） | `current_version` 收紧 NOT NULL DEFAULT 1、chunks.revision_id 建索引 | 待观察期结束 |

迁移命名沿用 fork 日期式（如 `202609xx_knowledge_revisions.up/.down.sql`），登记 FORK-MIGRATIONS.md；
注意上游 000091 补洞目标顺延。

## 4. 写路径：什么时候、怎么留档

### 4.1 变更判定（防修订爆炸）

```
new_hash = hash(正文或文件内容)
if new_hash == 上一版 hash 且 元数据(标题/描述/自定义)未变 → 不留档（no-op 保存）
else → 在破坏性写入【之前】追加一行 revision（version = knowledge.current_version + 1）
```

留档点插桩三处：
1. `UpdateKnowledge`：元数据变更（含 manual 正文编辑入口）；
2. `ReparseKnowledge` / 重传文件：**先快照后删 chunks**（顺序是正确性关键——现状是先删）；
3. `RestoreRevision`：回滚即"以旧版内容创建新版"（append-only，类似 git revert）。

并发：`UPDATE knowledge SET current_version = current_version + 1 WHERE id=? AND current_version=?`
乐观锁 + 唯一键兜底，冲突重试一次。

### 4.2 回滚语义（设计决策，需拍板确认）

**回滚 = 前进式恢复**：`POST .../revisions/{version}/restore` 以该版内容创建 `version+1` 新修订
（edit_source=restore），再走常规 reparse 重建 chunks/向量。理由：
- 修订表永远 append-only，审计链完整（"谁在何时把文档回滚到了第几版"本身是历史）；
- 不需要"切换指针"语义，实现面小一半；
- 代价：回滚要重解析（embedding 成本），与 v1 不做向量快照一致。

### 4.3 保留策略

`knowledge.max_revisions`（config，默认 50/篇）：超限时删最旧**非 baseline** 修订；
文件对象仅在无任何修订引用该 hash 时才删存储。清理任务复用现有 asynq 队列。

## 5. 读路径：API 与前端

```
GET  /api/v1/knowledge-bases/{kb}/knowledge/{id}/revisions        # 列表：version/editor/source/edited_at/title/hash/size
GET  /api/v1/knowledge-bases/{kb}/knowledge/{id}/revisions/{v}    # 详情（含正文/元数据快照）
POST /api/v1/knowledge-bases/{kb}/knowledge/{id}/revisions/{v}/restore   # 回滚（=新建修订+reparse）
```

- 权限：列表/详情随知识读权限；restore 随编辑权限；
- diff：v1 前端做两版正文的行级对比（纯展示，不落后端）；
- 前端：知识详情抽屉新增「历史版本」页签（**第六个 VITE flag：`VITE_ENABLE_DOC_VERSIONS`** 门控，
  存量构建零变化——沿用四/五 flag 纪律 + streame 式新接口白名单自检）。

## 6. 检索层联动（v1.5 → v3 的渐进路线）

| 阶段 | 能力 | 机制 |
|---|---|---|
| v1 | 检索只查当前版（**与现状完全一致**） | 回滚后重建 chunks，天然成立 |
| v1.5 | 溯源引用 | chunks.revision_id 回填；引用事件携带版本号 → "本答案依据第 N 版" |
| v2 | 时间点检索 | 检索参数 `as_of`：按 chunk 的 revision edited_at 过滤 ≤t 的**当时最新版**（需 chunk 不跨版本混存：回滚重建天然满足） |
| v3 | 即时回滚 | 每版向量快照（按 revision 命名空间）——成本模型清楚前不动 |

**评估协同（真正的 payoff）**：am-qa 题集的 F 型时效题 + 新增"时间点问题"（"按 2022 年报口径，
该基金规模是多少"——库里同时有 2022/2023 两版年报）让"版本检索正确性"第一次可量化；
v2 落地后这也是 recency 权重的第二个天然标尺。

## 7. 测试与验收

- 单测：变更判定（hash 同/异、元数据变更）、append 语义、并发版本冲突、惰性 baseline、保留裁剪、restore 产生新修订；
- 迁移测试：up/down 往返（sqlite 镜像 DDL，照 wiki_page_test.go 先例）；
- 集成：编辑→留档→回滚→重检索全链路（evals 脚本可复用：灌库-改版-回滚-验证引用一致）；
- 验收口径：功能关闭时数据库与网络行为与现状逐位一致（照 weighted-ranking 的奇偶性纪律）。

## 8. 实施切分（建议三棒）

- **M1**（1 棒）：expand 迁移 + 写路径三插桩 + 惰性 baseline + list/get API + 单测；开关默认关；
- **M2**（1 棒）：restore + 前端页签（第六 flag）+ 保留策略 + streame 白名单/双自检；
- **M3**（1 棒）：chunks.revision_id 回填 + 引用溯源 + `as_of` 检索 + am-qa 时间点题集验证。

## 9. 待用户拍板

1. 回滚语义用"前进式恢复"（本设计）还是"指针切换"（体验更快但破坏 append-only）？——**推荐前者**；
2. 默认保留 50 版/篇是否合适（资管合规场景可能要更长，存储成本换）；
3. M1 开关默认关（租户级灰度先开 10011）还是直接全量写档？——**推荐先关后灰度**；
4. 标题/描述这类轻量元数据编辑是否入档（本设计：入，因为它们影响检索与引用展示）；
   若嫌修订列表噪音，可加"仅内容变更入档"配置。
