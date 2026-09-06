#!/usr/bin/env python3
"""Render eval runs into a self-contained HTML report.

Usage:
  python3 make_report.py --out ../reports/report.html \
      --title "试点 20 题" --run baseline-en=../data/runs/pilot-baseline-en.jsonl \
      [--run label=path ...] [--baseline label]

The report embeds the plain-language metric explanations so it can be
shared as-is. Data tables show every question with per-question EM/F1.
"""

from __future__ import annotations

import argparse
import datetime
import json
from pathlib import Path

from score import f1_score, load_jsonl, normalize_answer, title_key

GREEN = "#0a7d3f"
RED = "#b3261e"
AMBER = "#a05a00"


def question_rows(manifest: list[dict], run: list[dict]) -> tuple[list[dict], dict]:
    man = {q["qid"]: q for q in manifest}
    rows = []
    agg = {"n": 0, "em": 0, "f1": 0.0, "full": 0, "partial": 0, "zero": 0, "err": 0}
    for rec in run:
        q = man.get(rec["qid"])
        if not q:
            continue
        if rec.get("error") or not rec.get("answer"):
            agg["err"] += 1
            rows.append(
                {"qid": rec["qid"], "q": q["question"], "gold": q["answer"],
                 "pred": f"[错误] {rec.get('error','')[:60]}", "em": None,
                 "f1": 0.0, "hits": None, "refs": rec.get("ref_titles", [])}
            )
            continue
        agg["n"] += 1
        num_same, n_pred, n_gold = f1_score(rec["answer"], q["answer"])
        prec = num_same / n_pred if n_pred else 0
        recl = num_same / n_gold if n_gold else 0
        f1 = 2 * prec * recl / (prec + recl) if (prec + recl) else 0.0
        em = normalize_answer(rec["answer"]) == normalize_answer(q["answer"])
        agg["f1"] += f1
        agg["em"] += 1 if em else 0
        refs = {title_key(t) for t in rec.get("ref_titles", [])}
        hits = sum(1 for g in q["gold_titles"] if title_key(g) in refs)
        total = len(q["gold_titles"])
        if hits == total:
            agg["full"] += 1
        elif hits > 0:
            agg["partial"] += 1
        else:
            agg["zero"] += 1
        rows.append(
            {"qid": rec["qid"], "q": q["question"], "gold": q["answer"],
             "pred": rec["answer"], "em": em, "f1": f1, "hits": f"{hits}/{total}",
             "refs": rec.get("ref_titles", [])}
        )
    return rows, agg


def esc(s: str) -> str:
    return (
        str(s)
        .replace("&", "&amp;")
        .replace("<", "&lt;")
        .replace(">", "&gt;")
        .replace('"', "&quot;")
    )


CSS = """
body{font-family:-apple-system,'PingFang SC','Microsoft YaHei',sans-serif;margin:0;background:#f6f7f9;color:#1c1e21}
.wrap{max-width:1080px;margin:0 auto;padding:24px 20px 60px}
h1{font-size:22px} h2{font-size:17px;margin-top:34px;border-left:4px solid #2f6fed;padding-left:10px}
.meta{color:#667;font-size:13px}
.cards{display:flex;gap:14px;flex-wrap:wrap;margin:18px 0}
.card{background:#fff;border:1px solid #e3e6ea;border-radius:12px;padding:14px 22px;min-width:150px}
.card .v{font-size:30px;font-weight:700} .card .k{font-size:12px;color:#667;margin-top:2px}
table{border-collapse:collapse;width:100%;background:#fff;font-size:13px;border:1px solid #e3e6ea;border-radius:8px;overflow:hidden}
th,td{padding:7px 10px;border-bottom:1px solid #eef0f2;text-align:left;vertical-align:top}
th{background:#f0f3f7;font-weight:600;white-space:nowrap}
.pill{display:inline-block;padding:1px 9px;border-radius:10px;font-size:12px;font-weight:600;color:#fff}
.p-ok{background:#0a7d3f}.p-bad{background:#b3261e}.p-mid{background:#a05a00}
.refs{color:#889;font-size:11px;max-width:340px}
.expl{background:#fff;border:1px solid #e3e6ea;border-radius:12px;padding:16px 20px;font-size:14px;line-height:1.75}
.expl b{color:#2f6fed}
details summary{cursor:pointer;font-weight:600}
"""


def render(title: str, runs: list[dict]) -> str:
    now = datetime.datetime.now().strftime("%Y-%m-%d %H:%M")
    html = [
        "<!doctype html><html lang=zh><meta charset=utf-8>",
        f"<title>{esc(title)}</title><style>{CSS}</style>",
        '<div class=wrap>',
        f"<h1>{esc(title)}</h1>",
        f'<div class=meta>生成时间 {now} ｜ 样本固定种子 seed=42 ｜ 指标解释见文末</div>',
    ]
    for run in runs:
        rows, a = run["rows"], run["agg"]
        n = max(a["n"], 1)
        html.append(f'<h2>{esc(run["label"])}（{a["n"]+a["err"]} 题）</h2>')
        html.append('<div class=cards>')
        for val, key, color in [
            (f"{a['em']/n*100:.1f}%", "EM 精确匹配率", GREEN),
            (f"{a['f1']/n*100:.1f}", "F1 平均分", GREEN),
            (f"{a['full']/n*100:.1f}%", "金标段落全命中", GREEN),
            (f"{a['partial']/n*100:.1f}%", "金标部分命中", AMBER),
            (f"{a['zero']/n*100:.1f}%", "金标零命中", RED),
        ]:
            html.append(
                f'<div class=card><div class=v style="color:{color}">{val}</div>'
                f'<div class=k>{key}</div></div>'
            )
        if a["err"]:
            html.append(f'<div class=card><div class=v style="color:{RED}">{a["err"]}</div><div class=k>错误/空答</div></div>')
        html.append("</div>")
        html.append("<details><summary>逐题明细（点开）</summary><table><tr>"
                    "<th>题号</th><th>问题</th><th>标准答案</th><th>模型答案</th>"
                    "<th>EM</th><th>F1</th><th>金标命中</th><th>引用来源</th></tr>")
        for r in rows:
            if r["em"] is None:
                em_pill = '<span class="pill p-bad">错误</span>'
            else:
                em_pill = (f'<span class="pill p-ok">√</span>' if r["em"]
                           else '<span class="pill p-bad">×</span>')
            refs = "、".join(dict.fromkeys(r["refs"]))[:160] or "—"
            html.append(
                f"<tr><td>{esc(r['qid'])}</td><td>{esc(r['q'][:90])}</td>"
                f"<td><b>{esc(r['gold'])}</b></td><td>{esc(r['pred'][:140])}</td>"
                f"<td>{em_pill}</td><td>{r['f1']*100:.0f}</td>"
                f"<td>{esc(r['hits'] if r['hits'] is not None else '-')}</td>"
                f"<td class=refs>{esc(refs)}</td></tr>"
            )
        html.append("</table></details>")
    html.append("""
<h2>指标解释（大白话）</h2>
<div class=expl>
<p><b>金标段落命中</b>：每道题配了 10 段资料，其中只有 <b>2 段是出题人标注的"含答案证据的段落"（金标）</b>，
其余 8 段是故意用来迷惑的干扰项。系统回答时会列出它参考了哪些资料——
我们检查它列的资料里有没有那 2 段金标。<b>全命中=两段都翻到了；部分命中=只翻到一段；零命中=完全翻错了地方。</b>
这个指标衡量"<b>找资料的能力</b>"，不受答题文笔影响——好比开卷考试，先看你有没有翻到正确的那两页。</p>
<p><b>EM（Exact Match，精确匹配）</b>：模型的答案经过标准化处理（统一小写、去掉冠词 a/an/the 和标点）后，
和标准答案<b>一模一样才算得分</b>，答对一题得 1 分，EM% = 答对题数 ÷ 总题数。它非常严格——
标准答案是"Dutch"，答"the Dutch heritage"就算 0 分。</p>
<p><b>F1（部分得分）</b>：比 EM 宽容，按<b>词的重叠程度</b>给分。
答案"the Dutch heritage"和标准"Dutch"共享 1 个词，能拿到约 67 分；完全不相干才是 0。
它弥补了 EM 的苛刻：<b>意思对了但多说了几个字</b>的情况，EM 给 0 而 F1 给高分。</p>
<p><b>为什么要分开看</b>：金标命中衡量检索层（找资料），EM/F1 叠加了生成层（组织答案）。
将来评估"知识库路由""排序加权"这类改造时，先看金标命中有没有变化（归因检索层）；
评估"改写领域化"时重点看 EM/F1（归因生成层）。<b>两个数分开，才知道该夸哪层、该修哪层。</b></p>
</div></div>""")
    return "".join(html)


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--manifest", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--title", default="WeKnora 评估报告")
    ap.add_argument("--run", action="append", required=True, help="label=path")
    args = ap.parse_args()

    manifest = load_jsonl(args.manifest)
    runs = []
    for spec in args.run:
        label, path = spec.split("=", 1)
        rows, agg = question_rows(manifest, load_jsonl(path))
        runs.append({"label": label, "rows": rows, "agg": agg})
    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(render(args.title, runs), encoding="utf-8")
    print("report:", out)


if __name__ == "__main__":
    main()
