# Docker 内的 codebase-memory-mcp

在现有 WeKnora 应用镜像上安装官方 Linux portable 发行包，默认固定为
`0.12.1`，支持 amd64 / arm64，并校验发行包的 SHA-256。源码及索引操作
使用应用已有的 `appuser`，无需开放额外端口。

## 工具安装 ≠ 业务集成

本文档描述的是**工具安装**：把 `codebase-memory-mcp` 可执行文件装进
镜像并挂载缓存卷。**业务集成**由应用内的 Code Wiki 功能完成（见
[CODE_WIKI_IMPLEMENTATION_PLAN.md](CODE_WIKI_IMPLEMENTATION_PLAN.md) 与
`internal/codebase/`）：

- 创建 `code_wiki` 类型知识库后，在"代码仓库"面板配置 GitLab 连接并
  绑定仓库；后端按固定 commit 下载归档、过滤解包为不可变快照，再调用
  本 CLI 建立/校验分析产物。
- 内置 Provider 通过 `WEKNORA_CODEBASE_CLI_PATH` 定位可执行文件（默认
  `codebase-memory-mcp`，即本镜像安装的版本）；开发机自装时指向本地
  路径并对齐版本。
- 快照根目录默认 `/data/code-wiki`（可用 `WEKNORA_CODE_WIKI_DATA_DIR`
  覆盖）；归档解包与文件数量上限可用
  `WEKNORA_CODE_WIKI_MAX_ARCHIVE_MB` / `WEKNORA_CODE_WIKI_MAX_UNPACKED_MB` /
  `WEKNORA_CODE_WIKI_MAX_FILES` 调整。
- 选择自定义 MCP Provider 时不依赖本 CLI；远程服务如何获得代码见计划
  §4.3（共享目录 / 导入 / 绑定外部索引）。

## 构建和启动

```bash
docker compose build app
docker compose up -d --no-deps app
```

主 `docker-compose.yml` 已包含安装和缓存卷配置，无需额外的 Compose 文件。
构建阶段需要访问 GitHub 和 Debian 软件源；运行时 `.env` 内的代理配置
不会自动应用到 Docker build，需要时通过标准构建参数传递代理。

## 验证和使用

### 使用手动下载的安装包

将官方 Linux portable 安装包（不解压）及同版本 `checksums.txt` 放在项目的
`.local-data/codebase-memory/` 下，然后执行：

```bash
docker compose build \
  --build-arg CODEBASE_MEMORY_PACKAGE_SOURCE=local app
docker compose up -d --no-deps --no-build app
```

安装时会检查架构、SHA-256 和版本号。此选项跳过 GitHub 下载；首次安装 Git
仍需要 Debian 软件源，已有构建缓存时可复用。若希望后续构建始终使用本地包，
可在 `.env` 设置 `CODEBASE_MEMORY_PACKAGE_SOURCE=local`。

### 命令行验证

```bash
docker compose exec -T --user appuser app codebase-memory-mcp --version
docker compose exec -T --user appuser app codebase-memory-mcp cli list_projects
```

将仓库放在容器的 `/data/files/code-repositories/<repository>`，然后执行：

```bash
docker compose exec -T --user appuser app codebase-memory-mcp cli index_repository \
  --repo-path /data/files/code-repositories/<repository> --name <project>
docker compose exec -T --user appuser app codebase-memory-mcp cli get_architecture \
  --project <project>
```

仓库使用已有 `data-files` 数据卷；索引缓存使用独立的
`codebase-memory-cache` 数据卷，重建容器后仍保留。CLI 的仓库路径必须是
容器路径，宿主机索引不会自动迁入。不要将 GitLab Token 写进仓库 URL 或
提交到代码中。

## 开发环境

`docker-compose.dev.yml` 的前后端在宿主机运行，代码分析由独立的
`codebase-memory` 工具容器提供，复用同一个扩展镜像：

```bash
docker compose -f docker-compose.dev.yml up -d --no-deps codebase-memory
docker compose -f docker-compose.dev.yml exec -T --user appuser codebase-memory \
  codebase-memory-mcp --version
docker compose -f docker-compose.dev.yml exec -T --user appuser codebase-memory \
  codebase-memory-mcp cli index_repository --repo-path /workspace --name WeKnora
```

需要构建镜像时，可运行 `docker compose -f docker-compose.dev.yml build
codebase-memory`；使用手动下载包时追加
`--build-arg CODEBASE_MEMORY_PACKAGE_SOURCE=local`（放在服务名之前）。

当前仓库只读挂载到 `/workspace`，克隆的其他仓库可存放于
`/data/files/code-repositories`。开发索引与仓库分别使用
`codebase-memory-cache-dev`、`codebase-repositories-dev` 数据卷，与主应用隔离。
此服务运行 stdio MCP 程序，通过上述 CLI 命令使用，不提供 HTTP 接口；
本机的 `make dev-app` 暂不自动调用它。

此配置安装 CLI 和默认 stdio MCP 服务程序。当前 WeKnora 禁用 stdio MCP
接入，因此不能直接把此程序填进设置页当成 HTTP/SSE 服务。此处未开放
HTTP MCP 端点，也未实现 GitLab 全量代码导入或 code-wiki 文档生成任务；
这些功能需要后续接入。

官方项目：https://github.com/DeusData/codebase-memory-mcp
