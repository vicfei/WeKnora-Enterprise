#!/usr/bin/env python3
"""Ingest the synthetic corpus into a fresh KB via the manual API with
business_date, then wait for processing to complete.

Usage: ingest.py [--kb-name tar-eval] [--timeout 600]
"""
from __future__ import annotations

import argparse
import json
import time

import requests

BASE = "http://localhost:8080"
TENANT = 10011
USER = "hotpot-eval-runner"
KEY_FILE = "/home/ubuntu/WeKnora-portal-sim/.env"


def token() -> str:
    for line in open(KEY_FILE):
        if line.startswith("WEKNORA_PLATFORM_KEY="):
            k = line.split("=", 1)[1].strip().strip('"')
    r = requests.post(f"{BASE}/api/v1/identity/bridge",
                      headers={"X-API-Key": k, "X-Tenant-ID": str(TENANT)},
                      json={"uum_user_id": USER, "tenant_id": TENANT, "role": "admin", "username": USER},
                      timeout=30)
    return r.json()["data"]["token"]


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--kb-name", default="tar-eval-corpus")
    ap.add_argument("--timeout", type=int, default=900)
    args = ap.parse_args()

    h = {"Authorization": f"Bearer {token()}", "X-Tenant-ID": str(TENANT)}

    emb = requests.get(f"{BASE}/api/v1/models?type=embedding", headers=h, timeout=30).json()
    emb_id = (emb.get("data") or [{}])[0].get("id")
    kb = requests.post(f"{BASE}/api/v1/knowledge-bases", headers=h, timeout=30,
                       json={"name": args.kb_name, "description": "time-aware eval corpus (seeded)",
                             "type": "document", "embedding_model_id": emb_id}).json()
    kb_id = kb["data"]["id"]
    print("kb:", kb_id)

    docs = [json.loads(l) for l in open("corpus.jsonl", encoding="utf-8")]
    ids = []
    for d in docs:
        r = requests.post(f"{BASE}/api/v1/knowledge-bases/{kb_id}/knowledge/manual", headers=h,
                          timeout=60, json={"title": d["title"], "content": d["content"],
                                            "status": "publish", "business_date": d["business_date"] + "T00:00:00+08:00"})
        body = r.json()
        if not body.get("success"):
            print("INGEST FAIL", d["title"], str(body)[:160])
            return
        ids.append(body["data"]["id"])
    print("ingested:", len(ids))

    deadline = time.time() + args.timeout
    while time.time() < deadline:
        done = 0
        for i in range(0, len(ids), 30):
            batch = ",".join(ids[i:i + 30])
            rr = requests.get(f"{BASE}/api/v1/knowledge/batch?ids={batch}", headers=h, timeout=30).json()
            for k in (rr.get("data") or []):
                if k.get("parse_status") == "completed":
                    done += 1
        if done == len(ids):
            print("all completed")
            break
        print(f"  processed {done}/{len(ids)}")
        time.sleep(10)
    with open("kb.txt", "w") as f:
        f.write(kb_id)


if __name__ == "__main__":
    main()
