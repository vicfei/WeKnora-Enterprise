#!/usr/bin/env python3
"""Ingest every manifest paragraph into ONE shared-pool KB (title-deduped).

The per-question KB mode saturates retrieval (10 docs, ~9.4 cited of 10 —
support hit is nearly vacuous). The pool makes retrieval a real test:
every question is answered against all ~4600 paragraphs of all 500
questions, so distractors include the other questions' confusable text.
Random-chance double-gold-hit collapses from ~80% to <1%.
"""

from __future__ import annotations

import argparse
import json
import time
from pathlib import Path

from common import EvalClient

POLL_INTERVAL = 10
POLL_TIMEOUT = 7200


def load_manifest(path: str) -> list[dict]:
    with open(path, encoding="utf-8") as fh:
        return [json.loads(line) for line in fh if line.strip()]


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--manifest", required=True)
    ap.add_argument("--kb-name", default="hotpot-pool-500")
    args = ap.parse_args()

    questions = load_manifest(args.manifest)

    # dedup by title, longest text wins (same Wikipedia paragraph reused
    # across questions; occasionally trimmed variants exist)
    pool: dict[str, str] = {}
    for q in questions:
        for para in q["paragraphs"]:
            text = "".join(para["sentences"])
            if len(text) > len(pool.get(para["title"], "")):
                pool[para["title"]] = text
    titles = sorted(pool)
    print(f"unique paragraphs: {len(titles)} (raw {sum(len(q['paragraphs']) for q in questions)})")

    state_path = Path(args.manifest + ".pool-state.json")
    state: dict = {}
    if state_path.exists():
        state = json.loads(state_path.read_text())
    kb_id = state.get("kb_id")
    done_titles: set[str] = set(state.get("ingested", []))

    client = EvalClient()
    if not kb_id:
        kb_id = client.create_kb(args.kb_name)
        state = {"kb_id": kb_id, "ingested": []}
        state_path.write_text(json.dumps(state))
    print(f"pool kb: {kb_id} | already ingested: {len(done_titles)}")

    t0 = time.time()
    batch_start = time.time()
    for i, title in enumerate(titles):
        if title in done_titles:
            continue
        try:
            client.add_manual_knowledge(kb_id, title, pool[title])
            state["ingested"].append(title)
        except Exception as exc:  # noqa: BLE001
            print(f"[{i}] FAILED {title[:50]}: {str(exc)[:120]}")
            time.sleep(2)
        if (i + 1) % 50 == 0:
            state_path.write_text(json.dumps(state))
            rate = 50 / (time.time() - batch_start)
            batch_start = time.time()
            eta_min = (len(titles) - i - 1) / max(rate, 0.01) / 60
            print(f"[{i+1}/{len(titles)}] {rate:.1f}/s eta {eta_min:.0f}min "
                  f"(elapsed {(time.time()-t0)/60:.0f}min)")
    state_path.write_text(json.dumps(state))

    # wait until every doc reports completed
    print("waiting for indexing to complete ...")
    deadline = time.time() + POLL_TIMEOUT
    while time.time() < deadline:
        ok, total = check_pool(client, kb_id, len(titles))
        if ok:
            print("pool fully indexed")
            break
        print(f"  indexed check: {total}/{len(titles)} done")
        time.sleep(POLL_INTERVAL)

    # emit a run_eval-compatible ingest map: every qid -> pool kb
    ingest_path = Path(args.manifest + ".pool.ingest.jsonl")
    with open(ingest_path, "w", encoding="utf-8") as fh:
        for q in questions:
            fh.write(json.dumps({"qid": q["qid"], "kb_id": kb_id, "indexed": True}) + "\n")
    print(f"ingest map: {ingest_path}")


def check_pool(client: EvalClient, kb_id: str, expect: int) -> tuple[bool, int]:
    done = 0
    page = 1
    while True:
        items = client.list_knowledge_page(kb_id, page)
        if not items:
            break
        done += sum(
            1
            for it in items
            if it.get("parse_status") == "completed" and it.get("enable_status") == "enabled"
        )
        if len(items) < 100:
            break
        page += 1
    return done >= expect, done


if __name__ == "__main__":
    main()
