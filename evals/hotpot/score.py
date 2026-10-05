#!/usr/bin/env python3
"""Score a run against the manifest: EM, token F1 (HotpotQA official
normalization) and support-fact retrieval hit; optional baseline diff."""

from __future__ import annotations

import argparse
import json
import re
import string
import unicodedata
from collections import Counter
from pathlib import Path


# Inline citation contract emitted by modelcontext.ExpandText; rendered as
# clickable chips by the frontend but pure noise for text scoring.
CITATION_TAG_RE = re.compile(r"<(?:kb|web)\b[^>]*/>")


def strip_citation_tags(s: str) -> str:
    return CITATION_TAG_RE.sub("", s)


def normalize_answer(s: str) -> str:
    """HotpotQA official normalization (squad-style)."""

    def remove_articles(text: str) -> str:
        return re.sub(r"\b(a|an|the)\b", " ", text)

    def white_space_fix(text: str) -> str:
        return " ".join(text.split())

    def remove_punc(text: str) -> str:
        exclude = set(string.punctuation)
        # Unicode 标点（en/em-dash、中文逗号句号等）一并剥离：模型输出常用
        # en-dash 连接年份区间，ASCII 白名单漏剥曾致 EM 低估 1-2 分。
        # 类别 P* 覆盖所有 Unicode 标点；符号类（货币/度数）是内容，保留。
        return "".join(
            ch
            for ch in text
            if ch not in exclude and not unicodedata.category(ch).startswith("P")
        )

    def lower(text: str) -> str:
        return text.lower()

    return white_space_fix(remove_articles(remove_punc(lower(s))))


def f1_score(pred: str, gold: str) -> tuple[int, int, int]:
    pred_tokens = normalize_answer(pred).split()
    gold_tokens = normalize_answer(gold).split()
    common = Counter(pred_tokens) & Counter(gold_tokens)
    num_same = sum(common.values())
    if num_same == 0:
        return 0, len(pred_tokens), len(gold_tokens)
    return num_same, len(pred_tokens), len(gold_tokens)


def load_jsonl(path: str) -> list[dict]:
    with open(path, encoding="utf-8") as fh:
        return [json.loads(line) for line in fh if line.strip()]


def title_key(t: str) -> str:
    return normalize_answer(t)


def score_run(manifest: list[dict], run: list[dict]) -> dict:
    man = {q["qid"]: q for q in manifest}
    em_hit = 0
    f1_sum = 0.0
    support_full = 0  # both gold paragraphs cited
    support_partial = 0
    support_none = 0
    scored = 0
    errors = 0
    # rank-aware: citation width + where gold ranks in the ordered refs
    cite_width_sum = 0
    cite_width_n = 0
    rr_sum = 0.0  # mean reciprocal rank of the FIRST gold doc
    rr_n = 0
    recall_at = {1: 0, 3: 0, 5: 0, 10: 0}
    for rec in run:
        q = man.get(rec["qid"])
        if not q:
            continue
        if rec.get("error") or not rec.get("answer"):
            errors += 1
            continue
        scored += 1
        num_same, n_pred, n_gold = f1_score(strip_citation_tags(rec["answer"]), q["answer"])
        prec = num_same / n_pred if n_pred else 0
        rec_ = num_same / n_gold if n_gold else 0
        f1 = 2 * prec * rec_ / (prec + rec_) if (prec + rec_) else 0.0
        f1_sum += f1
        if normalize_answer(strip_citation_tags(rec["answer"])) == normalize_answer(q["answer"]):
            em_hit += 1
        if rec.get("ref_titles") is None:
            continue  # 引用未采集(SSE 中断),只计入 EM/F1
        if not q.get("gold_titles"):
            continue  # 反馈黄金集无金标引用（台账 T13）：只计 EM/F1
        refs = {title_key(t) for t in rec.get("ref_titles", [])}
        hits = sum(1 for g in q["gold_titles"] if title_key(g) in refs)
        if hits == len(q["gold_titles"]):
            support_full += 1
        elif hits > 0:
            support_partial += 1
        else:
            support_none += 1
        # ordered distinct citation list (refs preserve payload order)
        ordered: list[str] = []
        seen: set[str] = set()
        for t in rec.get("ref_titles", []):
            k = title_key(t)
            if k not in seen:
                seen.add(k)
                ordered.append(k)
        cite_width_sum += len(ordered)
        cite_width_n += 1
        gold_keys = [title_key(g) for g in q["gold_titles"]]
        ranks = [ordered.index(g) + 1 for g in gold_keys if g in ordered]
        if ranks:
            rr_sum += 1.0 / min(ranks)
        rr_n += 1
        for k in recall_at:
            recall_at[k] += sum(1 for g in gold_keys if g in ordered[:k])
    n = max(scored, 1)
    rn = max(rr_n, 1)
    gold_per_q = 2
    return {
        "questions": len(man),
        "scored": scored,
        "errors_or_empty": errors,
        "EM": round(em_hit / n * 100, 2),
        "F1": round(f1_sum / n * 100, 2),
        "support_full_pct": round(support_full / n * 100, 2),
        "support_partial_pct": round(support_partial / n * 100, 2),
        "support_zero_pct": round(support_none / n * 100, 2),
        "cite_width_avg": round(cite_width_sum / rn, 1),
        "MRR_first_gold": round(rr_sum / rn * 100, 2),
        **{f"gold_in_top{k}": round(recall_at[k] / (rn * gold_per_q) * 100, 2)
           for k in sorted(recall_at)},
    }


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--manifest", required=True)
    ap.add_argument("--run", required=True)
    ap.add_argument("--baseline", default="", help="previous run JSONL to diff against")
    args = ap.parse_args()

    result = score_run(load_jsonl(args.manifest), load_jsonl(args.run))
    print(json.dumps(result, indent=2, ensure_ascii=False))

    if args.baseline:
        base = score_run(load_jsonl(args.manifest), load_jsonl(args.baseline))
        print("\n=== vs baseline ===")
        for key in ("EM", "F1", "support_full_pct"):
            delta = result[key] - base[key]
            mark = "+" if delta >= 0 else ""
            noise = " (noise band |d|<2)" if abs(delta) < 2 else ""
            print(f"{key:18} {base[key]:7.2f} -> {result[key]:7.2f}  ({mark}{delta:.2f}){noise}")


if __name__ == "__main__":
    main()
