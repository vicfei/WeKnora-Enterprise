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
rerank + 多跳生成相当稳；检索端出现真实区分度，是特性评估的主战场。
