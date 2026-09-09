# 上游问题登记簿（Upstream Findings Register）

> **用途**：登记二开过程中实测发现的官方库（Tencent/WeKnora）bug、不合理设计与体验债，
> 按"可回馈上游 PR 候选"优先级排序，作为**新加坡节点（GitHub 网关 + PR 工作台）**的工作输入。
> **来源**：上海节点第 4 棒（2026-09-07，分支 feature/weighted-ranking 期间）实测 + 前几棒交接档记录。
> **维护规则**：每条必须附证据（file:line / 日志 / 复现步骤）；状态变化时更新"状态"列；
> 回馈上游前先在新加波节点对 upstream/main 复现，fork 独有代码路径的问题不回馈。
> **纪律**：回馈 PR 的分支从 upstream/main 切出（不带二开 flag 逻辑），提交前 grep 自查无密钥。

## 总览（按 PR 候选优先级）

| # | 优先级 | 问题 | 类型 | PR 可行性 | 状态 |
|---|---|---|---|---|---|
| U-01 | P0 | chat 链路从不接入 rerank 模型，精排阶段死代码 | 设计缺陷/bug | ★★★ 修法清晰、收益巨大 | fork 已绕过，待回馈 |
| U-02 | P0 | 混合检索单路降级时分数不归一，复合分饱和 | bug | ★★★ 小修+易测 | 待修 |
| U-03 | P1 | 排序公式在 chat/agent 两条路径重复实现 | 代码重复 | ★★★ 纯重构零风险 | fork 部分处理（留分叉） |
| U-04 | P1 | SSE 偶发不收尾（答案落库、complete 事件丢失） | bug | ★★ 需先定位根因 | 待定位 |
| U-05 | P1 | embedding 批量调用无退避节流（429 连环） | 韧性缺失 | ★★ 修法标准 | 待修 |
| U-06 | P2 | 查询向量失败静默降级为关键词检索，无任何观测标记 | 设计缺陷 | ★★ 加响应标记即可 | 待修 |
| U-07 | P2 | 会话标题并行"抢答"+ 零校验 | 设计不合理 | ★★ 自包含好提 | 待修 |
| U-08 | P2 | 前端 streame.ts 白名单重建请求体，新字段静默丢失 | 脆弱设计 | ★★ 可提透传/告警 | fork 已防（自检 grep） |
| U-09 | P2 | 快速问答不选库=默认检索全租户库，范围不可见 | 设计问题 | ★ 需 upstream 讨论 | fork 已绕过（kb-routing） |
| U-10 | P3 | 新手引导遮罩拦截操作 / 模式切换失败无引导 / 图标无 aria | UX/可访问性债 | ★（aria 项适合首 PR） | 待修 |
| U-11 | P3 | 知识对象状态字段语义混乱（status/parse_status/enable_status） | API 一致性 | ★ 需讨论 | 记录在案 |
| U-12 | P3 | KB 路由质心缓存不持久化，冷缓存首问 36-38s | 特性缺口 | ★ 属新特性 | fork 记录在案 |
| U-13 | P1 | agent 启动期失败（如 rerank 未配置）SSE 流不收尾，客户端挂死 | bug | ★★★ 修法清晰 | 2026-09-08 实测登记 |
| U-14 | P0 | runtime_context 渲染全部知识库目录，无总量上限——多库租户的 kb=all 智能体首轮即塞爆窗口 | bug | ★★★ 修法清晰 | 2026-09-09 实测登记（附烧钱事故） |

---

## P0（强烈建议回馈上游）

### U-01 chat 链路从不接入 rerank 模型——精排对聊天用户是死代码

- **证据**：
  - `internal/application/service/chat_pipeline/rerank.go` OnEvent：`RerankModelID == ""` → `skip reason="empty_model_id"`（实测日志 2026-09-07 15:49:06）；
  - chat 入口播种 PipelineRequest（`session_knowledge_qa.go:118-163`）从不设置 RerankModelID，仅 agent 覆盖（`session_qa_helpers.go:229`）可设；
  - 对照：search-only API（同文件 ~:905-918）有"RetrievalConfig.RerankModelID → 否则自动选第一个可用 rerank 模型"的完整解析——**同一服务两条入口行为不一致**。
- **影响**：租户配置了 rerank 模型，聊天问答也从不精排；复合分（0.6/0.3/0.1）+ MMR + FAQ 加权整条链路对普通用户不生效。上海节点实测（eval-100 池模式，报告 `:5173/report-weighted-ranking-v1-100.html`）：接入后金标 top3 70.5→83.2、top5 77.0→90.3、EM 47.0→53.1。
- **建议修法（upstream PR）**：把 search-only 的解析逻辑提取为共享 helper，chat 入口同样执行"租户 RetrievalConfig → 自动选第一个可用"。注意在 PR 里说明这是行为变更（默认开启 rerank 调用），可加全局开关（如 ConversationConfig.EnableRerank 或沿用现有 enable_rerank）。
- **状态**：fork 用请求级 `rerank_model_id`（"auto"）绕过（feature/weighted-ranking @ 5d117c48），未动默认行为。

### U-02 混合检索单路降级时分数不归一，复合分饱和

- **证据**：`internal/application/service/knowledgebase_search_fusion.go`（fuseOrDeduplicate）：双路在场走 RRF（分数 ~0.01 量级）；**单路时 deduplicateByScore 保留原始分**。实测 2026-09-07 16:11（向量检索因限流产 0 条）关键词单路 base score=11.504，复合分 0.6×0.23+0.3×11.5+0.1 钳位到 1.0。
- **影响**：单路降级时多候选复合分集体饱和=1.0，排序退化到 tie-breaker（KnowledgeID/ChunkIndex 字典序）。
- **建议修法**：单路分数过一遍既有的 `retriever.EngineAwareNormalizer`（knowledgebase_search.go:249 已在用）再出融合层；补一个"单路分数必须∈[0,1]"的单测。
- **状态**：待修（下一棒候选）。

## P1（值得回馈）

### U-03 compositeScore/applyMMR 双路径重复实现

- **证据**：`chat_pipeline/rerank.go:439-538` 与 `internal/agent/tools/knowledge_search.go:1209-1249` 两份近似拷贝。
- **影响**：必然漂移——fork 做权重可配置时只改了 chat 路径，agent 路径已是已知分叉（提交 8a77b068 注明）。
- **建议修法**：提取到共享包（如 internal/searchutil），两处调用。纯重构，零行为变更，PR 风险最低。

### U-04 SSE 偶发不收尾

- **证据**：交接档记录 + 本轮评估 arm② 2 例（hp-0016 done=true 但 answer 空；hp-0035 客户端 180s 超时，答案已在 DB）。DB 有完整答案而 complete 事件未发。
- **建议**：先在 upstream/main 加日志复现定位（疑点：流式 writer 异常路径漏发 complete），根因明确后再提 PR。定位期间评估端用 DB 补录（已有实践）。

### U-05 embedding 批量调用无退避节流

- **证据**：kb_router 冷缓存批量算质心（`kb_router.go:255` computeKBCentroid），实测一次 8 连发 429 + 大库批次（4955 篇池）持续 500 Internal（2026-09-07 16:11 日志）。
- **建议修法**：BatchEmbed 加指数退避重试 + 并发上限/分批（标准云服务客户端姿势）。对 upstream 同样成立（任何多 KB 租户都可能触发）。

### U-13 agent 启动期失败时 SSE 流不收尾

- **证据**（2026-09-08 实测，agent-lab）：配置 KB+knowledge_search 但 rerank_model_id 为空的智能体，POST /api/v1/agent-chat 后 `AgentQA` 在引擎启动前返回错误（日志 qa.go:1072 "rerank model is not configured"，EventError 已 emit）；`AgentStreamHandler.handleError`（agent_stream_handler.go:594）只 AppendEvent(Done:true) 不终结流；引擎的 defer emitCompletion（executeLoop 内）只覆盖引擎已启动的路径。客户端无超时则永久挂起（实测 60s 客户端超时，期间无任何字节抵达）。同类问题可能覆盖一切"AgentQA 装配期 return err"的分支（模型缺失、附件 staging 失败等）。
- **建议修法**：handleError 在 AppendEvent 后终结 SSE 流（与 complete 事件同路径收尾）；或在 executeQA 的 serviceErr 分支兜底 emit 一个终结性事件。与 U-04（偶发不收尾）可能同族，建议一起定位。

### U-14 runtime_context 知识库目录无上限，kb=all 智能体在多库租户上首轮即塞爆上下文

- **证据**（2026-09-08/09 实测，agent-lab 评估事故）：kb_selection_mode=all 的智能体在 541 库租户上，`formatKnowledgeBaseList`（internal/agent/prompts.go:129）把全部 KB 逐个渲染进用户消息的 `<runtime_context>`（每库含 id/name/描述 + 最多 2 篇近期文档及摘要），**没有任何总量上限**。实测第一轮用户消息 = **219,319 token**（`largest=user(219319)`），超过 200k 窗口本身。后果链：估算器正确报警（222k > 183,616 阈值）→ 压缩器正确拒绝（"Nothing outside the keep-recent budget"——重量就在当前轮）→ provider 截断输入（实测 prompt=175,640，finish=length，completion=1，内容为空）→ 空答 nudge 循环 15 轮 → 迭代耗尽合成兜底。**单题烧掉 ~260 万 token（175k × 15 轮）**；评估事故中 40 题在 26 分钟内烧穿 ~1 亿 token 导致账户 402（2026-09-08 03:08，DeepSeek）。开启 kb-routing 的同批问题不受影响（范围收窄到 3 库后用户消息正常 ~1.9k token）。
- **影响**：任何"允许全部库"的智能体（含内置智能推理默认形态）在库数成百的租户上**功能性瘫痪且大量烧钱**；智能体越诚实（多轮重试）烧得越多。
- **建议修法**：① runtime_context 目录设总量预算（如 >N 库时只渲染"N 个知识库可用，用 knowledge_search 检索"的摘要形态，去掉逐库 recent_documents 采样）；② 引擎加首轮守卫：用户轮估算超窗口 X% 时立即终止并报清晰错误，而不是 15 轮空转（每轮都是全价 prompt）。
- **状态**：已在 fork 的评估中实测定位（docs/design/agent-harness-design.md §案例研究），待修。

## P2（可选回馈 / 内部先修）

### U-06 向量检索失败静默降级无观测标记
- 证据：16:11:52 请求 `vector_len=0`，只剩关键词一路、仅 1 候选，用户端无任何异常。建议：响应/SSE 事件加 `degraded: keyword-only` 标记 + 日志 WARN（现在是 INFO 级 group_plan 才能看出来）。

### U-07 会话标题并行"抢答"+ 零校验
- 证据：`session.go:822+` 标题为独立 LLM 调用（仅喂问题、温度 0.3、与 RAG 并行，提问后 2s 落库）；实测 6 条标题 5 条直接是答案（"40"、"Yes, they were both American." 等，违反"中文 4-10 词、不要回答"指令）；生成后仅长度截断（sanitizeGeneratedTitle），无语言/字数/语义校验。
- 建议修法：提示词加 few-shot 硬约束 + 生成后校验（不合格回退为问题前 N 字），或挪到答案完成后基于答案主题生成。

### U-08 前端请求体白名单重建
- 证据：`frontend/src/api/chat/streame.ts:86-130` 逐字段手工拷贝；kb-routing 曾因此静默丢字段（修复见 c6721e34 缺陷 1）。
- 建议回馈：至少对 params 中未识别的顶层字段打 console.warn；理想是改成显式透传未知字段或集中字段注册表。fork 侧已靠"新字段必须进白名单+构建后双自检 grep"纪律防护。

### U-09 快速问答默认全租户库检索且不可见
- 证据：builtin-quick-answer 伪智能体默认填全部库（实测 combined_kb_count=541）；工具栏"知识库"弹层点卡片=跳详情页，无勾选/确认控件把库选进对话范围。
- 定位：属产品设计讨论（默认范围该多大、UI 入口长什么样），适合先开 issue 征求 maintainer 意见再动代码。fork 侧 kb-routing（自动选库）已是另一种解法。

## P3（记录在案 / 小 PR）

### U-10 UX 与可访问性三件套
- 新手引导遮罩拦截工具栏点击、每个新浏览器环境全量重播（guide__backdrop--hit/full 实测拦截 hover/click）；
- 「智能推理」切换在内置智能体未配置时仅弹一闪 toast、模式不动（`Input-field.vue:2168-2196` selectAgentMode 守卫），缺下一步引导；
- 工具栏图标按钮无 aria-label（ariaSnapshot 全是裸 img）——**此项是理想的首个贡献 PR**（纯增量、零风险）。

### U-11 知识对象状态字段语义混乱
- manual 灌库接口 `status` 只接受 draft/publish；就绪与否要看 `parse_status`/`enable_status`，并没有 `status` 就绪字段（前几棒灌库脚本踩坑）。

### U-12 KB 路由质心缓存不持久化
- 15 分钟 TTL 过期全量重算，冷缓存首问 36-38s（fork kb_router.go 已知问题，交接档有记录）。属新特性（缓存持久化/预热），非 bug。

---

## 给新加坡节点的执行建议

1. **拉取**：`git fetch kbpaamc && git checkout feature/weighted-ranking -- docs/upstream-issues-register.md`（或直接读分支）。
2. **流程**：每个候选项先在 upstream/main 建 issue（引用本文档证据），maintainer 认可后从 upstream/main 切修复分支提 PR；U-01 行为变更性质，PR 描述里必须带开关方案与评测数字（可直接引用 `:5173/report-weighted-ranking-v1-100.html` 的结论，但注意该报告在 fork 域名下，issue 里转述数字即可）。
3. **顺序建议**：U-03（零风险热身）→ U-10 aria（首 PR）→ U-02（小修带测试）→ U-01（旗舰）→ U-05/U-07 → 其余。
4. **不要回馈的**：fork 的请求级开关/flag 体系、kb-routing 本身（属二开特性非上游修复）；U-04 先定位后决定。
