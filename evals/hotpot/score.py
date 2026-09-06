#!/usr/bin/env python3
"""Score a run against the manifest: EM, token F1 (HotpotQA official
normalization) and support-fact retrieval hit; optional baseline diff."""

from __future__ import annotations

import argparse
import json
import re
import string
from collections import Counter
from pathlib import Path


def normalize_answer(s: str) -> str:
    """HotpotQA official normalization (squad-style)."""

    def remove_articles(text: str) -> str:
        return re.sub(r"\b(a|an|the)\b", " ", text)

    def white_space_fix(text: str) -> str:
        return " ".join(text.split())

    def remove_punc(text: str) -> str:
        exclude = set(string.punctuation)
        return "".join(ch for ch in text if ch not in exclude)

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
    for rec in run:
        q = man.get(rec["qid"])
        if not q:
            continue
        if rec.get("error") or not rec.get("answer"):
            errors += 1
            continue
        scored += 1
        num_same, n_pred, n_gold = f1_score(rec["answer"], q["answer"])
        prec = num_same / n_pred if n_pred else 0
        rec_ = num_same / n_gold if n_gold else 0
        f1 = 2 * prec * rec_ / (prec + rec_) if (prec + rec_) else 0.0
        f1_sum += f1
        if normalize_answer(rec["answer"]) == normalize_answer(q["answer"]):
            em_hit += 1
        refs = {title_key(t) for t in rec.get("ref_titles", [])}
        hits = sum(1 for g in q["gold_titles"] if title_key(g) in refs)
        if hits == len(q["gold_titles"]):
            support_full += 1
        elif hits > 0:
            support_partial += 1
        else:
            support_none += 1
    n = max(scored, 1)
    return {
        "questions": len(man),
        "scored": scored,
        "errors_or_empty": errors,
        "EM": round(em_hit / n * 100, 2),
        "F1": round(f1_sum / n * 100, 2),
        "support_full_pct": round(support_full / n * 100, 2),
        "support_partial_pct": round(support_partial / n * 100, 2),
        "support_zero_pct": round(support_none / n * 100, 2),
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
