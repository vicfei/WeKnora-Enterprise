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

## 存量库登记簿手术

已按旧编号跑过迁移的数据库（如上海 dev 库），切换到本分支前需执行一次
`scripts/renumber-schema-migrations.sql`（把 `schema_migrations` 里的
84/85/86 改成对应新号）。全新部署的库直接跑即可，无需手术。
