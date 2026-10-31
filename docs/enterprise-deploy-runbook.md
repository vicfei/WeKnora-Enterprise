# 企业部署升级跑本（Enterprise Deploy & Upgrade Runbook）

> **用途**：给"拿到离线镜像 tar、要在无外网（air-gapped）环境部署或升级 WeKnora-Enterprise"的运维者一份可逐步照敲的执行跑本。
> **姊妹篇**：[enterprise-kb-ops-playbook.md](https://github.com/vicfei/Awesome-RAG-Frontier/blob/main/docs/enterprise-kb-ops-playbook.md)（九节浓缩方法论，本篇是其"部署验收"一节的展开执行版）。
> **来源声明**：出自一次完整的企业内网两级落地（测试环境 + 生产环境、多个离线升级窗口实战），全部命令实测过；环境细节已脱敏为变量。标注〔实测〕的数字为当次观测值，供参考而非性能承诺。
>
> **变量约定**：`$COMPOSE_DIR` = compose 项目目录；`$STAGE_DIR` = tar 暂存目录；`$APP_PORT` / `$UI_PORT` = 后端 / 前端端口；`$NEW_APP` / `$NEW_UI` = 本次交付的新镜像 ID（`docker images` 里取）。命令以 root 或 docker 组用户执行。

## 〇、适用与前提

- **适用**：内网 / 离线环境（docker pull 不可用），镜像以 `docker save` 的 tar 交付；升级线为同 fork 系的零 schema 迁移版本（升级日志应出现 `Database is up to date`）。跨大版本（带 migrations）不适用本跑本的"零迁移"断言，先单独评估数据面。
- **前提**：现役部署以 compose 拉起，服务名 `app` / `frontend`（容器名形如 `WeKnora-app` / `WeKnora-frontend`）。**引用名一律以步骤一的 inspect 实测为准，不要靠读 compose 文件猜**〔实测：compose 文件里的镜像引用可能与实际运行态漂移〕。
- **数据面不动**：本跑本全程不碰数据库 / 向量库 / 缓存，只换应用层镜像指针。

## 一、升级前侦察（第一动作，两条命令定案现役状态）

```bash
docker inspect WeKnora-app --format 'APP-IMG {{.Config.Image}} {{.Image}}'
docker logs WeKnora-app 2>&1 | grep -m2 -E 'Current version|Database is up to date'
```

- 第一条：`.Config.Image` = **compose 引用名**（步骤三 tag 接线的目标）；`.Image` = **实际镜像 ID**（回滚锚点，立刻抄录）。
- 第二条：定案"上次成功升到的版本 + 迁移水位"。若两条日志都为空，说明容器近期重建过、日志已被截断——改查上一次升级窗口的工单记录。
- 同场顺手记录：`docker ps --format '{{.Names}}\t{{.Image}}'` 全量快照（多服务宿主上留证，事后核对无误伤）。

## 二、升级六步

### ① tar 落位 + sha256 双端校验

```bash
cd $STAGE_DIR && sha256sum -c SHA256SUMS
```

传输防御：U 盘 / 网络拷贝 / 截图转录都可能让哈希串缺字符，**终验口径 = 在目标节点本地比对、只回传 0/1**：

```bash
sha256sum app.tar.gz | awk '{print $1}' | grep -c '<交付侧给的期望值>'   # 输出 1 即过
```

### ② 加载镜像

```bash
docker load -i app.tar.gz && docker load -i ui.tar.gz
docker images | grep -E 'weknora'   # 记下 $NEW_APP / $NEW_UI
```

### ③ tag 接线（核心步骤：引用名不动，compose 零改动）

```bash
APPIMG=$(docker inspect WeKnora-app --format '{{.Config.Image}}')
UIIMG=$(docker inspect WeKnora-frontend --format '{{.Config.Image}}')
docker tag $NEW_APP "$APPIMG" && docker tag $NEW_UI "$UIIMG"
```

原理：compose 里的镜像引用名不变，新内容通过 tag 指针接入；旧镜像失去引用但仍留在 daemon 里 = **天然的回滚资产**。首次部署（无存量容器）则跳过 inspect，直接以 compose 文件里的引用名打 tag。

### ④ 重建容器

```bash
cd $COMPOSE_DIR
docker-compose up -d app frontend      # v2 环境写 docker compose；本地 DB 容器停用时加 --no-deps
```

**幂等空转 ≠ 重启**：输出里没有 `Recreating` / `Started` 字样就是没换容器〔实测两次踩中：命令"成功"但新镜像根本没生效〕。

### ⑤ 就绪验证（sleep 30 后三看）

```bash
docker logs WeKnora-app 2>&1 | grep -E 'Database is up to date|Server is running'
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:$APP_PORT/api/v1/health   # 期望 200
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:$UI_PORT/                 # 期望 200
```

新版本首启可能出现数十秒 chown（entrypoint 修正数据目录属主）——日志滚动、非挂死，等待即可〔实测秒级到分钟级〕。

### ⑥ 业务走查

登录 → 知识库列表加载 → 任一知识库检索问答一轮。升级窗口内重点核对：本版本新增功能入口可见、旧数据（知识库 / 标签 / 会话）不报"实体不存在"。

## 三、三级验收铁律

**核心认知：健康检查证明"活着"，不证明"换了"。** 三级递进：

| 级 | 判据 | 排除什么 |
|---|---|---|
| L1 活着 | 健康端点 200、横幅正常 | 只排除"起不来" |
| L2 同 daemon 锚 | `docker inspect WeKnora-app --format '{{.Image}}'` == `$NEW_APP` | 排除"标签对、内容错"（tag 打偏 / load 到别的镜像） |
| L3 跨 daemon 层指纹 | 见下 | 排除"同 ID 假象"与跨节点对账争议 |

L3 层指纹（同一 tar 在**不同 docker daemon load 会得到不同镜像 ID**——存储布局差异，跨节点交付时 ID 对不上不代表内容不同；层指纹才跨 daemon 不说谎〔实测两案〕）：

```bash
docker inspect $NEW_APP --format '{{len .RootFS.Layers}}'
docker inspect $NEW_APP --format '{{join .RootFS.Layers ","}}' | sha256sum
```

两个值与**构建侧随包交付的基准值**比对（层数 + join-sha）。注意：容器 inspect **没有** RootFS 键，必须 inspect 镜像；要从容器侧取证则先取 `.Image` 再 inspect 该 ID。防转录丢字的口径同步骤①：`... | sha256sum | grep -c <期望值>` 只回传 0/1。

## 四、回滚（一分钟口径）

1. **锚点先行**：步骤一抄录的旧镜像 ID（daemon 内 ID 稳定，即使失去具名 tag 也能引用）；若交付时带具名版本 tag 则一并记录。
2. 回滚 = tag 指针反向 + 重建，数据面零动作：

```bash
docker tag <旧app镜像ID> "$APPIMG" && docker tag <旧ui镜像ID> "$UIIMG"
cd $COMPOSE_DIR && docker-compose up -d app frontend
```

3. 预期耗时 ≈ 重建两个容器〔实测：从决定回滚到旧版本健康检查通过，分钟级〕。零迁移线的升降级是对称的——schema 未动过，旧版本起来即用。

## 五、坑集（全部实测）

| # | 坑 | 症状 | 解法 |
|---|---|---|---|
| 1 | `up -d` 幂等空转 | 命令成功但版本没换 | 看输出有无 Recreating/Started；或比对容器 `.Image` |
| 2 | 引用名靠猜 | tag 打到 compose 没引用的名字上 | 一律 `inspect --format '{{.Config.Image}}'` 实测 |
| 3 | compose v1/v2 连字符 | `docker-compose: command not found` 或反之 | 先探 `docker-compose version` 与 `docker compose version` 哪个在 |
| 4 | 跨 daemon 镜像 ID 漂移 | 构建侧与接收侧 ID 对不上 | 走 L3 层指纹对账，别用 ID 互判 |
| 5 | 哈希转录缺字符 | 双端各验各的都"对不上" | 节点本地 grep -c 比对，只回传 0/1 |
| 6 | 首启 chown 假挂死 | 新版本起来慢、日志滚 chown | 等待；超过 5 分钟再排查 |
| 7 | `--no-deps` 漏加 | 把停用的本地 DB 摆设容器也拉起来 | 多服务宿主上 up 精确点名 app frontend |

## 六、给交付侧的清单（反向要求）

要让接收方跑得通三级验收，交付包应含：app/ui 双 tar + `SHA256SUMS` + **L3 层指纹基准值**（层数 + join-sha256 各两条）+ 引用名说明（或注明"以接收侧 inspect 为准"）。体积优化技巧：多镜像单档 save 可去重共享层〔实测 8 镜像分档 1.8G → 单档 602M〕。
