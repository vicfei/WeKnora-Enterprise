#!/usr/bin/env python3
"""Ingest one HotpotQA question = one KB with its 10 paragraphs.

Memory discipline: strictly sequential — create KB, add 10 manual
knowledges, poll until every paragraph finishes indexing, move on.
Writes ingest state to <manifest>.ingest.jsonl so interrupted runs resume.
"""

from __future__ import annotations

import argparse
import json
import time
from pathlib import Path

from common import EvalClient

DONE_STATES = {"active", "parsed", "completed", "ready"}
POLL_INTERVAL = 3
POLL_TIMEOUT = 600


def load_manifest(path: str) -> list[dict]:
    with open(path, encoding="utf-8") as fh:
        return [json.loads(line) for line in fh if line.strip()]


def paragraph_text(para: dict) -> str:
    return "".join(para["sentences"])


def wait_indexed(client: EvalClient, kb_id: str, expect: int) -> dict:
    """Manual knowledge rows are ready when parse_status=completed and
    enable_status=enabled (there is no plain `status` field)."""
    deadline = time.time() + POLL_TIMEOUT
    while time.time() < deadline:
        items = client.list_knowledge(kb_id)
        if len(items) >= expect and all(
            it.get("parse_status") == "completed" and it.get("enable_status") == "enabled"
            for it in items
        ):
            return {"ok": True, "statuses": ["completed/enabled"]}
        time.sleep(POLL_INTERVAL)
    return {"ok": False, "statuses": [f"{len(items)}/{expect} ready"]}


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--manifest", required=True)
    ap.add_argument("--kb-prefix", default="hotpot-eval")
    args = ap.parse_args()

    questions = load_manifest(args.manifest)
    state_path = Path(args.manifest + ".ingest.jsonl")
    done: dict[str, dict] = {}
    if state_path.exists():
        with open(state_path, encoding="utf-8") as fh:
            for line in fh:
                if line.strip():
                    rec = json.loads(line)
                    done[rec["qid"]] = rec

    client = EvalClient()
    with open(state_path, "a", encoding="utf-8") as out:
        for i, q in enumerate(questions):
            if q["qid"] in done and done[q["qid"]].get("kb_id"):
                print(f"[{i+1}/{len(questions)}] {q['qid']} already ingested, skip")
                continue
            t0 = time.time()
            try:
                kb_id = client.create_kb(f"{args.kb_prefix}-{q['qid']}")
                for para in q["paragraphs"]:
                    client.add_manual_knowledge(
                        kb_id, para["title"], paragraph_text(para)
                    )
                res = wait_indexed(client, kb_id, len(q["paragraphs"]))
                rec = {
                    "qid": q["qid"],
                    "kb_id": kb_id,
                    "indexed": res["ok"],
                    "statuses": res["statuses"],
                    "secs": round(time.time() - t0, 1),
                }
            except Exception as exc:  # noqa: BLE001 — record and continue
                rec = {"qid": q["qid"], "kb_id": "", "error": str(exc)[:200]}
            out.write(json.dumps(rec, ensure_ascii=False) + "\n")
            out.flush()
            print(f"[{i+1}/{len(questions)}] {q['qid']} -> {rec}")


if __name__ == "__main__":
    main()
