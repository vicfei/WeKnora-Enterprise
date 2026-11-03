# WeKnora Fork 版本血统总账

> 本文是 fork 所有版本的**权威基线记录**，每次发新版必须更新此表并推送。
> 记录维度：上游代码基线、Docker 底座镜像、git tag、docker tag、commit、版本内容、上游同步量。

## 总表

| git tag | commit | 上游代码基线 | 上游同步量 | Docker 底座 | docker tag | 版本内容 | 构建日期 |
|---|---|---|---|---|---|---|---|
| `v2` | `a8d43567` | v0.7.2 后 3 提交 (cc690bbe7) | 0(fork 起点) | latest (Aug-07) | `v2-fork` | portal 聚合 + grants + bridge + sandbox/skill + 标签树 | 2026-09-03 |
| `v3` | `bb5b37d0` | **v0.8.0** (1edcd54b, WIP 合入 c32b615a2) | 含 v0.8.0 | latest (Aug-07) | `fork-v3` | 安全加固 + Bocha 搜索 + evals 骨架 + MCP 开关 | 2026-09-05 |
| `v3.1` | `0ae30f9d` | **v0.8.0** 经 fork-v3 | 同 v3 | **v0.8.0-base** | `fork-v3.1` | 应急回移植:bocha + doubao 搜索(DMZ 适配) | 2026-10-06 |
| `v3.1.1` | `80a950ad` | **v0.8.0** 经 fork-v3 | 同 v3 | v0.8.0-base | `fork-v3.1.1` | P1 UI 文本 + P2 网页配额 + P4 测试端点 | 2026-10-07 |
| `v3.1.2` | `5b2545f3` | **v0.8.0** 经 fork-v3 | 同 v3 | v0.8.0-base | `v0.8.0-fork-v3.1.2` | 标签树 2.0 + bocha/doubao 完善 + 打标折叠箭头修复 | 2026-10-16 |
| `v3.1.3` | `f4d7f3e4` | **v0.8.0** 经 fork-v3 | 同 v3 | v0.8.0-base | `v0.8.0-fork-v3.1.3` | 删除影响预览 + UM 审计链(X-External-User-ID) | 2026-10-27 |
| `v4` | `cab16dca` | v0.8.0 后快照 e3ac49ca | **+154** | **v0.8.0-base**(首次显式) | `v0.8.0-fork-v4` | BrowserSkill + 知识卡检 + time-aware M1-M3 + 154 上游 | 2026-09-21 |
| `v5` | `3d41706` | **v0.8.2** (3e8b0bfc) + 102 提交至 bccb4b15 | **+102 after v0.8.2** | **v0.8.2-base** | `v0.8.2-fork-v5` | T1-T8 + KB 路由 v2 + sandbox/skill 完善 | 2026-10-05 |
| `v5.1` | `b65020a4` | **v0.8.2** + 102(同 v5) | 同 v5 | v0.8.2-base | `v0.8.2-fork-v5.1` | v5 + T9-T13(时间治理/版本管理/元数据/反馈闭环) | 2026-10-06 |

## Docker 底座三代

| 底座 | 使用版本 | 说明 |
|---|---|---|
| `latest` (2026-08-07 构建) | v2, v3 | 旧版,代码≈v0.7.2 时代;config 全量过期 |
| `v0.8.0-base` | v3.1 → v3.1.3, v4 | 显式对齐;v4 起首次升级(README:"旧底座 entrypoint 含已废弃的 skills 合并逻辑") |
| `v0.8.2-base` | v5, v5.1 | 二次升级 |

## 两条分支线

| 分支 | 版本线 | 说明 |
|---|---|---|
| `main` | v2 → v3 → v4 → v5 → v5.1 → … | 主线开发,追上游最新 |
| `backport/v3.1-bocha-doubao` | v3.1 → v3.1.1 → v3.1.2 → v3.1.3 | 生产兼容支线(v0.8.0 底座不变),.109 生产线 |

## 上游合并点

| 合并 commit | 合入的上游版本 | 说明 |
|---|---|---|
| `c32b615a2` | v0.8.0 tip (1edcd54b) | WIP 合并,v3 首次包含 v0.8.0 |
| `f57dbf7ee` | e3ac49ca (v0.8.0 后 154 提交) | v4 的上游大同步 |
| `9984bd295` | v0.8.2 (3e8b0bfc) | v5 的上游 tag 合入 |
| `2dc4d042a` | bccb4b15 (v0.8.2 后 102 提交) | v5 的 plus102 同步 |

## 发版清单(铁律)

每次发新版本,按以下步骤操作:

1. [ ] 在对应分支上 commit
2. [ ] 打 git tag(带上游基线注释,含:上游版本/commit hash/Docker 底座/版本内容/构建日期)
3. [ ] 推送分支:`git push kbpaamc <branch>`
4. [ ] 推送 tag:`git push kbpaamc <tag>`
5. [ ] 更新本文件的总表(新增一行)
6. [ ] 提交并推送本文件
7. [ ] 构建 Docker 镜像(bookworm 容器内编译 + staging 组装 + docker build)
8. [ ] docker tag 命名规则:`<上游版本>-fork-<fork版本>`(如 v0.8.0-fork-v3.1.3)
9. [ ] docker save → tar.gz → sha256sum
10. [ ] 更新 fork-image-build 台账(sha256 + BACKPORT-NOTES)
11. [ ] 组装 WSL2 交付包(rsync + :5173 双通道)
