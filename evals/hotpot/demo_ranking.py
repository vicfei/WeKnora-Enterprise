#!/usr/bin/env python3
"""Manual A/B demo for weighted ranking: one question, two configs, side by side.

Runs the SAME question twice against the shared pool KB:
  arm 1  off   — today's default chat behavior (rerank silently skipped)
  arm 2  rerank — rerank_model_id=auto, optionally + --ranking weights

Prints answer, gold answer (when the question comes from eval-100) and the
cited-document list, so the ordering effect is visible by eye.

Examples:
  python3 demo_ranking.py                     # default demo question hp-0000
  python3 demo_ranking.py --qid hp-0003
  python3 demo_ranking.py --question "Which magazine was started first, ...?"
  python3 demo_ranking.py --ranking '{"mmr_lambda":1.0}'
"""

from __future__ import annotations

import argparse
import json
import time

from common import EvalClient, reference_titles


def load_question(qid: str) -> tuple[str, str | None]:
    with open("../data/eval-100.jsonl", encoding="utf-8") as fh:
        for line in fh:
            r = json.loads(line)
            if r["qid"] == qid:
                return r["question"], r["answer"]
    raise SystemExit(f"qid {qid} not in eval-100; use --question for custom text")


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--qid", default="hp-0000", help="question id from eval-100")
    ap.add_argument("--question", default="", help="custom question text (overrides --qid)")
    ap.add_argument("--top", type=int, default=8, help="how many cited titles to print")
    ap.add_argument("--ranking", default="", help='JSON, e.g. \'{"mmr_lambda":1.0}\'')
    args = ap.parse_args()

    question, gold = args.question, None
    if not question:
        question, gold = load_question(args.qid)
    question += (
        "\n\nAnswer in English. Reply with ONLY the short answer "
        "(a phrase, name, number, or yes/no) — no explanation."
    )

    kb_id = json.load(open("../data/eval-500.jsonl.pool-state.json", encoding="utf-8"))["kb_id"]
    rerank_body: dict = {"rerank_model_id": "auto"}
    if args.ranking:
        rerank_body["enable_weighted_ranking"] = True
        rerank_body["ranking"] = json.loads(args.ranking)

    client = EvalClient()
    arms = [("off (现状, rerank 跳过)", None), ("rerank on", rerank_body)]
    for label, extra in arms:
        sid = client.create_session(f"demo-ranking {label}")
        t0 = time.time()
        r = client.ask_sse(sid, kb_id, question, timeout=300, extra_body=extra)
        titles = reference_titles(r["references"])
        print(f"\n===== {label}  ({time.time()-t0:.0f}s) =====")
        print(f"Q: {question.splitlines()[0]}")
        if gold:
            print(f"gold: {gold}")
        print(f"A: {r['answer']}")
        print(f"cited {len(titles)} docs, first {min(args.top, len(titles))}:")
        for i, t in enumerate(titles[: args.top], 1):
            print(f"  {i}. {t}")
        if not r["done"]:
            print("!! SSE 未收尾（答案可能被截断）")


if __name__ == "__main__":
    main()
