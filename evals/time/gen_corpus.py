#!/usr/bin/env python3
"""Deterministic synthetic corpus + question set for time-aware retrieval.

Design (docs/design/time-aware-retrieval-design.md §5):
- ~192 docs over 24 months (2024-10 .. 2026-09), 3 companies x ~8 reports/month
- every doc states its month explicitly (date anchor) and carries a unique
  (company, month) rating that only appears in that month
- 60 questions: 20 explicit-window / 20 as-of / 20 implicit-recency
- gold for each question is the (company, month, rating) that the correct
  window semantics must select — plus the set of in-window doc titles.

Usage: python3 gen_corpus.py --out-dir . [--seed 20260918]
"""
from __future__ import annotations

import argparse
import json
import random
from datetime import date
from pathlib import Path

COMPANIES = {
    "恒星科技": "Hengxing Technology",
    "北辰能源": "Beichen Energy",
    "临港建设": "Lingang Construction",
}
RATING_LADDER = ["AAA", "AA+", "AA", "AA-", "A+", "A", "A-", "BBB+", "BBB", "BBB-", "BB+", "BB"]
EVENTS = [
    "中标智慧园区项目", "完成 B 轮融资", "更换主承销商", "发布新一代产品线",
    "收到监管问询函", "提前偿还部分债券", "新增海外订单", "更换财务总监",
    "回购股份", "与地方政府签订战略合作",
]

def month_iter(start: date, months: int):
    y, m = start.year, start.month
    for _ in range(months):
        yield date(y, m, 1)
        m += 1
        if m == 13:
            y, m = y + 1, 1

def fmt_zh(d: date) -> str:
    return f"{d.year}年{d.month}月"

def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--out-dir", default=".")
    ap.add_argument("--seed", type=int, default=20260918)
    args = ap.parse_args()

    rng = random.Random(args.seed)
    out = Path(args.out_dir)
    out.mkdir(parents=True, exist_ok=True)

    start = date(2024, 10, 1)
    months = list(month_iter(start, 24))

    docs = []
    for ci, (cname, ename) in enumerate(COMPANIES.items()):
        # Each company walks its own deterministic rating path (a random walk
        # over the ladder); the (company, month) rating is unique per month.
        pos = 3 + ci  # AA / AA- / A+ starting points
        for mi, m in enumerate(months):
            if mi > 0 and rng.random() < 0.45:
                pos += rng.choice([-1, 1])
                pos = max(0, min(len(RATING_LADDER) - 1, pos))
            rating = RATING_LADDER[pos]
            outlook = rng.choice(["稳定", "正面", "负面"])
            event = EVENTS[(ci * 31 + mi * 7 + 3) % len(EVENTS)]
            revenue = 1.2 + ((ci * 13 + mi * 5) % 40) / 10.0
            title = f"{cname}{fmt_zh(m)}月度经营快报"
            content = (
                f"{cname}（{ename}）{fmt_zh(m)}月度经营快报。报告所属月份：{fmt_zh(m)}。"
                f"本月主体信用评级为 {rating}，展望{outlook}（评级机构：中证信评）。"
                f"当月营业收入约 {revenue:.1f} 亿元，环比变动 {('+%.1f%%' % (revenue * 3 % 8)) if revenue else '0%'}。"
                f"本月重要事项：{event}。本快报由公司董事会办公室于 {m.year}-{m.month:02d}-05 发布。"
            )
            # Mid-month business date: the report itself says it is published
            # on the 5th, and the eval runs mid-month — a month-end date would
            # fall in the future and get hard-filtered out of "recent" windows.
            docs.append({
                "title": title,
                "company": cname,
                "month": f"{m.year}-{m.month:02d}",
                "business_date": f"{m.year}-{m.month:02d}-05",
                "content": content,
                "rating": rating,
                "outlook": outlook,
            })

    with open(out / "corpus.jsonl", "w", encoding="utf-8") as f:
        for d in docs:
            f.write(json.dumps(d, ensure_ascii=False) + "\n")

    # ---- questions ----
    by_company_month = {(d["company"], d["month"]): d for d in docs}
    qs = []

    def window(company: str, start_m: date, end_m: date | None):
        got = []
        for m in months:
            if m < start_m:
                continue
            if end_m and m > end_m:
                continue
            d = by_company_month[(company, f"{m.year}-{m.month:02d}")]
            got.append(d)
        return got

    # Type 1: explicit window (recent N months)
    for i in range(20):
        company = list(COMPANIES)[i % 3]
        span = [3, 6, 12][i % 3]
        anchor = months[-1]
        start_m = months[max(0, len(months) - span)]
        docs_in = window(company, start_m, anchor)
        latest = docs_in[-1]
        qs.append({
            "qid": f"tw-{i:03d}", "type": "explicit_window",
            "question": f"近{span}个月{company}的主体信用评级是多少？请只回答评级符号本身（如 AA、A-、BBB+），不要任何其他文字。",
            "gold_answer": latest["rating"],
            "gold_months": [d["month"] for d in docs_in],
            "company": company, "window": [start_m.isoformat(), anchor.isoformat()],
        })

    # Type 2: as-of point in time
    for i in range(20):
        company = list(COMPANIES)[(i + 1) % 3]
        cut = months[6 + (i * 2) % 14]  # anywhere mid-timeline
        docs_in = window(company, months[0], cut)
        latest = docs_in[-1]
        qs.append({
            "qid": f"tp-{i:03d}", "type": "as_of",
            "question": f"截至{fmt_zh(cut)}末，{company}的主体信用评级是多少？请只回答评级符号本身（如 AA、A-、BBB+），不要任何其他文字。",
            "gold_answer": latest["rating"],
            "gold_months": [d["month"] for d in docs_in],
            "company": company, "window": [months[0].isoformat(), cut.isoformat()],
        })

    # Type 3: implicit recency (current / latest)
    for i in range(20):
        company = list(COMPANIES)[(i + 2) % 3]
        latest = by_company_month[(company, f"{months[-1].year}-{months[-1].month:02d}")]
        qs.append({
            "qid": f"im-{i:03d}", "type": "implicit_recency",
            "question": f"{company}现在的主体信用评级是多少？请只回答评级符号本身（如 AA、A-、BBB+），不要任何其他文字。",
            "gold_answer": latest["rating"],
            "gold_months": [latest["month"]],
            "company": company, "window": [months[-1].isoformat(), months[-1].isoformat()],
        })

    with open(out / "questions.jsonl", "w", encoding="utf-8") as f:
        for q in qs:
            f.write(json.dumps(q, ensure_ascii=False) + "\n")

    print(f"docs={len(docs)} months={len(months)} questions={len(qs)}")
    for t in ("explicit_window", "as_of", "implicit_recency"):
        n = sum(1 for q in qs if q["type"] == t)
        print(f"  {t}: {n}")

if __name__ == "__main__":
    main()
