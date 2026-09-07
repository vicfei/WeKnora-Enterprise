# WeKnora 评估工具链（HotpotQA 基线）

目的：为二开特性（知识库路由 / 领域化改写 / 排序加权 / 版本管理）提供
**可复现的准确率基线**，特性 on/off 配对对比，支撑"准确率上不去就撤销"的决策。

## 组成（evals/hotpot/）

| 脚本 | 作用 |
|---|---|
| `common.py` | API 客户端（bridge 认证 / KB / 文档 / SSE）、配置 |
| `prepare.py` | 下载 HotpotQA dev(distractor) → 固定种子抽样 → 切题清单 |
| `ingest.py` | 每题一库：建 KB + 10 段落 manual 灌入 + 等索引完成 |
| `run_eval.py` | SSE 问答跑批（限速并发）→ 原始结果 JSONL 落盘 |
| `score.py` | EM / F1 / 支撑段落命中率 + 基线对比报告 |

数据集与结果产物在 `evals/data/`（gitignore，不入库）。

## 评估协议（防噪声误杀）

- 固定样本（seed=42 抽样）、固定 KB 内容（每题一库，10 段落=2 金标+8 干扰）
- 特性对比必须**配对**：同一样本集、同配置，仅特性开关不同
- 每配置跑 ≥2 轮，答案分取均值；差异 < 2 个点视为噪声区间，不作为撤销依据
- 检索命中与答案准确分开计分：前者归因检索/路由层，后者叠加生成层

## 计分口径

- **EM / F1**：HotpotQA 官方归一化（小写、去冠词/标点、token F1）
- **支撑命中（support hit）**：引用文档 ∩ 金标段落（2 金标题，命中数 0/1/2）
  ——引用来源取 SSE `references` 事件

## 运行环境

- 上海节点 dev 栈（:8080），评估租户 10011（review 租户，模型：
  embedding=text-embedding-v4、QA=deepseek-v4-flash、rerank=qwen3-rerank）
- 认证：platform key bridge 出 admin 冒烟用户（`hotpot-eval-runner`），
  测完可 `POST /identity/disable` 收尾
- 内存纪律：串行灌库、问答并发 ≤2（3.6G 机器血泪教训）

## 快速开始

```bash
cd evals/hotpot
python3 prepare.py --limit 20          # 试点 20 题（基线建议 500）
python3 ingest.py --manifest ../data/eval-20.jsonl
python3 run_eval.py --manifest ../data/eval-20.jsonl --out ../data/runs/pilot.jsonl
python3 score.py --run ../data/runs/pilot.jsonl
```

## 成本与端点教训（2026-09-06）

- **跑批前先确认模型端点余额**：单轮 500 题问答 + 4955 篇池文档的摘要生成可耗尽
  按量计费账户（实测：DeepSeek API 402 Insufficient Balance）。
- manual 灌入的摘要生成是隐性大头（每篇一次 LLM 调用）；评估场景应通过
  `process_config` 跳过摘要（待实现），或接受摘要失败——**摘要失败不影响
  向量/关键词检索**（chunks 与 embeddings 在 processing 阶段完成）。
- 余额耗尽时后端摘要队列会对全部失败文档反复重试（半小时 ~4 万次 402），
  重试耗尽后自动停；恢复余额后队列可续。
- 评估租户 10011 的 QA=DeepSeek、Embedding/Rerank=阿里云 dashscope，独立计费。

## 标准测评集（2026-09-06 固化）

一次抽样（seed=42）、三层嵌套切片，题目永不改动，保证任何两轮可比：

| 集合 | 题数 | 用途 | 预计耗时（问答阶段） |
|---|---|---|---|
| `eval-100.jsonl` | 100 | **特性迭代默认集**（on/off 配对） | 小库 ~17min / 池 ~30min |
| `eval-200.jsonl` | 200 | 阶段验收 | 小库 ~35min / 池 ~60min |
| `eval-500.jsonl` | 500 | 里程碑基线 | 小库 ~85min / 池 ~145min |

成本要点：
- **灌库是一次性成本**：小库 500 个 + 共享池 1 个（4955 篇）已建好，后续每轮只花问答
  token（池模式 ~17s/题·上下文更长；小库 ~10s/题）。断点续跑免费。
- 特性迭代建议流程：eval-100 池模式 on/off 配对 → 差异显著再上 eval-500 复验。
  池模式下检索层指标（金标命中/top-k）有真实区分度（小库已饱和：宽度 9.4/10）。

## v2 基线参考值（384 题配对，2026-09-06）

| 指标 | 小库模式 | 共享池模式 |
|---|---|---|
| EM / F1 | 51.0 / 68.9 | 49.5 / 66.6 |
| 金标全命中 / 部分 | 94.0% / 0% | 84.4% / 11.5% |
| 金标进 top3 / top5 / top10 | 75.4 / 85.1 / 94.5 | 68.8 / 76.0 / 84.2 |
| MRR | 87.9 | 88.1 |
| 引用宽度 | 9.4/10（饱和） | 28.6（有梯度） |

语料从 10 篇/题 增至 4955 篇共享池（检索难度 ×500），答案端仅降 ~2 分——
多跳生成相当稳；检索端出现真实区分度，是特性评估的主战场。
（注：v2 及此前所有池模式跑批的 chat 链路实际**未经过 rerank 阶段**，见 v3 发现。）

## v3 排序加权三臂（eval-100 池模式，2026-09-07，分支 feature/weighted-ranking）

背景发现：chat 管线从不自行解析 rerank 模型（仅 agent 配置可设），rerank 阶段
（复合分 + MMR）一直被静默跳过——v2 基线实为纯 RRF 排序。本特性新增请求级
`rerank_model_id`（支持 `auto`）与 `enable_weighted_ranking` + `ranking` 权重对象。

| 指标 | ① off（纯 RRF） | ② +rerank（λ0.7） | ③ +rerank（λ1.0） |
|---|---|---|---|
| EM / F1 | 47.0 / 65.8 | **53.1 / 71.3** | 47.5 / 65.3 |
| 金标进 top3 / top5 / top10 | 70.5 / 77.0 / 87.0 | **83.2 / 90.3 / 93.4** | 81.8 / 85.9 / 89.4 |
| 金标全命中 | 90.0% | **90.8%** | 85.9% |
| MRR / 引用宽度 | 90.4 / 28.9 | 92.9 / 11.5 | 88.9 / 11.1 |

结论（|Δ|<2 为噪声；②含 2 题、③含 1 题空答/超时按 0 分计，方向只会低估②③）：

1. **rerank 信号是池模式最大单杠杆**：top3 +12.7、top5 +13.3、EM +6.1、F1 +5.5，全部远超噪声。
2. **MMR λ=0.7 是功臣而非负担**（原假设被推翻）：λ=1.0 相对 λ0.7 回吐 top5 −4.5、
   EM −5.6、金标全命中 −5.0——多样性惩罚防止同文档近重复挤占 RerankTopK 名额，
   多跳题的第二篇金标因此更容易进 top-k。
3. 信号分布实测：RRF 基线分仅 0.008–0.011，rerank 模型分 0.30–0.88——旧公式里
   0.3×基线项贡献≈0.003，权重再平衡在此标尺上是死胡同；有效旋钮是"接不接 rerank"和 λ。
4. 引用宽度 28.9→11.5 是 rerank+RerankTopK 的真实行为（非统计假象），top10 对比部分
   由此受益；top3/top5 与 EM/F1 为货真价实的排序改进。
5. 单轮 r1；+12 级差异不会被第二轮翻盘，但按协议正式结论前应在下一棒补 r2 复验。

复现：`run_eval.py --ingest …pool.ingest.jsonl` + `--rerank-model auto [--ranking '{"mmr_lambda":1.0}']`。

## v4 智能推理模式标尺 + RankingConfig/kb-routing 接入（eval-100 池模式，2026-09-08，分支 feature/agent-lab）

背景：eval harness 此前只打 knowledge-chat；智能推理（/agent-chat）没有评估覆盖。
本版新增 `--agent-id` 模式（POST /agent-chat + agent_id；EM/F1 取 answer 文本，
ref_titles 从落库的 agent_steps 取 knowledge_search 结果有序并集——agent 模式没有
references 事件，引用是答案内嵌 `<kb/>` 标签）。智能体 hotpot-rag-agent
（rag-qa 型，model=deepseek-v4-flash，rerank=qwen3-rerank，KB=hotpot-pool-500，
max_iterations=15，web_search 关闭）。

| 指标 | ① agent 基线 r1 | ② weighted-defaults | ③ λ=1.0 | ④ routing-on（kb=all 智能体） |
|---|---|---|---|---|
| EM / F1 | 53.0 / 75.49 | 55.0 / 76.05 | 54.0 / 76.69 | 55.0 / 75.62 |
| 金标 top3 / top5 / top10 | 78.0 / 82.0 / 84.0 | 77.5 / 83.0 / 86.5 | **82.0 / 86.5 / 90.5** | 75.5 / 80.5 / 84.0 |
| 金标全命中 | 80.0% | 82.0% | **86.0%** | 82.0%（零命中 14%） |
| MRR | 86.67 | 87.67 | **91.67** | 81.08 |

结论：

1. **agent 基线（首条标尺）**：EM 53.0 与 chat+rerank（53.1）打平、比 chat 无 rerank
   （48.6）高 4.4——agent 的 knowledge_search 原生带 rerank，早已吃到精排红利；
   F1 75.5 显著高于 chat 双臂（agent 可多轮补检索）。
2. **② vs ① 全部在噪声带内**（EM +2.0 / F1 +0.56 / top3 −0.5 / top5 +1.0）：
   RankingConfig 接入 + U-03 共享内核重构在默认参数下零回归（数学等价+行为等价双确认）。
3. **③ λ=1.0 检索指标全面超噪声提升（与 chat 侧相反！）**：top3 +4.0 / top5 +4.5 /
   top10 +6.5 / 全命中 +6.0 / MRR +5.0，EM/F1 持平。chat 是一次性列表消费（多样性
   防近重复挤占名额，λ=1.0 回吐）；agent 是可再检索的消费者，纯相关度排序更优。
   **chat 的最优 λ 不迁移到 agent。** 单轮 r1，正式结论需 r2 复验。
4. agent 侧 ref_titles 语义=检索并集（cite_width 8.4-9.4），跨模式对比方向性成立、
   模式内 A/B 精确。
5. kb-routing（agent 侧）：`enable_kb_routing` + 智能体 kb_selection_mode=all 且
   未显式选库时路由收窄（复用 chat 的 kb_router，事件同形状）；手测 541 库→3 库、
   选中问题自有小库、答案正确。**④ routing-on 单臂已完成**（EM/F1 与基线持平，
   检索指标低于池基线：MRR −5.6、top3 −2.5——路由 3 库替代单一 pool 库有小幅漏检，
   零命中 11%→14%）；**off 配对臂因 DeepSeek 账户 402 断费未完成**（arm4-off 100 题
   全空，已删除无效数据）——充值后重跑 `--agent-id <agent-all-id>`（不带 --route）
   补全配对，再做正式结论。
   复现：`run_eval.py --agent-id <id> [--route] [--ranking '{...}']`。
6. **中断记录（2026-09-08 03:08）**：DeepSeek（10011 QA 模型）余额耗尽 402，
   arm4-off 与 r2 复验受阻；充值后优先补 r2（协议两轮要求）。

复现：`run_eval.py --manifest …eval-100.jsonl --ingest …pool.ingest.jsonl --agent-id <agent-id>
--label <label> [--ranking '{"mmr_lambda":1.0}'] [--route]`。

