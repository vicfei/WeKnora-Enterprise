#!/usr/bin/env python3
"""Run the time-aware eval: paired on/off arms over the chat API.

The time-aware entry point is query understanding — the OFF arm disables
rewrite (enable_rewrite=false), which is the faithful "feature off"
configuration: no TimeCondition is ever produced, retrieval is untouched.

Usage: run_eval.py [--arm on|off|both] [--out runs/]
"""
from __future__ import annotations

import argparse
import json
import re
import time
from pathlib import Path

import requests

BASE = "http://localhost:8080"
TENANT = 10011
USER = "hotpot-eval-runner"
KEY_FILE = "/home/ubuntu/WeKnora-portal-sim/.env"
TAG_RE = re.compile(r'<(?:kb|web)\b[^>]*/>')


def token() -> str:
    for line in open(KEY_FILE):
        if line.startswith("WEKNORA_PLATFORM_KEY="):
            k = line.split("=", 1)[1].strip().strip('"')
    r = requests.post(f"{BASE}/api/v1/identity/bridge",
                      headers={"X-API-Key": k, "X-Tenant-ID": str(TENANT)},
                      json={"uum_user_id": USER, "tenant_id": TENANT, "role": "admin", "username": USER},
                      timeout=30)
    return r.json()["data"]["token"]


def ask(h, kb_id, question, enable_rewrite):
    sid = requests.post(f"{BASE}/api/v1/sessions", headers=h, timeout=30,
                        json={"title": "tar-eval", "knowledge_base_ids": [kb_id]}).json()["data"]["id"]
    answer, cited = "", []
    with requests.post(f"{BASE}/api/v1/knowledge-chat/{sid}", headers=h, stream=True, timeout=240,
                       json={"query": question, "knowledge_base_ids": [kb_id],
                             "enable_rewrite": enable_rewrite, "disable_title": True}) as r:
        for raw in r.iter_lines(decode_unicode=True):
            if not (raw and raw.startswith("data:")):
                continue
            try:
                e = json.loads(raw[5:])
            except json.JSONDecodeError:
                continue
            chunk = e.get("content") or ""
            if chunk and e.get("response_type") in ("answer", "message", None):
                answer += chunk
            if e.get("response_type") == "references":
                for ref in e.get("knowledge_references") or []:
                    t = ref.get("knowledge_title")
                    if t:
                        cited.append(t)
    # short answers carry no inline tags; inline citations are the fallback
    inline = re.findall(r'doc="([^"]+)"', answer)
    return TAG_RE.sub("", answer).strip(), cited or inline


def run_arm(h, kb_id, arm, questions, out_path):
    enable_rewrite = arm == "on"
    with open(out_path, "w", encoding="utf-8") as out:
        for i, q in enumerate(questions):
            t0 = time.time()
            answer, cited = ask(h, kb_id, q["question"], enable_rewrite)
            rec = {"qid": q["qid"], "type": q["type"], "answer": answer,
                   "cited_docs": sorted(set(cited)), "secs": round(time.time() - t0, 1)}
            out.write(json.dumps(rec, ensure_ascii=False) + "\n")
            out.flush()
            print(f"[{arm} {i+1}/{len(questions)}] {q['qid']} {rec['secs']}s ans={answer[:40]!r}")


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--arm", default="both", choices=["on", "off", "both"])
    ap.add_argument("--out", default="runs")
    args = ap.parse_args()

    kb_id = open("kb.txt").read().strip()
    questions = [json.loads(l) for l in open("questions.jsonl", encoding="utf-8")]
    h = {"Authorization": f"Bearer {token()}", "X-Tenant-ID": str(TENANT)}
    out = Path(args.out)
    out.mkdir(exist_ok=True)

    arms = ["on", "off"] if args.arm == "both" else [args.arm]
    for arm in arms:
        run_arm(h, kb_id, arm, questions, out / f"tar-{arm}.jsonl")


if __name__ == "__main__":
    main()
