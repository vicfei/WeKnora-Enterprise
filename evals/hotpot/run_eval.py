#!/usr/bin/env python3
"""Run the QA round for every ingested question, capturing answer + references.

Concurrency stays at 1 by default (remote LLM endpoint, unmeasured quota);
--concurrency 2 is the ceiling validated on the Shanghai box.
Resumable via <out>.progress markers.
"""

from __future__ import annotations

import argparse
import json
import time
from pathlib import Path

from common import EvalClient, reference_titles


def load_jsonl(path: str) -> list[dict]:
    with open(path, encoding="utf-8") as fh:
        return [json.loads(line) for line in fh if line.strip()]


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--manifest", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--concurrency", type=int, default=1)
    ap.add_argument("--label", default="", help="config label, e.g. baseline / routing-on")
    ap.add_argument(
        "--lang",
        default="en",
        choices=["en", "raw"],
        help="en: append English short-answer instruction (the default prompts are "
        "Chinese-oriented; without it the model answers Chinese and EM/F1 against "
        "English gold collapses); raw: send the question as-is",
    )
    args = ap.parse_args()

    manifest = {q["qid"]: q for q in load_jsonl(args.manifest)}
    ingest = {
        r["qid"]: r for r in load_jsonl(args.manifest + ".ingest.jsonl") if r.get("kb_id")
    }

    out_path = Path(args.out)
    out_path.parent.mkdir(parents=True, exist_ok=True)
    done_qids: set[str] = set()
    if out_path.exists():
        done_qids = {r["qid"] for r in load_jsonl(str(out_path))}

    client = EvalClient()
    with open(out_path, "a", encoding="utf-8") as out:
        for i, (qid, q) in enumerate(manifest.items()):
            if qid in done_qids:
                continue
            kb_id = ingest.get(qid, {}).get("kb_id")
            if not kb_id:
                print(f"[skip] {qid}: no kb")
                continue
            t0 = time.time()
            question = q["question"]
            if args.lang == "en":
                question = (
                    f"{question}\n\n"
                    "Answer in English. Reply with ONLY the short answer "
                    "(a phrase, name, number, or yes/no) — no explanation."
                )
            try:
                sid = client.create_session(f"eval {qid} {args.label}".strip())
                result = client.ask_sse(sid, kb_id, question)
                rec = {
                    "qid": qid,
                    "label": args.label,
                    "answer": result["answer"],
                    "ref_titles": reference_titles(result["references"]),
                    "done": result["done"],
                    "secs": round(time.time() - t0, 1),
                }
            except Exception as exc:  # noqa: BLE001
                rec = {"qid": qid, "label": args.label, "error": str(exc)[:200]}
            out.write(json.dumps(rec, ensure_ascii=False) + "\n")
            out.flush()
            flag = "ok" if rec.get("done") else "INCOMPLETE"
            print(f"[{i+1}/{len(manifest)}] {qid} {flag} {rec.get('secs', '?')}s "
                  f"refs={len(rec.get('ref_titles', []))}")


if __name__ == "__main__":
    main()
