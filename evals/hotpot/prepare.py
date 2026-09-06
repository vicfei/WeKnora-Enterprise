#!/usr/bin/env python3
"""Sample HotpotQA dev(distractor) questions into an eval manifest.

Input is the HuggingFace parquet export (distractor/validation, 7405 rows)
— the original CMU JSON endpoint is dead; the parquet is byte-equivalent.
Fixed seed => reproducible sample; the manifest is the single source of
truth for ingest/run/score so every round compares identical questions.
"""

from __future__ import annotations

import argparse
import json
import random
from pathlib import Path

import pyarrow.parquet as pq

DATA_DIR = Path(__file__).resolve().parent.parent / "data"
DATASET = DATA_DIR / "hotpot-validation.parquet"
SEED = 42


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--limit", type=int, default=20, help="questions to sample")
    ap.add_argument("--dataset", default=str(DATASET))
    ap.add_argument("--out", default="", help="output manifest (default eval-<n>.jsonl)")
    args = ap.parse_args()

    rows = pq.read_table(args.dataset).to_pylist()
    rng = random.Random(SEED)
    picked = rng.sample(rows, args.limit)

    out = Path(args.out) if args.out else DATA_DIR / f"eval-{args.limit}.jsonl"
    out.parent.mkdir(parents=True, exist_ok=True)
    n_para = 0
    with open(out, "w", encoding="utf-8") as fh:
        for i, item in enumerate(picked):
            paras = [
                {"title": t, "sentences": s}
                for t, s in zip(item["context"]["title"], item["context"]["sentences"])
            ]
            gold = sorted(set(item["supporting_facts"]["title"]))
            fh.write(
                json.dumps(
                    {
                        "qid": f"hp-{i:04d}",
                        "question": item["question"],
                        "answer": item["answer"],
                        "gold_titles": gold,
                        "level": item.get("level", ""),
                        "paragraphs": paras,
                    },
                    ensure_ascii=False,
                )
                + "\n"
            )
            n_para += len(paras)
    print(f"manifest: {out} | questions={args.limit} paragraphs={n_para} seed={SEED}")


if __name__ == "__main__":
    main()
