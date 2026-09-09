# 深度解析：智能推理模式（Agentic RAG / ReAct）架构

> 分支 `feature/agent-lab`（基线 feature/weighted-ranking @ be077802）。本文覆盖：双通道
> 分叉全景 → agent 链路逐层解剖（入口/引擎/工具/上下文/降级）→ 与 chat 管线的对照表 →
> 实测记录 → 已知问题与优化接缝。**所有结论均经实测**（curl /agent-chat 全链路 + psql +
> 日志，证据见 §8），非纯代码推断。
> **续篇**：harness 三大深水区（上下文压缩算法、modelcontext 身份编解码、缓存经济学）
> 与设计原则的逐文件解剖见 docs/design/agent-harness-design.md（含 P0 发现 U-14 的事故
> 案例研究）。

## 1. 摘要

智能推理模式是 WeKnora 的 Agentic RAG 通道：不走 chat_pipeline 插件链，而是由
`internal/agent/` 的 ReAct 引擎驱动——LLM 在函数调用循环里自主决策"检索什么、检索几次、
何时停"，工具集覆盖知识检索/沙箱/技能/MCP/Wiki 全家桶。核心事实：

1. **一条请求两种人格**：`/api/v1/agent-chat` 与 `/knowledge-chat` 共用 handler 骨架
   （parseQARequest → executeQA），分叉点只在 `qaModeAgent`；**模式切换=选智能体**
   （custom_agent.agent_mode 字段决定走哪条链）。
2. **引擎每回合无状态**：历史每回合从 DB 重载（LoadAgentHistory），AgentSteps 回放成
   OpenAI 格式的 assistant_with_tool_calls + tool 消息；引擎自己不缓存任何跨回合状态。
3. **终止条件完备**（7 种）：自然停（无工具调用）/ content_filter / 空内容重试耗尽 /
   同内容重复卡环 / 迭代预算耗尽（合成兜底答案）/ 用户取消（抢救已有工具结果）/ 瞬态
   错误耗尽（降级合成）。
4. **三处降级合成**：LLM 挂了但有工具结果 → 从工具结果合成；上下文溢出 → 压缩后重试
   一次；迭代耗尽 → 强制合成。保证"有工具结果就不空手而归"。
5. **引用机制与 chat 完全不同**：agent 模式没有 references SSE 事件，引用是模型内联
   `<ref id="cN"/>` 句柄被系统展开成 `<kb doc=... chunk_id=.../>` 标签嵌在答案文本里，
   前端渲染；`messages.knowledge_references` 列在 agent 回合**恒为空**（state.KnowledgeRefs
   是无人填充的死字段）。
6. **排序能力分叉**（本任务的优化素材）：agent 的 knowledge_search 自带一份
   compositeScore(0.6/0.3/0.1)+MMR(λ=0.7) 拷贝（U-03），未接 RankingConfig；agent 级
   检索策略字段（EmbeddingTopK 等 5 个）只喂 chat 管线，agent 工具读的是全局 config。
7. **实测新发现一个错误路径 bug**（登记 U-13）：AgentQA 启动期失败（如 rerank 未配置）
   时错误事件已发但 SSE 流不收尾，客户端挂到超时。

## 2. 双通道分叉全景

```
                      POST /api/v1/{knowledge|agent}-chat/:session_id
                                   │
                     handler/session/qa.go: parseQARequest
                     （同一 DTO：query/knowledge_base_ids/agent_id/...）
                                   │
                 ┌──── customAgent.IsAgentMode()? ────┐
                 │ false                              │ true
                 ▼                                    ▼
        executeQA(qaModeNormal)              executeQA(qaModeAgent)
                 │                                    │
   session_knowledge_qa.go              session_agent_qa.go AgentQA()
   播种 PipelineRequest                 解析租户/模型/rerank/历史 → 引擎
   （全局 ConversationConfig            │
     + agent 覆盖 session_qa_helpers）   │ CreateAgentEngine
                 │                                    │
        chat_pipeline 插件链              internal/agent 引擎（每请求新建）
   search → rerank → merge               ReAct 循环 think→analyze→act→observe
   → filter → into_msg → completion                │
                 │                        工具执行 internal/agent/tools/*
                 ▼                                    │
   references SSE 事件（结构化引用）       <kb/> 标签内嵌答案文本（内联引用）
```

**关键差异一览**（对照实测）：

| 维度 | 快速问答（knowledge-chat） | 智能推理（agent-chat） |
|---|---|---|
| 执行框架 | chat_pipeline 中间件链，一次检索一次生成 | ReAct 循环，模型自主决定检索次数与策略 |
| 检索触发 | 管线自动（每问必检） | 模型调 knowledge_search/grep_chunks 才检 |
| rerank 接入 | 官方死代码（U-01），fork 用请求级 rerank_model_id 绕过 | **原生接通**：agent 配置 RerankModelID，缺失直接报错 |
| 检索参数来源 | 全局 config → 租户 → agent 覆盖（applyAgentOverrides） | knowledge_search 工具只读**全局 config**（agent 级 5 字段无效）|
| 引用呈现 | references SSE 事件 → knowledge_references 列 | `<ref id="cN"/>` → `<kb/>` 标签内嵌答案 |
| 多轮历史 | 管线 messages | DB 每回合重载 + AgentSteps 回放 + KB 工具结果默认 redact |
| 排序公式 | rerank.go:439（已接 RankingConfig） | knowledge_search.go:1209 拷贝（未接） |
| 工具集 | 无 | 20+ 种（KB/沙箱/技能/MCP/Wiki/web） |
| 上下文管理 | 管线级 TOP_K 过滤 | token 估算 + Compactor 摘要压缩 + 溢出恢复 |

## 3. 入口与装配（handler → service）

### 3.1 请求入口（qa.go:849 AgentQA）

- 请求体与 knowledge-chat 同构，关键字段 `agent_id`；handler 解析 CustomAgent
  （resolveAgent，共享智能体走 share 关系解析），`agent_enabled` 仅在无 agent_id 时有意义；
- **前置守卫**：agent_mode=smart-reasoning 但 agent_id 缺失/不可解析 → 400
  "agent_id is required"（防 localStorage 脏状态）；
- executeQA 异步 goroutine 调 `sessionService.AgentQA`（qa.go:1061），SSE 事件由
  AgentStreamHandler 消费 eventBus 转写。

### 3.2 AgentQA 装配序列（session_agent_qa.go:20，实测日志逐步对应）

1. **租户解析** resolveRetrievalTenantID（共享智能体用 agent 属主租户的 KB/模型域）；
2. `EnsureDefaults()`（types/custom_agent.go:470）：MaxIterations=10、HistoryTurns=5、
   RetrievalStrategy 四参数（EmbeddingTopK=10 等）、smart-reasoning 强制 MultiTurnEnabled；
3. **buildAgentConfig**（:283）：CustomAgentConfig → 运行时 AgentConfig；AllowedTools
   缺省 DefaultAllowedTools()；@skill/@MCP per-turn 收窄（只窄 MCP，不窄 skill——skill
   仅置顶 must_use 不吊销）；buildSearchTargets 把 KB/tag 范围解析成带租户的检索目标；
4. **chat 模型解析**：resolveChatModelID，为空直接报错"summary model is not configured"
   （agent 模式模型是硬前置，不像 chat 有默认回落链）；
5. **上下文窗口**：模型 Parameters.ContextWindow → AgentMaxContextTokens（实测
   deepseek-v4-flash 声明 200000）；
6. **rerank 解析**（:116，仅当 agentRequiresRerankModel）：kb_selection_mode≠none 且
   allowed_tools 含 knowledge_search（空列表回落 DefaultAllowedTools 也算含）→ 必须配
   RerankModelID，**无租户级回落**（注释明说：强制配置在用户实际编辑的地方）；
7. **历史装载**：MultiTurnEnabled 时 LoadAgentHistory(sessionID, HistoryTurns)；
8. 沙箱回合锁 + 会话附件 staging（有沙箱能力时）；
9. CreateAgentEngine + 长期记忆 recall（memoryService.Recall 注入系统提示尾部——必须在
   system prompt 里，因为 buildMessagesWithLLMContext 会丢弃历史里的 system 消息）；
10. `engine.Execute(ctx, sessionID, messageID, agentQuery, llmContext, imageURLs)`。

### 3.3 查询增强（Execute 前）

vision 模型直接传图；非 vision 附加图片描述文本；引用上下文（quoted）、附件内容
（BuildPrompt）、沙箱 staged 附件清单依次拼进 agentQuery。**scope envelopes
（runtime_context/must_use）只进引擎内每次 LLM 调用、不落 user 消息**——多轮历史不被
过期 @mention 污染。

## 4. ReAct 引擎（internal/agent/engine.go）

### 4.1 主循环与终止条件（executeLoop :428）

```
for withinIterationBudget(round):          # MaxIterations；负值=无限
    ctx.Done()? → 有工具结果则抢救合成 → return
    runReActIteration:                     # 一轮 = think→analyze→act→observe
        think:  callLLMWithRetry           # 流式，含消毒/重试/降级
        analyze: analyzeResponse           # 判定终局
        act:    executeToolCalls           # 串行或 errgroup 并行
        observe: appendToolResults         # OpenAI tool 格式回写 messages
循环退出仍未完成 → handleMaxIterations → 合成兜底答案
defer emitCompletion():                    # 恰好一次 EventAgentComplete（WithoutCancel）
```

七种终止条件（const.go + observe.go:350 实测核对）：

| # | 条件 | 行为 |
|---|---|---|
| 1 | 自然停：isNaturalStopFinishReason 且无 tool_calls | 已流式内容收 Done；空内容→nudge 重试≤2 次 |
| 2 | content_filter | 直接终局（防同响应累积死循环） |
| 3 | 空内容重试耗尽（2 次） | 固定 fallback 文案 |
| 4 | 同内容连续重复 2 轮（maxRepeatedResponseRounds） | 视为卡环，内容当答案终局 |
| 5 | 迭代预算耗尽 | handleMaxIterations 从工具结果合成答案 |
| 6 | ctx 取消（用户停止） | 有工具结果则抢救合成；不加兜底文案（防"停止却出答案"） |
| 7 | LLM 瞬态错误重试耗尽（2 次） | 有工具结果→降级合成；否则整轮失败 |

### 4.2 Think：流式调用与防御（think.go）

- **流式看门狗**（watchStreamStall）：120s 无输出（LLMCallTimeout 可调）即取消——是
  **静默预算不是总时长预算**（合法的大工具参数流几分钟不能杀，注释有真实事故）；
- 流错误（StreamError）**必当 Go 错误返回**：半截 preamble 不能冒充最终答案（注释：
  曾让 238 字符 preamble 顶替被丢的 30KB write_sandbox_file）；
- 溢出恢复：provider 拒绝 too-large → forceCompaction 压缩后重试一次（每回合仅一次，
  overflowRecovered 防循环）；
- 瞬态错误（429/5xx/timeout/stalled…标记串匹配）指数退避重试 2 次；
- `<think>…</think>` 内联思考与 reasoning_content 双通道分流到 thought 事件；answer
  文本乐观流式输出（若该轮最终带工具调用则由后续 tool_call 事件自然"撤回"为前言）；
- **ChatOptions 恒 parallel_tool_calls=true**（告知模型可发并行调用），但执行侧受
  AgentConfig.ParallelToolCalls（默认 false）控制——默认"可并行请求、串行执行"。

### 4.3 Act：工具执行（act.go）

- finish_reason=length（补全 token 上限截断）→ **整批拒执行**（截断的参数执行比失败
  更糟：半写的文件还报成功），每个调用回结构化错误+引导重发；
- JSON 参数解析失败 → RepairJSONDetail 修复；修复后仍判定"截断"（未闭合串/括号）→
  同样拒执行；
- 执行超时：通用 60s，shell_exec 10m05s（让工具自己回报结构化超时）；
- 并行模式 errgroup 收集、按原序回放（兄弟失败不传染）；
- 事件流：tool_call(pending/progress/hint) → tool_result → tool-exec（AgentActionData）；
- modelcontext 句柄（cN/dN/bN/wN）在执行前解码，未解析句柄直接拒绝（防幻觉句柄进入
  持久层/外部服务）。

### 4.4 Observe：结果回写与历史（observe.go:741）

- 每步 assistant(含 tool_calls) + 每调用一条 role=tool 消息（OpenAI 规范）；
- 工具结果经 modelContext.ModelToolResultForTool 编码（chunk ID→cN 句柄）；
- **跨回合**：AgentSteps 落 messages.agent_steps，下回合 LoadAgentHistory 回放；
  KB 类工具历史结果默认 redact 成 103 字符标记（"知识库可能已变，请重新检索"），
  RetainRetrievalHistory=true 才保留。

### 4.5 上下文管理（compaction/ + observe.go:39）

- token 估算双轨：上轮 API usage 基线 + 增量 BPE 估算（estimateCurrentTokens）——
  工具 schema（约 5k token）只计入首轨，避免"每轮都看似超限"；
- 实测预测行：`predicted=3969 | tool_schemas=5031 | threshold=183616 window=200000
  keep_recent=20000`；
- 超阈值 → Compactor 摘要旧历史（LLM 调用），保留最近 keep_recent token 原文；
- 预留 = 完成预算(4096/24576) + 4096 安全余量；完成预算钳制到窗口剩余；
- 工具输出 rune 截断 16KB（DefaultMaxToolOutput）；prompt cache key=sessionID；
  工具定义按名排序保证字节稳定（Qwen 前缀缓存命中前提）。

## 5. 工具体系（tools/）

### 5.1 注册漏斗（agent_service.go:816 registerTools，策略明确写在注释里）

```
AllowedTools（用户可编辑白名单，预设填充；空=DefaultAllowedTools）
  → SharedAgentReadOnly 剥写类 Wiki 工具
  → 无 KB 范围：剥全部 KB 类工具（+无 web 时剥 todo_write）="纯智能体模式"
  → WebSearchEnabled：追加 web_search/web_fetch（不在白名单制内）
  → 记忆开关（workspace/user/agent 三方同意）：追加 search_memory
  → 无 wiki KB：剥 wiki 工具；无向量/关键词 KB：剥 RAG 工具集（防陈旧配置调用必错工具）
  → 去重 → registry.RegisterTool（首注优先，防同名劫持 GHSA-67q9-58vj-32qx）
```

- 沙箱文件工具/shell/skill 工具**不在白名单制**：由沙箱能力/技能开关单独注册
  （definitions.go 注释：勾选框不该对能力撒谎）；
- MCP 工具 registerMCPTools 按 MCPSelectionMode 注入；
- 每工具 JSON Schema 参数校验 + 类型矫正（"true"→true）前置，省一轮浪费的执行；
- GetFunctionDefinitions 按名排序（字节稳定，见 4.5）。

### 5.2 knowledge_search 工具（检索主力，1395 行）

```
Execute: queries(模型可发多查询) → 并发检索（按 embedding 模型分组、
同模型目标合并单次调用）→ 去重 → rerank（agent 的 RerankModelID；
失败回退原序）→ compositeScore(0.6/0.3/0.1)×位置先验 → MMR(λ=0.7, k=topK)
→ 终去重 → 按分排序 → XML 格式输出（<search_results><chunk score=...>）
```

- 检索参数（topK/thresholds）读**全局 config.Conversation**（dev.env），agent 级
  EmbeddingTopK/KeywordThreshold/VectorThreshold/RerankTopK/RerankThreshold 五字段
  **在此路径不消费**（只进 chat 管线 applyAgentOverrides）——配置面与执行面脱节；
- rerank 失败仅告警回退原序（韧性 OK，观测弱）；
- 结果同时给模型（XML）和前端（Data.results，含 knowledge_title/rank）。

### 5.3 引用与句柄（modelcontext/）

- 检索结果进上下文时 chunk→cN 句柄；citation_enabled 时协议提示词要求模型内联
  `<ref id="cN"/>`；系统展开成 `<kb doc=... chunk_id=... kb_id=.../>`；
- 流式展开有防半标签机制（streamHold 后缀保持）；
- **实测注意**：`messages.knowledge_references` 恒空（state.KnowledgeRefs 无人填充，
  finalize 只是转发空切片）——评估取引用要么解析答案 `<kb>` 标签，要么读
  agent_steps 里的工具结果（后者保真且有序）。

## 6. 前端与配置面

- **模式切换=选智能体**（Input-field.vue:2168 selectAgentMode）：内置两 agent
  （builtin-quick-answer / builtin-smart-reasoning）由 builtin_agents.yaml 按租户实例化；
- **就绪守卫**（agent-readiness.ts）：model_id 必须命中租户 KnowledgeQA 模型列表 +
  （agent 模式且 KB 工具在列时）rerank_model_id 命中 Rerank 模型——内置智能推理默认
  无模型配置，**未配置时切换被拦**（弹"未就绪"引导去编辑器）；
- 租户 10011 实测：只有 builtin-quick-answer 被配置过（model_id 已填），无
  smart-reasoning 实例 → 前端智能推理切换必然被拦（上一棒 T5 置灰的根因）；
- 智能体 CRUD：POST/PUT /api/v1/agents（CustomAgentConfig 全量）；AgentType 预设
  （rag-qa/wiki-qa/hybrid-rag-wiki/data-analysis/custom）只是建_agent 时的预填包。

## 7. 观测与评估接缝

- **Langfuse 三层 span**：agent.execute → agent.round.N → agent.tool.<name>（含模型
  参数句柄审计：model_arguments vs resolved_arguments）；
- **管线事件**：execute_start/tools_ready/round_start/round_end/think_*/tool_call_*/
  final_answer_*/max_iterations_reached（ELK 侧可全程追踪）；
- **SSE 事件面**（实测序列）：agent_query → thinking* → (tool_call → tool_result)* →
  answer* → complete（含 usage/cache 统计）；tool_result 的明细被
  sanitizeToolDataForClient 剥掉（只留计数）；
- **评估数据面**：排序后的完整检索结果只在 DB `messages.agent_steps`（step.tool_calls
  [].result.data.results，含 knowledge_title/result_index）；EM/F1 用 answer 文本。

## 8. 实测记录（2026-09-08，租户 10011）

环境：8080=/tmp/weknora-server-wr2；智能体 hotpot-rag-agent（dd2d3093，rag-qa，
model=deepseek-v4-flash，rerank=qwen3-rerank，KB=hotpot-pool-500，max_iterations=15，
web_search=false，temperature=0.3）。

| # | 场景 | 结果 | 关键证据 |
|---|---|---|---|
| L1 | 语料外问题（Arthur's Magazine） | 58.3s / 8 步 / 28 tool_call 事件，诚实回答"知识库无此文" | 多轮 grep+语义检索后正确拒答 |
| L2 | eval-100 hp-0000（金标 1755） | 3.0s / 2 步，**答案正确**；检索命中金标文档（Tokarev/Moscow State University） | 单轮并行请求 grep_chunks+knowledge_search（串行执行） |
| L3 | 多轮追问（"那位教授的研究领域？"） | 正确解析指代 → Ethnography | T2 装载 10 条历史；KB 工具结果已 redact（len=103 标记） |
| L4 | 纯智能体（kb=none，无 rerank） | 0.7s 答对（17×23=391），rerank 不被要求 | agentRequiresRerankModel 判定链生效 |
| L5 | KB+knowledge_search 但 rerank_model_id 空 | 报错"rerank model is not configured"（qa.go:1072），**但 SSE 流不收尾**，客户端挂 60s 超时 | 新 bug，登记 U-13 |
| L6 | 引用呈现 | agent 回合 knowledge_references 恒空；引用走 `<kb/>` 标签 | psql + 事件流双验证 |
| L7 | UI 全链路（无头 Chromium） | 模式下拉列出全部智能体（含自定义 hotpot-rag-agent）→ 选中 → 提问 → 步骤树"Ran 8 tools across 8 rounds"（知识搜索/关键词搜索/查看文档分块）→ 答案正常渲染，无 page error | 截图 evals/reports/assets-agent-lab/ui_*.png |

## 9. 已知问题与本任务优化接缝

1. **U-13（新）agent 启动期失败 SSE 不收尾**：AgentQA 返回错误时 handleError 只
   AppendEvent(Done:true) 不终结流；executeLoop 的 defer emitCompletion 只覆盖引擎
   启动后的路径。复现：配 KB+knowledge_search 不配 rerank，POST /agent-chat 挂死。
   （与 U-04"偶发不收尾"可能同族——U-04 疑似流式 writer 异常路径漏发 complete。）
2. **U-03 排序公式双拷贝**：knowledge_search.go:1209 未接 RankingConfig；接法见
   §5.2——把 chat 侧 searchutil 共享函数替换进来即消分叉。
3. **agent 级检索策略字段失效**：CustomAgentConfig.EmbeddingTopK 等五字段编辑器可配、
   UI 可存，但 agent 路径 knowledge_search 只读全局 config——配置幻觉（上游问题候选，
   建议并入 U-03 修法一起回馈）。
4. **state.KnowledgeRefs 死字段**：agent 模式恒空，`AgentCompleteData.KnowledgeRefs`
   与 IM 侧消费链路形同虚设；要么删要么在工具执行处填充。
5. **标尺缺口**：eval harness 只打 knowledge-chat；agent 模式基线缺失（本任务 §下一步：
   ask 走 /agent-chat + agent_id，引用命中从 agent_steps 补算）。
6. kb-routing 候选=agent 允许库范围（原待办 4）：agent 的 SearchTargets 就是现成的
   路由候选集，接入点在 buildAgentConfig 之后、CreateAgentEngine 之前。

## 10. 创建智能体实操配方（已验证）

API 路径（评估租户模板）：

```jsonc
POST /api/v1/agents
{
  "name": "hotpot-rag-agent",
  "config": {
    "agent_mode": "smart-reasoning", "agent_type": "rag-qa",
    "model_id": "<KnowledgeQA 模型>", "rerank_model_id": "<Rerank 模型>",
    "temperature": 0.3, "max_iterations": 15,
    "kb_selection_mode": "selected", "knowledge_bases": ["<pool-kb>"],
    "web_search_enabled": false,          // 评估必须关，防外泄
    "allowed_tools": ["thinking", "todo_write", "knowledge_search",
                      "grep_chunks", "list_knowledge_chunks",
                      "get_document_info", "search_conversations"],
    "multi_turn_enabled": true, "history_turns": 5
  }
}
// 然后单问验证：POST /api/v1/agent-chat/:sid  body={"query","agent_id",disable_title:true}
```

UI 路径：平台管理 → 智能体 → 新建（类型选 rag-qa）→ 模型/排序模型/知识库范围三项
配齐（缺一即被就绪守卫拦截）→ 会话页切换该智能体提问。

经验值：单题延迟 3s（命中快路径）~60s（语料外多轮排查）；token 消耗约为 chat 模式
5-7 倍（多轮思考+工具结果）；迭代上限 15 对 rag-qa 足够且控成本。

## 11. 优化实验（本分支 feature/agent-lab 实测，eval-100 池模式，协议同 chat 侧）

### 11.1 agent 模式基线（首条标尺）

智能体 hotpot-rag-agent（rerank=qwen3-rerank 原生生效），r1：

| 指标 | agent 基线 | chat 无 rerank | chat+rerank |
|---|---|---|---|
| EM | **53.0** | 48.6 | 53.1 |
| F1 | **75.49** | 65.7 | ~78.6 |
| 金标 top3/5/10 | 78.0/82.0/84.0 | 69/77/84 | 83.2/90.3/— |
| MRR | 86.67 | — | — |

注：agent 的 ref_titles=回合内全部 knowledge_search 结果的有序并集（首轮结果主导），
与 chat 的"最终引用列表"语义不同——跨模式对比是方向性的，模式内 A/B 是精确的。
EM 与 chat+rerank 打平（+4.4 于无 rerank），说明 agent 的检索质量已吃到 rerank 红利。

### 11.2 U-03 重构 + RankingConfig 接入（on/off 配对）

- 实现：searchutil 共享内核（CompositeScore/RecencySignal/SelectMMR，含增量=朴素
  等价的 200 次随机对照单测）；chat rerank.go 与 agent knowledge_search.go 双侧替换；
  AgentConfig.Ranking 运行时字段，解析链=请求级>智能体租户>旧常量（同 chat）；
  零值守卫（结构体字面量构造不炸）。
- **arm2（weighted-defaults 开，权重全默认）vs r1（旧常量路径）**：EM +2.0 / F1
  +0.56 / top3 -0.5 / top5 +1.0 / top10 +2.5——全部在噪声带内，无一致方向。
  公式在默认下数学等价 + 行为学等价双确认：**重构零回归**。
- arm3（mmr_lambda=1.0，纯相关度无多样性惩罚）：**与 chat 侧结论相反**——
  金标 top3 78.0→82.0（+4.0）、top5 82.0→86.5（+4.5）、top10 84.0→90.5（+6.5）、
  全命中 80%→86%（+6.0）、MRR +5.0，全部超噪声带且方向一致；EM/F1 持平（噪声）。
  解读：chat 是"一次性列表消费"，MMR 多样性防止近重复挤占 RerankTopK 名额
  （chat 侧 λ=1.0 回吐 4-6 分）；agent 是"可再检索的消费者"（可多轮补检索），
  更受益于每轮列表的纯相关度排序。**chat 的最优 λ 不迁移到 agent。**
  单轮 r1，正式结论需 r2 复验。

### 11.3 kb-routing 接入 agent（原待办 4）

- 实现：buildAgentConfig 在 resolveKnowledgeBases 之后，`enable_kb_routing` 且智能体
  KBSelectionMode=all 且本请求未显式选库/@提及时，复用 chat 的
  routeKnowledgeBases（top-K/阈值同环境变量）收窄，路由结果与能力过滤集求交，
  决策经 kb_router 事件外发（与 chat 同形状，eval harness 直接复用）。
- 显式选择永远优先；mode=selected 的智能体（如基线 agent）不受影响。
- 实测：手测 541→3 库（选中问题自有小库 score 0.60）、答案正确；routing-on 单臂
  （agent=hotpot-rag-agent-all）EM/F1 与基线持平、检索指标略低于池基线
  （MRR 81.1 vs 86.7，top3 75.5 vs 78.0，零命中 14% vs 11%）——3 库路由相对单一
  pool 库有小幅漏检代价。off 配对臂因 DeepSeek 402 断费未完成（数据无效已删），
  充值后补跑再做结论。
