#!/usr/bin/env python3
"""Score time-aware runs: EM per question type, window precision, and the
compliance metric — stale-citation rate (out-of-window doc cited).

Window precision: of the docs a run cited, the fraction inside the gold
window. Stale citation: any cited doc whose month is outside the window AND
the answer does not carry that doc's date (design §5; target 0).
"""
from __future__ import annotations

import argparse
import json
import re
from collections import defaultdict

MONTH_RE = re.compile(r"(\d{4})年(\d{1,2})月")


def load(path):
    with open(path, encoding="utf-8") as f:
        return {json.loads(l)["qid"]: json.loads(l) for l in f}


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--run", required=True)
    ap.add_argument("--baseline", default="")
    args = ap.parse_args()

    qs = load("questions.jsonl")
    run = load(args.run)
    base = load(args.baseline) if args.baseline else None

    doc_month = {}
    for l in open("corpus.jsonl", encoding="utf-8"):
        d = json.loads(l)
        doc_month[d["title"]] = d["month"]

    stats = defaultdict(lambda: {"em": 0, "n": 0, "prec_sum": 0.0, "prec_n": 0, "stale": 0})
    for qid, q in qs.items():
        r = run.get(qid)
        if not r:
            continue
        s = stats[q["type"]]
        s["n"] += 1
        if r["answer"].strip() == q["gold_answer"]:
            s["em"] += 1
        cited = [c for c in r.get("cited_docs", []) if c in doc_month]
        if cited:
            in_win = [c for c in cited if doc_month[c] in set(q["gold_months"])]
            s["prec_sum"] += len(in_win) / len(cited)
            s["prec_n"] += 1
            for c in cited:
                if doc_month[c] not in set(q["gold_months"]):
                    # stale unless the answer itself spells out that month
                    anchor = f"{doc_month[c][:4]}年{int(doc_month[c][5:7])}月"
                    if anchor not in r["answer"]:
                        s["stale"] += 1
                        break

    def row(name, s):
        em = 100.0 * s["em"] / s["n"] if s["n"] else 0
        prec = 100.0 * s["prec_sum"] / s["prec_n"] if s["prec_n"] else 0
        stale = 100.0 * s["stale"] / s["n"] if s["n"] else 0
        return em, prec, stale

    print(f"{'type':<18}{'n':>4}{'EM%':>8}{'winPrec%':>10}{'staleCite%':>12}")
    tot = defaultdict(float)
    for t in ("explicit_window", "as_of", "implicit_recency"):
        s = stats[t]
        if not s["n"]:
            continue
        em, prec, stale = row(t, s)
        tot["n"] += s["n"]; tot["em"] += s["em"]; tot["stale"] += s["stale"]
        tot["prec_sum"] += s["prec_sum"]; tot["prec_n"] += s["prec_n"]
        print(f"{t:<18}{s['n']:>4}{em:>8.1f}{prec:>10.1f}{stale:>12.1f}")
    em = 100.0 * tot["em"] / tot["n"] if tot["n"] else 0
    prec = 100.0 * tot["prec_sum"] / tot["prec_n"] if tot["prec_n"] else 0
    stale = 100.0 * tot["stale"] / tot["n"] if tot["n"] else 0
    print(f"{'TOTAL':<18}{int(tot['n']):>4}{em:>8.1f}{prec:>10.1f}{stale:>12.1f}")

    if base:
        print("\n=== vs baseline ===")
        for t in ("explicit_window", "as_of", "implicit_recency"):
            a, b = stats[t], None
            bq = [q for qid, q in qs.items() if qid in base and q["type"] == t]
            bem = sum(1 for q in bq if base[q["qid"]]["answer"].strip() == q["gold_answer"])
            if a["n"] and bq:
                print(f"{t}: EM {100.0*bem/len(bq):.1f} -> {100.0*a['em']/a['n']:.1f}")


if __name__ == "__main__":
    main()
