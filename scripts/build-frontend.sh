#!/usr/bin/env bash
# Fork 前端构建入口（二开资产之一）。
# 固化两件血泪教训：
#   1. 六个二开 flag 必须同时开启（TAG_TREE/PORTAL_LIST/BRIDGE_LOGIN/KB_ROUTING/WEIGHTED_RANKING/DOC_VERSIONS）；
#   2. 本机 3.6G 内存，vite build 必须 4G 堆，否则 OOM。
# 构建后自动恢复 eval 报告页（dist 是全新目录，9 份报告会被清掉）。
set -euo pipefail
cd "$(dirname "$0")/../frontend"
export VITE_ENABLE_TAG_TREE=1 \
       VITE_ENABLE_PORTAL_LIST=1 \
       VITE_ENABLE_BRIDGE_LOGIN=1 \
       VITE_ENABLE_KB_ROUTING=1 \
       VITE_ENABLE_WEIGHTED_RANKING=1 \
       VITE_ENABLE_DOC_VERSIONS=1
export NODE_OPTIONS=--max-old-space-size=4096
npm run build
cd ..
python3 evals/hotpot/report_index.py
echo "done: dist rebuilt (5 flags, 4G heap) + eval reports restored"
