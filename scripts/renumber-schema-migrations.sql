-- 一次性手术：fork 迁移旧编号 → 时间戳编号（2026-09-04，配合 chore/migration-timestamp-renumber 分支）
--
-- 适用对象：已按旧编号(84/85/86)执行过 fork 迁移的既有数据库（如上海 dev 库）。
-- 全新部署的库不需要本脚本。
--
-- 执行时机：停掉 WeKnora 服务后执行（迁移在服务启动时运行，避免并发）。
-- 幂等性：WHERE 条件保证重复执行无害（找不到旧号即为已手术过或全新库）。

-- ── 前置检查：确认当前登记簿状态（应能看到 84/85/86 且 dirty 必须为 false）──
SELECT version, dirty FROM schema_migrations ORDER BY version;

BEGIN;

UPDATE schema_migrations SET version = 20260819221612 WHERE version = 84;  -- tag_tree
UPDATE schema_migrations SET version = 20260821154919 WHERE version = 85;  -- external_identities
UPDATE schema_migrations SET version = 20260827095630 WHERE version = 86;  -- user_space_grants

COMMIT;

-- ── 后置验证：84/85/86 应消失，三个新号各一行且 dirty=false ──
SELECT version, dirty FROM schema_migrations ORDER BY version;

-- 验证后启动新版本服务，它会按新号识别"已应用"并跳过重放。
