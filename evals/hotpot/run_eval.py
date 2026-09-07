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
    ap.add_argument(
        "--ingest",
        default="",
        help="override ingest map (default <manifest>.ingest.jsonl); "
        "pool mode passes the .pool.ingest.jsonl written by ingest_pool.py",
    )
    ap.add_argument("--out", required=True)
    ap.add_argument("--concurrency", type=int, default=1)
    ap.add_argument("--label", default="", help="config label, e.g. baseline / routing-on")
    ap.add_argument(
        "--route",
        action="store_true",
        help="KB-routing mode: no knowledge_base_ids + enable_kb_routing=true; "
        "capture kb_router event as routed_kbs",
    )
    ap.add_argument(
        "--agent-id",
        default="",
        help="agent mode: POST /agent-chat with this agent id (the agent's own "
        "KB scope applies; --route/--rerank-model/--ranking are chat-path "
        "knobs and are ignored). ref_titles come from persisted agent_steps "
        "(ordered union of knowledge_search results).",
    )
    ap.add_argument(
        "--rerank-model",
        default="",
        help="request-level rerank model id, or 'auto' for the first available "
        "rerank model (the chat pipeline skips rerank entirely by default)",
    )
    ap.add_argument(
        "--ranking",
        default="",
        help="weighted-ranking mode: JSON object merged into the request as "
        "`ranking` with enable_weighted_ranking=true, e.g. "
        "'{\"mmr_lambda\":1.0}'. Recorded verbatim in each run record.",
    )
    ap.add_argument(
        "--lang",
        default="en",
        choices=["en", "raw"],
        help="en: append English short-answer instruction (the default prompts are "
        "Chinese-oriented; without it the model answers Chinese and EM/F1 against "
        "English gold collapses); raw: send the question as-is",
    )
    args = ap.parse_args()

    ranking_cfg = None
    if args.ranking:
        ranking_cfg = json.loads(args.ranking)
        if not isinstance(ranking_cfg, dict):
            ap.error("--ranking must be a JSON object")

    manifest = {q["qid"]: q for q in load_jsonl(args.manifest)}
    ingest_path = args.ingest or (args.manifest + ".ingest.jsonl")
    ingest = {
        r["qid"]: r for r in load_jsonl(ingest_path) if r.get("kb_id")
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
            if not kb_id and not args.route and not args.agent_id:
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
                if args.agent_id:
                    extra = {}
                    if ranking_cfg is not None:
                        extra["enable_weighted_ranking"] = True
                        extra["ranking"] = ranking_cfg
                    if args.route:
                        extra["enable_kb_routing"] = True
                    result = client.ask_sse_agent(sid, args.agent_id, question, extra_body=extra or None)
                    ref_titles = client.load_agent_ref_titles(sid)
                    rec = {
                        "qid": qid,
                        "label": args.label,
                        "answer": result["answer"],
                        "ref_titles": ref_titles,
                        "done": result["done"],
                        "tool_hints": result["tool_hints"],
                        "secs": round(time.time() - t0, 1),
                    }
                    if ranking_cfg is not None:
                        rec["ranking"] = ranking_cfg
                    if result.get("error"):
                        rec["error"] = result["error"]
                elif args.route:
                    result = client.ask_sse_routing(sid, question)
                    rec = {
                        "qid": qid,
                        "label": args.label,
                        "answer": result["answer"],
                        "ref_titles": reference_titles(result["references"]),
                        "routed_kbs": result["routed_kbs"],
                        "done": result["done"],
                        "secs": round(time.time() - t0, 1),
                    }
                else:
                    extra_body = {}
                    if args.rerank_model:
                        extra_body["rerank_model_id"] = args.rerank_model
                    if ranking_cfg is not None:
                        extra_body["enable_weighted_ranking"] = True
                        extra_body["ranking"] = ranking_cfg
                    result = client.ask_sse(sid, kb_id, question, extra_body=extra_body or None)
                    rec = {
                        "qid": qid,
                        "label": args.label,
                        "answer": result["answer"],
                        "ref_titles": reference_titles(result["references"]),
                        "done": result["done"],
                        "secs": round(time.time() - t0, 1),
                    }
                    if ranking_cfg is not None:
                        rec["ranking"] = ranking_cfg
                    if args.rerank_model:
                        rec["rerank_model"] = args.rerank_model
            except Exception as exc:  # noqa: BLE001
                rec = {"qid": qid, "label": args.label, "error": str(exc)[:200]}
            out.write(json.dumps(rec, ensure_ascii=False) + "\n")
            out.flush()
            flag = "ok" if rec.get("done") else "INCOMPLETE"
            print(f"[{i+1}/{len(manifest)}] {qid} {flag} {rec.get('secs', '?')}s "
                  f"refs={len(rec.get('ref_titles', []))}")


if __name__ == "__main__":
    main()
