# Fork 迁移编号约定（时间戳制）

> 背景：fork 曾用顺序号 000084–000086，与上游 Tencent/WeKnora 后续发布的
> 000084–000090 撞号（golang-migrate 只认数字版本、按号执行，同号不同文件
> 会被 `schema_migrations` 的"已应用"记录静默跳过）。2026-09-04 起改用
> 时间戳编号，一劳永逸避开上游的顺序号空间。

## 规则

1. **fork 自有的新迁移一律用 14 位时间戳做版本号**（UTC，`YYYYMMDDHHMMSS`），
   上游短序号（目前 ~90）永远追不上 14 位数，不再可能撞号。
2. **上游合并进来的迁移保持原编号**，不需要也不应该重编号。
3. 创建新迁移**只用** `scripts/new-fork-migration.sh <snake_case_name>`，
   不要用 `make migrate-create`（它是 max+1 顺序编号，会重新踩进上游号段）。
4. 本文件放在 `migrations/` 层级而非 `versioned/` 内——迁移目录只放 SQL。

## 已有 fork 迁移的编号映射（2026-09-04 重编号）

| 旧编号（已废弃） | 新编号（=原始编写时间，UTC） | 内容 |
|---|---|---|
| 000084 | 20260819221612 | tag_tree（标签树） |
| 000085 | 20260821154919 | external_identities（UUM↔用户映射） |
| 000086 | 20260827095630 | user_space_grants（授权表 + tenants.space_type） |

## 存量库升级 runbook（登记簿停在旧编号 84/85/86 的库 → fork-v3 合并版）

> 适用：WSL2 彩排库、内网生产库、以及任何已按旧编号跑过 fork 迁移、尚未手术的
> dev 库。升级到含上游 000084–000090 的 fork-v3 镜像前，按序完成 ①②，再换镜像。
> 全新部署的库不需要本 runbook，直接跑即可。

0. **前置**：停掉 WeKnora 服务（迁移在启动时运行，避免并发）；`pg_dump` 全量备份，
   保留至升级验收通过之后。
1. **登记簿手术**：`psql -f scripts/renumber-schema-migrations.sql`
   验证：`schema_migrations` 中 84/85/86 消失，出现 20260819221612 /
   20260821154919 / 20260827095630 三行，dirty=f。
2. **手工按序应用上游 000084–000090**：对 `migrations/versioned/` 下这 7 个
   `.up.sql` 依序 `psql -f`。原因见下方"结构性漏洞"：登记簿版本已是 14 位时间戳，
   golang-migrate 把上游短号全部视为"已越过"而**静默跳过**，等价手工应用是唯一
   处置（先例：2026-09-04 上游 000084–000090）。这些迁移全部用
   `IF NOT EXISTS` 幂等写法，可安全重跑；登记簿版本号**保持 20260827095630 不动**，
   不插入上游短号记录。
3. **换 fork-v3 镜像并启动**：启动日志应报
   `Database is up to date (version: 20260827095630)`。若报 dirty，或尝试重放
   fork 时间戳迁移（报"表已存在"类错误），说明 ①/② 的顺序或状态不对——回滚
   备份，按本 runbook 重新来。

### 上游迁移对象清单（步骤 ② 完成后逐项验证）

| 迁移 | 引入对象 |
|---|---|
| 000084_memory | memory_subjects / memory_items / memory_item_embeddings / memory_doc_affinity / memory_tombstones / memory_topic_stats |
| 000085_message_usage | messages 表 `usage` 列 |
| 000086_tenant_skills | tenant_skills / tenant_skill_snapshots |
| 000087_skill_install_transcript | tenant_skills 的 install_session_id / install_message_id 列 |
| 000088_skill_snapshot_planned_name | tenant_skill_snapshots 的 planned_name 列 |
| 000089_env_vars | tenant_user_env_vars |
| 000090_skill_catalog | tenant_skill_catalog |

### 结构性漏洞与复发处置（规则 2 的代价）

fork 登记簿改用 14 位时间戳后，上游后续所有短号迁移（91、92…）的版本号永远小于
当前登记版本，golang-migrate 会静默跳过它们。因此**每次上游合并后，新增的上游
迁移都要按步骤 ② 的方式等价手工应用（不改编号、不动登记簿）**，并把新号段补进
上面的对象清单。长期出路（需另行拍板）：启动时按"迁移文件 + 目标对象"双重对账，
或 fork 接管上游短号段后统一换空间。
