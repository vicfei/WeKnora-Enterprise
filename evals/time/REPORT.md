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
