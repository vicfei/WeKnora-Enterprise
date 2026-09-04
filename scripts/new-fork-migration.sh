#!/usr/bin/env bash
# 创建 fork 自有迁移骨架：时间戳版本号 + up/down 一对空文件。
# 用法: scripts/new-fork-migration.sh <snake_case_name>
# 约定见 migrations/FORK-MIGRATIONS.md（不要用 make migrate-create，那是顺序编号）。
set -euo pipefail

name="${1:?用法: new-fork-migration.sh <snake_case_name>}"
if ! [[ "$name" =~ ^[a-z][a-z0-9_]*$ ]]; then
    echo "错误: 迁移名必须是 snake_case（小写字母/数字/下划线）: $name" >&2
    exit 1
fi

version="$(date -u +%Y%m%d%H%M%S)"
dir="$(cd "$(dirname "$0")/.." && pwd)/migrations/versioned"

for kind in up down; do
    f="$dir/${version}_${name}.${kind}.sql"
    if [[ -e "$f" ]]; then
        echo "错误: 文件已存在: $f（一秒内重复创建？重试即可）" >&2
        exit 1
    fi
    {
        echo "-- fork migration ${version}_${name} (${kind})"
        echo "-- 描述:"
    } > "$f"
    echo "created $f"
done
