# 反馈飞轮（轻量版，台账 T13 收官）

设计决策（2026-10-06）：黄金集**不住数据库**，住在 git 里的 JSONL 清单——
复用 evals 骨架的母语形态，可 diff、可评审、零后端改动。重型方案
（in-app 数据集服务去硬编码 + 应用内回归跑批）明确搁置；反馈表数据完整，
未来要产品化随时可升级。

## 飞轮四步

```
① 收集   聊天 👎（理由+评语）→ message_feedback（快照含 question/KB/错答）
② 导出   EVAL_TENANT_ID=10000 EVAL_USER=cnfeichen \
         python3 export_golden.py                 # 增量合并，人工 answer 永不覆盖
③ 分诊   编辑 golden.jsonl：补 "answer"（正确答案）；核对 kb_ids（路由会话可能
         为空——人工指到出问题的库）；删掉不值得留的条目；commit
④ 回归   换模型/调参/升级前，在 ../hotpot 下：
         EVAL_TENANT_ID=10000 EVAL_USER=cnfeichen \
         python3 run_eval.py --manifest ../feedback/golden.jsonl \
             --out ../data/runs/golden-<label>.jsonl --label <label> --lang raw
         python3 score.py --manifest ../feedback/golden.jsonl \
             --run ../data/runs/golden-<label>.jsonl
```

## 字段约定

- `qid` = `fb-<反馈id前12位>`：与反馈行稳定对应；反馈被删不清条目（入册即资产）
- `kb_ids`：来自反馈快照的 execution_context；人工分诊时可修正
- `model_wrong` / `reason` / `comment`：分诊参考，不参与评分
- `answer`：**人工补写的正确答案**（EM/F1 的金标）；中文题用 `--lang raw`

## 边界

- 无 gold_titles → score.py 自动只计 EM/F1（引用指标跳过）
- 清单条目自带 kb_ids 时优先于 ingest 映射（run_eval 原生支持）
- 上线节奏建议：黄金集 ≥20 题前当冒烟用（趋势），之后才当回归门（阈值）
