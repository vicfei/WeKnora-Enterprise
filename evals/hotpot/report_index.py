#!/usr/bin/env python3
"""Build the evergreen report index (eval-report.html).

Each published report is its own permanent file (report-<slug>.html);
the index links them with date, question set and headline numbers.
History is never overwritten — old slugs stay reachable, and every
backing JSONL is version-controlled under evals/data/runs/."""

from __future__ import annotations

import datetime
from pathlib import Path

REPORTS_DIR = Path(__file__).resolve().parent.parent / "reports"
PUBLISH_DIR = Path("/home/ubuntu/WeKnora/frontend/dist")

# slug -> (title, source html in reports/, date, summary line)
INDEX = [
    ("routing-v11-100", "知识库路由 v1.1 评估（100 题）", "routing-v11-100.html",
     "2026-09-10", "路由 top1=96% / top3=97%；答案质量与 oracle 持平（EM +1.0 / F1 −1.3，噪声带内）"),
    ("pool-vs-small-500", "共享池 vs 小库 终版基线（500 题配对）", "pool-vs-small-v2.html",
     "2026-09-10", "EM 48.6/50.6，金标命中 84.2/95.0 —— 池模式创造检索层区分度"),
    ("baseline-500-smallkb", "小库模式基线（500 题）", "baseline-500.html",
     "2026-09-09", "EM 50.6 / F1 68.5 / 金标全命中 95.0%（答案侧首版基线）"),
    ("pilot-20", "试点评估（20 题·含中文回答对照）", "pilot-20.html",
     "2026-09-09", "语言失配对照：EM 0→50 / F1 6.9→66（英文短答指令的必要性实证）"),
]

CSS = """
body{font-family:-apple-system,'PingFang SC','Microsoft YaHei',sans-serif;margin:0;background:#f6f7f9;color:#1c1e21}
.wrap{max-width:900px;margin:0 auto;padding:28px 20px 60px}
h1{font-size:21px} .meta{color:#667;font-size:13px;margin-bottom:20px}
a.card{display:block;background:#fff;border:1px solid #e3e6ea;border-radius:12px;padding:16px 20px;margin:12px 0;
text-decoration:none;color:inherit;transition:box-shadow .15s}
a.card:hover{box-shadow:0 2px 10px rgba(0,0,0,.08)}
.card .t{font-weight:600;font-size:15px} .card .d{color:#889;font-size:12px;margin-top:2px}
.card .s{font-size:13px;color:#335;margin-top:6px}
.note{background:#fff;border:1px dashed #c8ccd2;border-radius:12px;padding:12px 16px;font-size:13px;color:#556;margin-top:24px}
"""


def main() -> None:
    REPORTS_DIR.mkdir(parents=True, exist_ok=True)
    PUBLISH_DIR.mkdir(parents=True, exist_ok=True)
    now = datetime.datetime.now().strftime("%Y-%m-%d %H:%M")
    rows = []
    for slug, title, src, date, summary in INDEX:
        src_path = REPORTS_DIR / src
        if not src_path.exists():
            continue
        # permanent per-report URL
        (PUBLISH_DIR / f"report-{slug}.html").write_text(src_path.read_text(encoding="utf-8"), encoding="utf-8")
        rows.append(
            f'<a class=card href="report-{slug}.html"><div class=t>{title}</div>'
            f'<div class=d>{date} ｜ evals/reports/{src} ｜ /report-{slug}.html</div>'
            f'<div class=s>{summary}</div></a>'
        )
    html = (
        "<!doctype html><html lang=zh><meta charset=utf-8>"
        f"<title>WeKnora 评估报告索引</title><style>{CSS}</style><div class=wrap>"
        "<h1>WeKnora 评估报告索引</h1>"
        f'<div class=meta>更新 {now} ｜ 每份报告一版一 URL，永不覆盖；原始跑批数据版本化于 evals/data/runs/</div>'
        + "".join(rows)
        + '<div class=note>历史中间版本（如 384 题池评估当期版）未单独留页，但其数据完整保留于 runs/，可随时用 make_report.py 复现渲染。</div>'
        "</div></html>"
    )
    (PUBLISH_DIR / "eval-report.html").write_text(html, encoding="utf-8")
    print(f"index: {PUBLISH_DIR / 'eval-report.html'} | reports: {len(rows)}")


if __name__ == "__main__":
    main()
