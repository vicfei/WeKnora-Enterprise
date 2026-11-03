# 深度解析：智能推理模式的 Harness 设计

> 分支 `feature/agent-lab` 续作。前篇 agent-mode-deep-dive.md 回答"链路长什么样"，本文回答
> **"围绕模型的工程脚手架为什么这样设计"**——harness 指模型之外的全部执行框架：主循环、
> 上下文经济学、身份编解码、工具纪律、韧性矩阵、缓存策略、可观测性。全部结论基于源码
> 通读 + 2026-09-08 评估跑批的真实日志证据（§8 案例研究是一次完整的 harness 行为实录，
> 同时暴露了 P0 级问题 U-14）。

## 1. Harness 的分层与设计目标

```
┌─ 韧性层：瞬态重试 / 溢出恢复 / 空答 nudge / 卡环检测 / 降级合成 ──────┐
├─ 循环层：executeLoop 状态机（iterOutcome）+ 7 种终止条件 ────────────┤
├─ 上下文层：token 估算（双轨）→ Compactor（切点/摘要/文件账本）→ 钳制 ┤
├─ 身份层：modelcontext（cN/dN/bN/wN 句柄表 + 流保持 + 工具策略） ──────┤
├─ 工具层：注册漏斗 → JSON 修复/校验 → 超时矩阵 → 输出预算 ────────────┤
├─ 缓存层：PromptCacheKey=sessionID + 字节稳定工具表 + usage 基线 ─────┤
└─ 观测层：Langfuse 三层 span + ctx 预测/漂移对账 + pipeline 事件 ──────┘
```

设计目标可以归纳为五句话，后文每个子系统都能对应回来：

1. **回合无状态**（引擎不存跨回合缓存，历史每回合从 DB 重放）——可水平扩展、可崩溃恢复；
2. **每类失败有专属语义**（溢出≠瞬态≠截断≠空答，绝不混用重试策略）；
3. **一次性预算**（溢出恢复每回合一次、压缩"无料可压"记账、卡环 2 轮即断）——防循环烧钱；
4. **模型只见句柄不见身份**（UUID 在进上下文前全部换成 cN/dN/bN/wN）；
5. **一切决策先估后验**（ctx 预测 → 实际 usage → 漂移对账，误差进日志）。

## 2. 上下文经济学（最精巧的子系统）

### 2.1 双轨估算（agent/token + engine.go:215）

估算器只做两件事：API usage 缺席时的首轮全量估算（cl100k BPE + 每消息 3 token 开销 +
每工具调用 4 + 每图片固定 1200），以及 usage 之后的**增量估算**（基线=上轮 usage，只加
新消息）。工具 schema（~5k token）**只进请求账不进历史账**——注释里记录了教训：把 232
个工具 schema 加进历史估算后，12k 的对话每轮都"看起来"超 111k 阈值，压缩永不收敛。

实测精度（§8 之外的正常案例）：`predicted=12345 actual_prompt=12311 diff=+34 (+0.3%)`。

### 2.2 Compactor（internal/agent/compaction/，9 个文件全读）

**触发**：阈值 = 窗口 − 保留额（默认 16,384，但取 max(完成预算+4096, 16384)）。绝对额
而非比例，因为要装下的是"下一条回复"，它不随窗口缩放。

**切点算法（cutpoint.go）**——三条铁律，每条都有血泪注释：

1. **预算是天花板不是地板**：从最新往回累积，超出预算就停——绝不把超预算那条也带上。
   反例注释：单个 knowledge_search 结果常态 1 万 token，"保留最近 16k"若带上超支消息
   会交还 25k，一两轮后再次触发压缩；
2. **切点永远不落在 tool 结果前**（`isCutPointMessage: role != "tool"`）：tool 结果必须
   和请求它的 assistant 消息同生共死，切开即孤儿，provider 直接拒单；
3. **当前轮不豁免**（包注释开宗明义）：ReAct 一"轮"（turn）可跑 20 个 round、产出
   10 万 token 工具流量；豁免当前轮 = 永远无料可压 = 每轮白付一次摘要费。

**切点落在轮中间**（SplitTurn）时做两段摘要：历史段用全量格式，轮前缀段用专门的
"Original Request / Early Progress / Context for Suffix"小格式——保留的轮后缀仍能知道
自己从哪来。

**摘要协议（prompts.go）**：结构化七段（Goal/Constraints/Progress 分 Done-InProgress-
Blocked/Key Decisions/Next Steps/Critical Context），更新式压缩要求"完成的塌成一行、
废弃的整段删除、长度不得单调增长"。两个防御性设计值得抄：

- **半截摘要=失败**（validateSummary）：finish=length 的摘要静默缺尾巴，后续所有轮都会
  把这个残缺当唯一记忆——直接判无效走降级；
- **摘要预算与 keep_recent 联动封顶**：不封顶的话摘要在逐次更新中单调膨胀，把压缩刚
  腾出的空间又吃回去。

**文件账本（fileops.go）**：沙箱路径（读过/写过/改过）从被压缩掉的消息里**机械提取**
（不信任 LLM 摘要会保留路径清单——"列表是摘要器最先丢的东西"），以 `<read-files>`/
`<modified-files>` 标签附加在每份摘要尾部并逐次继承（上限 50 条）。没有它，压缩后模型
忘记自己写过 deck.html，从头重建大文件——正是"永不终止的循环"。

**止损三闸（observe.go:73 runCompaction）**：

1. `ErrNothingToCompact` → 记 `compactionExhaustedAt = len(messages)`（**按消息数记账
   而非布尔**：循环加了新轮后有了新料，旧判断自然失效）；
2. 释放量 < 压缩前的 1/minFreedFraction → 同样记账不再试（"释放了 240/26,700 也算
   进展"的判据会每轮白付摘要费）;
3. 压缩后清零 `lastUsage` 基线——旧基线描述的是压缩前的历史，留着会让下轮立刻再压。

**降级路径**：摘要调用 2 次失败 → 用原始对话归档（rawArchive）拼接进摘要位。上下文
仍然变小了，只是记忆变粗——`Degraded` 标记外发给 UI。

**摘要消息的形状**：`role=user`（不是 system——"system 是指令，这是对话史"，且部分
provider 合并 system 消息）+ `Kind=MessageKindCompactionSummary`（下次压缩凭 Kind 识别
自己的产物，**绝不把摘要喂给下一份摘要**——防"摘要的摘要的摘要"）。

### 2.3 溢出识别（overflow.go）——一本 provider 错误方言词典

25 个正则对应 19 家 provider 的上下文超限报错（Anthropic/OpenAI/Bedrock/Gemini/Grok/
Groq/OpenRouter/Together/Copilot/llama.cpp/LM Studio/MiniMax/Kimi/Mistral/DS4/z.ai/
Ollama/DashScope/泛化），外加 4 个**反例模式**（Bedrock 限流说"Too many tokens"但不是
超限！）。误判的代价注释写得很清楚：当普通失败→整轮报废；当瞬态错误→原样重试到死。
正确识别才有"压缩后重试一次"这个专属恢复。

还有第二形态：**成功响应也被窗口挤过**（ResponseHitContextLimit）——provider 静默截断
输入（z.ai）、截到零输出（MiMo）、或没到我们的 max_tokens 就 length 停（通用情形）。
三种形状都有判据，且强调必须传**未钳制的**完成预算进来比较。

## 3. 身份边界（modelcontext）

### 3.1 句柄表（handle_table.go）

一个泛型 `handleTable[M]` 撑起全部句柄空间（cN/dN/bN/wN、iN、res://NNNN、ingest 的
c000/ref-N）：去重键 → 顺序分配的句柄 → 持久值+元数据，单锁保护、**只增不删**（句柄
编号终身稳定）。正则按词边界编译一次缓存在条目里——DecodeKnownText 每个流块都要跑，
"每句柄每块重编译"曾主导开销。

键与值可以不同（web 引用按规范化 URL 去重、解码回原始 URL）——去重和解耦分离。

### 3.2 流保持（stream.go）——半标签永不漏出的保证

`streamHold` 是唯一的原语：Feed 扣住**可能还会长成句柄的尾部字节**，对释放部分做解码，
Flush 决定尾部归属。资源解码器逐句柄逐前缀试后缀；数字句柄解码器扫词边界字符；孤儿
过滤器兜底删除一切 `res://数字` 形状的残片（连 Flush 都区分"普通 r/re/res 散文保留、
跨过语法定界符的丢弃"）。

组合式 StreamDecoder 的 Flush 顺序是精髓：**前一段的尾巴必须先 Feed 进后段再让后段
Flush 自己的尾巴**——否则上游尾巴拼出的句柄会绕过下游解码。

### 3.3 工具策略表（tool_policy.go）

`sourceKeySpaces` 是**ID 承载字段的唯一登记表**（chunk_id/faq_id/knowledge_id/
knowledge_base_id/url… → 五个句柄空间），注册与解码共用一张表，"两处逻辑不可能漂移"
（有测试强制工具策略里的每个字段都在表中）。策略按**工具名+字段名**双键判定——注释
点名：动态 MCP 工具可能重名不同义，必须保持不透明。

### 3.4 引用协议（citations.go，前篇已述）

citation 开启时模型被指示内联 `<ref id="cN"/>`，系统展开为 `<kb/>/<web/>`，历史重放时
再压缩回句柄。协议提示词声明"优先级高于任何自定义提示词里的引用格式指令"。

## 4. 工具纪律与韧性矩阵（前篇已详述，此处补三条深读所得）

1. **first-wins 注册**（registry.go:55）：同名工具第二个注册直接拒绝并告警——防名字
   劫持（引用 GHSA 编号），代价是注册顺序即优先级；
2. **截断即拒绝**：finish=length 的整批工具调用拒执行 + JSON 修复后仍判定"修出来的
   是截断串"也拒执行——"半写的文件还会报成功，模型只能读回来才发现"；
3. **输出预算协商**：registry 把 16KB rune 上限**发布给工具**（outputLimitProvider），
   预算感知的工具自己分页裁剪，无感知的才被暴力截断——两层设计。

## 5. 缓存经济学

三个互相咬合的决定：

1. `PromptCacheKey = sessionID`——同会话同前缀，provider 命中增量缓存；
2. **工具定义按名排序**（GetFunctionDefinitions）——map 遍历随机化会把 tools 块的字节
   序打乱，前缀缓存（Qwen 显式缓存等）按字节匹配直接失效；
3. usage 基线 = provider 全量输入计数（含 cache_read），漂移对账把"预测 vs 实际"的误差
   落日志，压缩后清零基线防连环误压。

实测命中率（本日正常跑批）：cache_hit_rate 77.4%–96.7%，cache_status=hit 稳定。

## 6. 可观测性对账

- `[ctx] predicted=… | messages=… tool_schemas=… request=… | threshold=… window=…
  keep_recent=…` + breakdown（text/reasoning/tool_results/tool_args/tool_schemas/
  largest）——**largest 字段是本次发现 U-14 的钥匙**；
- `[ctx] drift: predicted vs actual (+%)`——估算器的持续回归测试；
- Langfuse：trace → agent.execute → agent.round.N → agent.tool.* 四层，工具 span 同时
  记录 model_arguments（含句柄）与 resolved_arguments（实际执行值）——观测层不实现
  映射、只见证映射，边界不破。

## 7. 设计原则提炼（可迁移）

1. **失败分类学先于重试策略**：每类错误配专属恢复（词典+反例），没有"通用重试"；
2. **一次性预算 + 记账式熔断**：恢复动作带 exhausted 标记，且标记锚定在会变化的量
   （消息数）上而非布尔；
3. **机械提取优于 LLM 转述**：关键账目（文件路径）不信摘要，信代码提取；
4. **协议标签自带身份**（MessageKindCompactionSummary）——自己的产物自己认得，防自噬；
5. **估算只为触发时机服务**：允许 ±小误差，下轮 usage 会校准——精度换的是零额外调用；
6. **看门狗管静默不管时长**：合法长产出与死流的区分标准是"还在出字吗"。

## 8. 案例研究：一次完整的 harness 行为实录（兼 P0 发现 U-14）

2026-09-08 02:42–03:07，kb=all 智能体（541 库租户）不开路由的评估臂。日志逐帧还原：

| 帧 | 日志证据 | harness 行为 |
|---|---|---|
| 1 | `context_msgs=0, query_len=201` 但 `messages=222213, largest=user(219319)` | 首轮用户消息被 runtime_context 的 541 库目录撑到 219k token（格式见 prompts.go:129，无总量上限）——**超出 200k 窗口本身** |
| 2 | `Context at 222216 tokens, over the 183616 threshold … compacting` | 估算器正确报警 |
| 3 | `Nothing outside the keep-recent budget; skipping compaction` | 压缩器正确拒绝（§2.2 铁律 3 的推论：重量就在当前轮，无料可压）——记账熔断生效 |
| 4 | `LLM responded: finish=length, content=0 chars` + `Usage: prompt=175640, completion=1` | provider 静默截断输入（deepseek 分词后 175k），零输出、length 停——§2.3 第二形态的真实样本 |
| 5 | 15 轮重复帧 4（每轮 +1~5 token 的空转） | 空答 nudge 循环 + 迭代预算（15）耗尽 |
| 6 | `Reached max iterations → Synthesizing from 0 tool calls` | 降级合成兜底，turn 结束 |
| 7 | 40 题 × ~175k × 15 轮 ≈ **1 亿 token**，26 分钟烧穿账户（03:08 402） | —— |

三个结论：

1. **harness 每一层都按设计工作了**（报警→拒绝→循环→熔断→兜底），没有一层能救——因为
   毒药在最上游（用户消息本身），这是"上游数据缺陷不可被下游韧性消化"的教科书案例；
2. **真正的缺口是两个**：目录渲染无预算（U-14 根因）+ 引擎没有"首轮超窗即拒"守卫
   （15 轮全价空转是纯浪费）；
3. 同批开启 kb-routing 的问题完全正常（收窄 3 库后用户消息 ~1.9k）——**路由在这个
   事故里恰好是解药**，这为"全库智能体默认配路由"提供了额外论据。

## 9. 改进清单（按优先级）

1. **U-14 修复**（P0）：目录渲染预算化（>N 库改摘要形态）+ 首轮超窗守卫（估算超窗口
   80% 即终止并报清晰错误）；
2. 首轮守卫的报错应带"哪些内容最肥"（breakdown 已有，只差透出）；
3. 压缩触发阈值当前只看历史账（§2.1）——工具 schema 计入请求账但不触发压缩，边界
   值场景（schema+历史恰好在窗口边缘）可能出现"压缩后仍超"的第二轮溢出恢复，可观测
   但罕见；
4. `ResponseHitContextLimit` 的第三判据（completion < budget）在 provider 低配额输出时
   可能误报——有反例模式保护限流，但输出配额单独限的 provider 仍可能误入恢复路径。

## 10. 与前篇的关系

- 前篇（agent-mode-deep-dive.md）：链路全景、工具体系、终止条件、评估接缝——**是什么**；
- 本篇：压缩/身份/缓存三大深水区的算法与协议、设计原则、事故案例——**为什么**；
- U-14 登记于 docs/upstream-issues-register.md；评估数字与复现命令见 evals/README.md v4 节。
