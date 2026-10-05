#!/usr/bin/env python3
"""台账 T13 轻量飞轮——把点踩反馈导出为黄金集草稿。

从管理列表 API（GET /api/v1/feedback?rating=down）拉取点踩，用快照里的
question / kb_ids / 模型错答产出 evals 骨架可直接消费的清单草稿：

    {"qid":"fb-<反馈id前12位>","question":"…","kb_ids":["…"],
     "model_wrong":"…","reason":"bad_citation","comment":"…","answer":""}

answer 留空——正确答案只能人工补（分诊步骤）。重复导出是增量合并：
已有人工 answer 的条目永不覆盖；反馈被删除的旧条目保留（黄金集一旦
入册就是测试资产，不随反馈生命周期消亡）。

用法：
    EVAL_TENANT_ID=10000 EVAL_USER=cnfeichen python3 export_golden.py \
        [--out golden.jsonl] [--reason bad_citation] [--all]

回归（补完 answer 后）：
    cd ../hotpot && EVAL_TENANT_ID=10000 EVAL_USER=cnfeichen \
    python3 run_eval.py --manifest ../feedback/golden.jsonl \
        --out ../data/runs/golden-r1.jsonl --label golden-r1 --lang raw
"""
from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path

import requests

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "hotpot"))
from common import BASE, TENANT_ID, EVAL_USER, _platform_key  # noqa: E402


def bridge_token() -> str:
    resp = requests.post(
        f"{BASE}/api/v1/identity/bridge",
        headers={"X-API-Key": _platform_key(), "X-Tenant-ID": str(TENANT_ID)},
        json={
            "uum_user_id": EVAL_USER,
            "tenant_id": TENANT_ID,
            "role": "admin",
            "username": EVAL_USER,
        },
        timeout=30,
    )
    resp.raise_for_status()
    return resp.json()["data"]["token"]


def fetch_down_feedback(token: str, reason: str = "") -> list[dict]:
    rows, page = [], 1
    headers = {"Authorization": f"Bearer {token}", "X-Tenant-ID": str(TENANT_ID)}
    while True:
        params = {"rating": "down", "page": page, "page_size": 100}
        if reason:
            params["reason_code"] = reason
        data = requests.get(f"{BASE}/api/v1/feedback", headers=headers,
                            params=params, timeout=30).json()
        batch = data.get("data") or []
        rows.extend(batch)
        total = data.get("total") or 0
        if page * 100 >= total or not batch:
            break
        page += 1
    return rows


def draft_entry(row: dict) -> dict | None:
    snap = row.get("snapshot") or {}
    question = (snap.get("question") or "").strip()
    if not question:
        return None  # 没抓到问题的反馈做不成测试用例
    return {
        "qid": f"fb-{row['id'][:12]}",
        "question": question,
        "kb_ids": snap.get("knowledge_base_ids") or [],
        "model_wrong": snap.get("answer_excerpt") or "",
        "reason": row.get("reason_code") or "",
        "comment": row.get("comment") or "",
        "answer": "",
    }


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--out", default=str(Path(__file__).parent / "golden.jsonl"))
    ap.add_argument("--reason", default="", help="只导出指定 reason_code 的点踩")
    args = ap.parse_args()

    token = bridge_token()
    rows = fetch_down_feedback(token, args.reason)
    print(f"fetched {len(rows)} down-feedback rows (tenant {TENANT_ID})")

    existing: dict[str, dict] = {}
    if Path(args.out).exists():
        for line in open(args.out, encoding="utf-8"):
            if line.strip():
                rec = json.loads(line)
                existing[rec["qid"]] = rec

    added = 0
    for row in rows:
        entry = draft_entry(row)
        if entry is None:
            continue
        prev = existing.get(entry["qid"])
        if prev is not None:
            # 保留人工成果；只刷新模型错答快照等机器字段
            prev.update({k: v for k, v in entry.items() if k != "answer"})
            if prev.get("comment") and entry["comment"]:
                prev["comment"] = entry["comment"]
            continue
        existing[entry["qid"]] = entry
        added += 1

    out = Path(args.out)
    with open(out, "w", encoding="utf-8") as fh:
        for qid in sorted(existing):
            fh.write(json.dumps(existing[qid], ensure_ascii=False) + "\n")
    filled = sum(1 for e in existing.values() if e.get("answer"))
    print(f"{out}: {len(existing)} entries ({added} new, {filled} answers filled, "
          f"{len(existing) - filled} awaiting triage)")


if __name__ == "__main__":
    main()
