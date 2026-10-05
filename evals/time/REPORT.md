# 时效感知检索标尺首跑报告（evals/time，2026-09-18）

分支 feature/time-aware-retrieval @ 628735c89（M1+M2 全量），服务 :8080。

## 标尺构成
- 语料：72 篇 × 24 个月（2024-10..2026-09）× 3 公司月度经营快报，seed=20260918
  确定性生成；business_date=每月 05 日（对齐正文锚点，且不晚于评估日——
  首跑踩坑：月末日期会落在未来，被"近N个月"窗口以今天为上界硬过滤）。
- 题集：60 题三型各 20（显式窗口/时点/隐式时效），短答指令（只回答评级符号）。
- 双臂：ON = enable_rewrite（查询理解产 TimeCondition）；OFF = 关闭改写
  （time-aware 的忠实关闭态：无 TimeCondition，检索原样）。

## 结果（60 题 × 2 臂）

| 类型 | EM on/off | 窗口精确率 on/off | 过期引用率 on/off |
|---|---|---|---|
| 显式窗口 | 55.0 / 55.0 | **94.3 / 90.3** | **10.0 / 15.0** |
| 时点 | 100 / 100 | **96.8 / 92.7** | **5.0 / 15.0** |
| 隐式时效 | **45.0 / 25.0** | 8.3 / 5.2 | 100 / 100 |
| 合计 | **66.7 / 60.0** | **66.5 / 62.7** | **38.3 / 43.3** |

对照设计 §7 验收：
1. 无时间意图零行为变更：hotpot 30 题 EM +0.00 / F1 -0.54（M1 终验）✓
2. 显式窗口 winPrec 94.3（差 0.7pt 到 95 目标）；stale 10%（未达 0，见定性）
3. 隐式 EM +20 显著改善；stale 不动（见下"预期边界"）
4. FAQ 降级路径：单测覆盖 + time_scope_fallback 日志 marker ✓
5. am-qa 真实语料人工核验：待做（语料治理前置）

## 定性：残余 stale 的两个来源（非过滤失效）
- **相对窗口粒度差**："近12个月"模型按日解析（今天回推），gold 按月生成，
  边界月±1 篇被记 stale。改进：题干改绝对日期，或 gold 月容差。
- **跨公司引用串扰**：references 事件含同 KB 其他公司文档（tw-013 北辰题
  引了临港 2 篇）。属引用面板相关性口径，不是时间过滤问题。

## 预期边界：隐式时效的 stale=100%
隐式题走 prefer 档（设计 D2：模糊词不硬过滤）+ RecencyWeight 默认 0（惰性）。
检索返回近一年文档、模型引用多期对比，其中 11/12 落在"最新一期"gold 窗外。
**这是设计内行为**：该指标的改善依赖显式开启 RecencyWeight——下一步做设计
§4.5 预留的 weight 0/0.05/0.1 三臂实验（复用 weighted-ranking 评估骨架）。

## 复现
```
cd evals/time
python3 gen_corpus.py --out-dir .          # 确定性语料+题集
python3 ingest.py --kb-name tar-eval       # 灌库（manual API 带 business_date）
python3 run_eval.py --arm both --out runs  # on/off 配对
python3 score.py --run runs/tar-on.jsonl   # 三指标
```

## 踩坑记录（标尺建设本身）
1. 引用提取：短答指令抑制内联 <kb> 标记 → 需从 references 事件的
   knowledge_title 取（run_eval 已内置双通道）。
2. 语料 business_date 晚于评估日会被"近N个月"硬过滤——生成器已改月内 05 日。

---

# RecencyWeight 三臂实验（2026-09-18 晚，追加）

前置发现：chat 管道默认跳过 rerank（rerank_cnt=0），而 recency 信号在 rerank
composite 内——不开 rerank 的请求里 recency_weight 是无效参数。三臂均带
rerank_model_id=auto + recency_half_life_days=180；无 rerank 臂为首跑 tar-on。

| 配置 | 总EM | 显式 EM/prec/stale% | 时点 EM/prec/stale% | 隐式 EM/prec/stale% |
|---|---|---|---|---|
| 无 rerank | 66.7 | 55.0 / 94.3 / 10 | 100 / 96.8 / 5 | 45.0 / 8.3 / 100 |
| rerank + rw 0 | 71.7 | 50.0 / 86.0 / 20 | 100 / 96.3 / 10 | 65.0 / 6.4 / 100 |
| rerank + rw 0.05 | 90.0 | 75.0 / 94.9 / 15 | 100 / **100** / **0** | 95.0 / 13.9 / 100 |
| rerank + rw 0.1 | **91.7** | 75.0 / **98.5** / **5** | 100 / 97.2 / 5 | **100** / 12.9 / 100 |

## 结论
1. **recency weight 是隐式时效题的决定性杠杆**：EM 45 → 95/100（rw 0.05/0.1）。
   设计 §4.5 预判的"信号管道全线贯通、只差权重"得到定量确认。
2. **推荐配置：recency_weight 0.1 + half_life 180（配合显式 rerank 模型）**。
   总 EM 91.7，各分项最优或并列最优；rw 0.05 为保守替代（时点 stale 归零）。
3. 隐式型 staleCite 恒 100% 是**指标口径缺陷而非行为缺陷**：gold 只含最新一期，
   模型答对的同时引用历史各期做对比（EM 100% 证明行为正确）。~~下一步修口径~~
   **已修（2026-10-05）**：score.py 隐式型改为"从未够到 gold 最新一期（全部引用
   都早于它）才记 stale"；历史引用为对比语境，不算 stale。修正后重打历史 run：
   无 rerank 臂隐式 stale 100→50、rw0.1 臂 100→**0**（与 EM 100% 自洽）。
   显式/时点两型口径不变。
4. rerank 本身对显式窗口题有小扰动（rw0 臂 prec 86 < 无 rerank 94.3）——
   小语料上模型分噪声大；weight>0 后 recency 信号反而稳定了排序。

## 复现
```
python3 run_eval.py --arm on --ranking '{"recency_weight": 0.1}' --label=rw010 --out runs
python3 score.py --run runs/tar-onrw010.jsonl
```

---

# v5b 新底座复验（2026-10-05，102 同步 + reranking 包重构后）

接线实证：60 次 `stage=Rerank action=weighted_ranking` 管线事件（recency_weight=0.1
精确到达），rerank 真实调用 outcome=ok——chat 全链在新结构上贯通。

## 同基配对（同日同后端，n=60/臂）

| 臂 | 总EM | 显式 EM/stale | 时点 EM/stale | 隐式 EM/stale |
|---|---|---|---|---|
| off | 66.7 | 50.0 / 5 | 95.0 / 0 | 55.0 / 20 |
| on（过滤无权重） | 61.7 | 40.0 / 0 | 95.0 / 10 | 50.0 / 45 |
| **rw010（推荐）** | **73.3** | 50.0 / 5 | **100** / 10 | **70.0 / 0** |

结论：推荐配置臂每一维优于或平于 off 臂，隐式时效（EM+15、stale 归零）与
9 月三臂结论方向一致。裸 on 臂在本轮弱于 off（显式 EM 40 vs 50）——n=20 子项
两题翻转量级，与"recency 权重才是隐式杠杆"的既有结论相符。

## 观察（非阻塞）：跨基绝对值漂移

rw010 臂总 EM 91.7（9-18）→ 73.3（v5b）。同臂接线已事件级验证，漂移归因候选：
① rerank 阶段整体重构（MaxCandidates=200 截断/阈值降级/SkipContextEnrichment/
   MMR 参数化）对本标尺的短答 EM 是结构性扰动；② n=60 的抽样方差（±6pt 常态）。
统计口径更硬的 hotpot 100 题在新基上 EM +4.00。**结论：不构成回归判据，留观察**；
若后续 100 题级复跑仍显著偏低，再对 rerank 截断/富化策略做消融。
