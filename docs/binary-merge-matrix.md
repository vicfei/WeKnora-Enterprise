# 二开特性 → v0.9.0 合并冲突检测矩阵（2026-11-03）

> 检测窗口产物。基线：v090-plus @ 6c279927d（upstream main e3fb2335b = v0.9.0+129PR，官方优先）
> 特性源：`backport/v3.1-bocha-doubao` @ d1d5a21d6（v3.1.3 生产线，fork-v3 基线 + 62 提交）
> 性质：**只检测与评估，未做任何合并**。实测方法 = SHA 差集 + 逐文件内容 diff（`git diff v090-plus backport/v3.1-bocha-doubao`）+ 调用点扫描。

## 0. 总判定一览

| 域 | 内容 | 判定 | 预估工作量 | 主要依据 |
|---|---|---|---|---|
| C | 联网搜索 DMZ（bocha/doubao + P1/P2/P4） | **需机械适配（小-中）** | 0.5–1 天 | bocha 已全接通；doubao.go 接口适配已在但**是死代码**（未注册）；DMZ 免鉴权/P2 配额/P4 测试端点全缺；P1 已被 v0.9 等价取代 |
| D | UM 审计链（actor.go / X-External-User-ID） | **可直接合（小）** | 0.5 天（与 C 共用验证） | 依赖（principal 体系/外部用户头/CORS）全部已在 v090；principal.ID 拼接格式逐字一致（auth.go:636） |
| B | 标签树删除影响预览 | **可直接合（小）** | 0.5 天 | 全部载体文件 v090 == 2.0 前夜（80a950ad9），cherry-pick 干净；仅 i18n 需按 6 语言重放 |
| A | 标签树 2.0（4 提交主体） | **后端可直接合；前端需机械+语义适配（中）** | 2–3 天 | 后端 + features/tag-tree/* 与 v090 逐字一致；BatchTagDialog 需手工重放增量块；**TagEditDialog 已被上游删除，2.0 单文档打标需重定向到 KnowledgeTagPopover** |
| E | Portal 聚合 + Grants + UUM bridge | **已在 v090，只查漏** | 0.5 天（仅回归） | bridge/grants/portal 三件套全在；token 登记制雷已排（issueLogin 走 SwitchTenant 原生签发） |
| F | 工程（RUSTUP_DIST_ARG 等） | **可直接合（极小）** | 0.5 天 | Dockerfile.app 移 6 行；**本轮零新增迁移，迁移号雷失效** |

**建议合并顺序（实测结论）**：C+D（同文件同验证，捆一起）→ F（顺手）→ B（小，打通 tag-tree 后端链路）→ A（主体）→ E（仅回归）。
与交接稿预判一致；差异点：C 与 D 建议合并为一批（共享 doubao.go 改动与 DMZ 实弹验证），而非先后两步。

关键结构性事实：merge-base = bb5b37d07；**SHA 级差集仅 9 提交**（C 域 3 + A 域 4 + B/D 共 1 + F 域 1），其余 53 提交已随 fork main 合入 v090-plus。tag-tree 1.0 基座（迁移 20260819_tag_tree、模型、repo/service/handler、前端挂载、搜索集成 `knowledgebase_search.go` 的 TagIDs/ScopeTagIDs）确认已在 v090。

## 1. C 域：联网搜索 DMZ

| 子项 | v090-plus 现状 | 动作 |
|---|---|---|
| bocha provider | **全接通**：文件（254 行，新接口版）+ 注册（container.go:2071）+ 后端元数据 + 前端联合类型 | 无 |
| doubao.go 接口适配 | **已在**：`NewDoubaoProvider` 返回 `interfaces.WebSearcher`，`Search(ctx, types.WebSearchRequest)` | 复用，不动 |
| doubao 注册表项 | **缺**：container.go 中 doubao 0 处引用——doubao.go 目前是死代码 | 加 1 行 `{Type: "doubao", Search: infra_web_search.NewDoubaoProvider}` |
| doubao 后端元数据 | **缺**：types/web_search_provider.go 无 ID:"doubao" 块（bocha 有） | 移植 backport 367-400 行的 ConfigFields 块（time_range/need_content/endpoint）；可按 v0.9 LabelKey 风格微调 |
| doubao 前端联合类型 | **缺**：api/web-search-provider.ts:8 联合类型无 'doubao'（v0.9 另加了 serply/deepseek/searchapi） | 加 `'doubao'` 一词 |
| DMZ 免鉴权适配 | **缺**：v090 版无 endpoint 覆盖（extra_config["endpoint"]）、校验强制 API key、无条件发 Authorization | 移植 backport doubao.go:53-71 的 ~15 行（endpoint 覆盖 + keyless 校验 + 条件省略 Authorization） |
| P2 网页配额（capWebSearchResults） | **缺**：v090 filter_top_k.go（202 行）无任何 web 限流 | 移植 ~40 行；形状兼容（同为 filterTopK 内嵌函数 + sortSearchResultsDeterministically 前插），`types.MatchTypeWebSearch` 在 |
| P4 测试端点 | **缺**：doTestSearch 用 "test" 查询、裸 success 响应 | 移植 ~30 行；**Search 调用需改为新签名** `Search(ctx, types.WebSearchRequest{Query: "人工智能", MaxResults: 2})` |
| P1 文本输入渲染 | **已被 v0.9 等价取代**：WebSearchSettings.vue:279-297 已有 config_fields 通用 `t-select`/`t-input v-else` 渲染 | 无 |
| doubao_dmz_test.go | 缺 | 移植 111 行 + 签名适配 |
| evals 数据（27MB parquet + jsonl） | 部分 | **不移植**（应急评估资产，非运行时依赖） |

注意：C 域**不建议整提交 cherry-pick**（59bc0804f 基于 fork-v3 旧接口，与 v090 的 container/接口结构冲突面大）；手工移植上述清单更省。

## 2. D 域：UM 审计链

- `actor.go`（66 行）+ `actor_test.go`（72 行）自包含，仅依赖 `types.PrincipalFromContext`/`PrincipalAPIExternalUser`/`UserContextKey`——v090 全有（principal.go:13/85）。
- 关键语义校验通过：外部用户 principal.ID 拼接格式 `strconv.FormatUint(tenantID,10) + ":" + externalUserID` 在 v090 auth.go:636/648 与 backport 逐字一致，actor.go 的冒号切分逻辑可逐字移植。
- doubao.go 侧 3 行接线（actor 头 + 日志）与 C 域同文件，同批做。
- `docs/v3.1.3-um-audit-interfaces.md`（89 行）随迁。

## 3. B 域：标签树删除影响预览（f4d7f3e47 的 tag-tree 半边）

- **载体文件零分叉**：tag_tree 的 repo/service/handler/interfaces/types/routes + KBTagTreeSettings.vue + api.ts，v090 与 2.0 前夜（80a950ad9 = 5b2545f35^）diff 为空 → f4d7f3e47 干净 cherry-pick。
- 增量：路由 +2 行（`GET /nodes/:node_id/impact` 只读，Viewer+KBAccessRead）、repo impact 查询 +43、service/handler/interfaces/types 各小块、前端确认框、i18n 每语言 +1 键。
- 守卫语义不变：v090 routes_tag_tree.go 与 v3.1.3 同款（OwnedKBOrAdmin 写守卫已随 1.0 在）。

## 4. A 域：标签树 2.0（5b2545f35 / 64a66911c / 7455fd916 / d1d5a21d6）

| 载体 | 现状 | 动作 |
|---|---|---|
| 后端六件（repo/service/handler/interfaces/types + 单测） | 与 v090 逐字一致 | 4 提交干净 cherry-pick |
| frontend/src/features/tag-tree/*（含新文件 TagTreeSelectPanel.vue、treeRows.test.mjs） | 同上 | 同上 |
| BatchTagDialog.vue | v0.9 上游演进 +400 行分叉（multi_tags 000063 / auto_tag_config 000080 等） | 2.0 增量是**自包含"按层级浏览"块**（flag 门控、复用 getTagTree、选谁打谁）~60 行，手工重放到 v0.9 版本 |
| TagEditDialog.vue | **上游 v0.8→v0.9 已删除**（bfa7f0ed6 "remove unused components and share tag selection logic"）；v0.9 单文档打标 = KnowledgeTagPopover（DocumentListView:356 / DocumentCardView 行内） | 2.0 单文档树形打标块**语义重定向到 KnowledgeTagPopover**——A 域唯一设计点 |
| KnowledgeBase.vue 挂载点 | backport 挂 TagEditDialog；v0.9 无此结构 | 重定向后自然不需要 |
| i18n | 2.0 每语言改 ~12 行语义 + 删 demoteHint + 增键 | 在 v0.9 六语言结构重放；**ja-JP / zh-TW 补译是新工作** |

零 schema 迁移（2.0 定案原文），既有树零影响。

## 5. E 域：Portal + Grants + Bridge（查漏结果）

- 三件套全在 v090：identity_bridge（handler/service/repo/test + container 接线）、UserSpaceGrant（repo/service/sweeper + 9 admin 端点）、portal scope 聚合（knowledgebase_list_all_test 等）。
- **已知雷 #2（token 登记制）已排**：v090 bridge 的 `issueLogin` 走 `userService.SwitchTenant(ctx, user, tenantID, "")` 复用原生签发路径——auth_tokens 登记天然满足（fork main 合并时已解决，非本轮风险）。
- 无合并工作；建议合并完成后跑六账号共享空间回归（~/v090-test-kit/OPERATIONS-LOG.md 的 A-H 台账可复用）。

## 6. F 域：工程

- `RUSTUP_DIST_ARG`（145ba1216）：v090 Dockerfile.app 有 RUSTUP_HOME 但无镜像 ARG → 移 6 行。
- 迁移重编号 runbook / hmac 脱敏（a2e969765，已在）/ swagger 重生成：合并后统一 regenerate 一并处理。
- **本轮 A/B/C/D 零新增迁移** → 已知雷 #6（迁移号撞车）对本轮失效。fork 时间戳迁移（20260819_tag_tree / 20260821_external_identities / 20260827_user_space_grants）已在 migrations/versioned 共存（共 144 文件）。

## 7. 交叉雷区核查结论（对照交接稿六雷）

| # | 雷 | 实测结论 |
|---|---|---|
| 1 | WebSearcher 接口更名 | 已由 v090 doubao.go 适配解决（复用）；仅 P4 的 Search 调用需用新签名 |
| 2 | token 服务端登记制 | E 域 bridge 已天然满足（SwitchTenant 路径）——**雷已排** |
| 3 | 服务签名加参（*time.Time, bool） | A/B/C/D 域不触碰 CreateKnowledgeFromFile 等接口——**雷不触发** |
| 4 | i18n 语言数 | v0.9 实为 **6 语言**（+zh-TW，比交接稿认知多一）；ja-JP/zh-TW 补译为新工作 |
| 5 | OwnedKBOrAdmin / contributor 语义 | tag-tree 路由两线同款守卫，非合并冲突；公司级测试发现 4 列为**合并后回归项** |
| 6 | 迁移号 | 零新增迁移——**雷失效** |

## 8. 执行建议（待用户确认后启动）

1. **批一（C+D+F）**：手工移植 C 域清单 + actor.go 两文件 + Dockerfile 6 行；跑 web_search 单测 + DMZ 实弹（:8080 doubao 连通 + actor 头抓包验证）。
2. **批二（B）**：cherry-pick f4d7f3e47（或仅其 tag-tree 半边手工等价）；i18n 六语言补键。
3. **批三（A）**：cherry-pick 5b2545f35/64a66911c/7455fd916/d1d5a21d6 → 手工重放 BatchTagDialog 块 → KnowledgeTagPopover 重定向（先出小设计稿）→ 六语言 i18n。
4. **批四（E）**：仅回归。
5. 每批：build + 单测 + 台账登记 + commit 即推 GitHub（铁律）；全部完成后发版更新 RELEASE-LINEAGE.md。
