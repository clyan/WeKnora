# Code Wiki 多仓库知识库改造计划

日期：2026-10-21。状态：设计计划，尚未实现业务功能。

## 1. 目标与首版范围

新增 `code_wiki` 知识库类型：**一个知识库对应一个客户项目，一个知识库关联该客户的多个 GitLab 前端、后端和公共服务仓库**。仓库是分析输入，客户级 Wiki 是主要产物。

用户流程：创建 Code Wiki → 配置 GitLab 连接 → 选择多个仓库、分支和角色 → 同步并索引 → 生成客户级文档 → 阅读文档并定位代码证据 → 后续同步与更新。

首版交付：

- 多仓库管理，支持前端、后端、公共模块等角色及目录过滤；通过多个连接支持不同 GitLab 实例或凭据。
- 固定 commit 的代码快照、codebase-memory-mcp 索引和同一知识库内的跨仓库分析。
- 客户级系统概览、架构、前后端交互、业务流程，以及逐仓库模块、接口、依赖和启动说明。
- 异步任务、进度、取消、失败重试、生成记录；失败时继续展示上一份已发布文档。
- 文档引用包含仓库、commit、文件和行号；有依据才能描述调用关系，无法确认的关系明确标注。
- 复用 Wiki 阅读、编辑、目录和历史版本能力，保护人工修改。

首版先实现手动同步与全量生成。自动增量重生成、GitLab Webhook、直接代码问答、代码语义向量检索安排在后续阶段。生成文档只要求可用的大模型，不强制配置 Embedding。

“客户”在首版是知识库的业务归属，不新增客户主数据系统。访问权限继承 WeKnora 的租户、工作空间、知识库及分享规则；如果要给不同客户独立账号并严格隔离，必须同时配置对应工作空间和权限，不能仅靠知识库名称实现。

## 2. 探索依据与现状

本次使用本机已建好的 `WeKnora` 图索引，结合函数调用追踪和源码核验。索引生成时间为 `2026-10-21T03:41:45Z`，记录 57,867 个节点、291,347 条边。对 37 个依据文件完成覆盖检查；业务文件未记录覆盖问题，三个 Docker 相关文件存在变更或未跟踪，已直接读取当前源码。

索引是辅助证据，不保证完整性；动态注册调用、跨语言调用及部分语法结构仍需源码确认。本计划没有把图中候选关系直接当成业务事实。

| 已有能力 | 当前实现与限制 | 改造结论 |
| --- | --- | --- |
| 知识库类型与能力 | [knowledgebase.go](../internal/types/knowledgebase.go) 有 document、faq、wiki 常量；Wiki 是否启用由 `IndexingStrategy.WikiEnabled` 决定；[前端创建 API](../frontend/src/api/knowledge-base/index.ts) 类型为 document/faq | 新增 code_wiki，并统一后端类型校验、能力判断和前端入口，不能只添加一个单选项 |
| GitLab 多仓库选择 | [types.go](../internal/datasource/connector/gitlab/types.go) 已支持 `settings.projects[]`，含 project_id/ref/paths | 复用连接与选择交互，增加仓库角色、快照和索引状态 |
| GitLab 同步 | [connector.go](../internal/datasource/connector/gitlab/connector.go) 按文件生成 FetchedItem，`isSupportedFile` 使用文档类扩展名名单 | 导入代码需要独立流程，扩大文件名单不足以实现 Code Wiki |
| 文档入库链路 | [datasource_service.go](../internal/application/service/datasource_service.go) 的 `ProcessSync → processSyncStreaming → applyFetchedItem → ingestItem` 最终进入文件/URL 文档入库 | 在 ProcessSync 的连接器抓取前，按知识库类型分流到代码快照任务 |
| Wiki 生成 | [wiki_ingest_batch.go](../internal/application/service/wiki_ingest_batch.go) 的 ProcessWikiIngest/mapOneDocument 以 knowledge_id 和 chunks 为输入 | 新增代码分析与生成服务，不套用逐文档抽取流程 |
| Wiki 存储与编辑 | [wiki_page.go](../internal/types/wiki_page.go)、[页面服务](../internal/application/service/wiki_page.go)、[页面仓储](../internal/application/repository/wiki_page.go) 已有页面、版本、乐观锁、编辑和回退 | 复用已发布页面；新增代码证据、生成批次和发布事务 |
| Wiki 来源与历史 | SourceRefs/ChunkRefs 指向文档；WikiPageRevision 保存正文等字段，但没有完整来源元数据 | 代码引用单独建模，并绑定页面版本；旧版本引用不能指向新分支 |
| 异步任务 | [task.go](../internal/types/task.go)、[任务路由](../internal/router/task.go) 已有 Asynq、同步任务和独立 Wiki worker pool | 复用调度机制，增加代码任务类型、队列归属、恢复与取消逻辑 |
| 模型限流 | [concurrency_wrapper.go](../internal/models/chat/concurrency_wrapper.go)、[governor.go](../internal/models/limiter/governor.go) 已有限流，后台任务按 model ID 协调 | 生成调用走后台上下文；默认串行，补充 429 延迟重试和预算 |
| 页面展示 | [WikiBrowser.vue](../frontend/src/views/knowledge/wiki/WikiBrowser.vue) 已有 Wiki 阅读与编辑，但来源点击打开文档，页面类型也有过滤逻辑 | 复用阅读界面，扩展代码来源与 Code Wiki 文档分类 |
| Docker 工具 | [安装说明](CODEBASE_MEMORY_DOCKER.md)、[Dockerfile](../docker/Dockerfile.app-codebase)、[主 compose](../docker-compose.yml)、[开发 compose](../docker-compose.dev.yml) 已接入 CLI 安装和缓存 | 工具安装已具备；应用调用、仓库快照、权限和生成流程仍需开发 |

另一个需要修正的细节：现有 GitLab 同步会读取 HEAD SHA，但后续部分文件请求使用分支 ref。代码快照必须使用已解析的 commit SHA 获取内容，避免同步过程中分支推进造成混合版本。

## 3. 领域模型与配置归属

建议关系：`租户 → Code Wiki 知识库 → 多个仓库绑定 → 多个不可变快照`；每次生成冻结一份“仓库—快照”清单，产出一批文档与证据。

### 3.1 知识库与连接

- 新增 `KnowledgeBaseTypeCodeWiki = "code_wiki"` 和独立 `CodeWikiConfig`，配置代码分析、语言、文档目录和资源预算；生成模型和自定义指令复用 `WikiConfig`，不维护两套相同设置。
- Wiki 能力开启以复用阅读界面；创建和默认化逻辑允许关闭向量/关键词文档索引，不能因 Wiki 开启而把源码送入文档 chunks 流程。
- `DataSource` 继续保存 GitLab 连接、加密凭据、定时同步配置及同步日志；不另存明文 Token。
- 一个仓库绑定只能引用同租户、同知识库下的 GitLab 数据源；跨知识库绑定返回权限错误。
- 仓库列表以新增的仓库绑定表为唯一事实来源。代码模式下不再同时维护一套 `settings.projects[]`；普通文档数据源继续使用原设置。
- 为数据源增加明确的代码同步目标标识，并按 KB 类型校验。调整 GitLab 配置校验：文档模式仍校验 projects，代码模式从绑定表获取项目集合。

### 3.2 建议数据表

以下为逻辑模型，字段和索引在第一阶段定稿；所有查询必须携带 tenant_id、knowledge_base_id。

| 表 | 主要用途与字段 |
| --- | --- |
| `code_wiki_repositories` | 仓库绑定：source_id、GitLab project_id、展示名、角色、ref、include_paths、exclude_patterns、enabled、状态；唯一约束覆盖 KB、source、project、ref |
| `code_wiki_snapshots` | 不可变输入：repository_id、commit_sha、过滤配置哈希、引擎版本、图项目标识、本地路径、覆盖摘要、索引状态与错误 |
| `code_wiki_runs` | 生成批次：配置版本、状态、当前阶段、模型、进度、预算消耗、错误、取消标记、幂等键、执行租约 |
| `code_wiki_run_repositories` | 冻结本轮参与仓库及 snapshot_id；明确排除项、原因；生成过程中不跟随最新分支变化 |
| `code_wiki_run_pages` | 发布前暂存文档：稳定页面标识、正文、类别、证据、发布基线版本、校验结果 |
| `code_wiki_page_evidence` | 页面版本引用：page_id/version、run_id、snapshot_id、path、起止行、符号、证据类型；历史版本仍能解析原引用 |

KB 配置或关联状态保存 `active_run_id`，表示当前发布的生成批次。`wiki_pages` 仍承载可阅读、可编辑的页面；新增表负责代码分析输入和生成过程，不重建另一套 Wiki 页面系统。

仓库改名不改变内部 ID；仓库 ref、目录过滤改变需产生新快照。删除绑定先停止新任务并标记相关文档过期，历史快照按引用与保留策略回收，不立即破坏旧文档引用。

## 4. 代码导入和 codebase-memory 接入

### 4.1 同步流程

1. 校验 KB 权限与类型，读取该连接下启用的仓库绑定。
2. 将每个 ref 解析为 commit SHA，记录访问失败和分支不存在等具体错误。
3. 使用固定 SHA 下载仓库归档；复用现有 GitLab 认证、URL 校验和受控 HTTP 客户端，新增归档下载能力。
4. 在暂存目录解包，限制压缩包大小、文件数量和解压总量；拒绝路径穿越与越界链接。
5. 按目录和文件规则过滤，保留源码、路由、接口定义、构建配置及必要说明。默认排除 node_modules、dist、构建缓存、生成物、二进制及敏感文件；供应商代码是否包含由规则决定。
6. 原子落地不可变快照，调用 codebase-memory 建立仓库索引，记录引擎版本、覆盖范围和未解析文件。
7. 所有参与仓库准备就绪后，执行限定在本 KB 仓库集合内的跨仓库分析；更新同步日志。

解析 package.json、go.mod 等清单只用于提取依赖和启动线索；不运行仓库提供的安装脚本、构建命令或 hooks。

网络请求分离连接超时、下载超时与任务总超时。大仓库下载/索引进入后台；HTTP 请求只负责提交任务，避免把整个分析过程放进“测试连接”或同步请求。原有企业内网 GitLab 的网络、代理和 SSRF 配置必须沿用并实测。

### 4.2 应用适配层

建议新增 `internal/codebase/`，向业务层提供以下受限接口：

- 建立快照索引、读取索引状态与覆盖摘要。
- 查询仓库结构、符号、调用关系和指定源码片段。
- 对明确列出的本 KB 项目执行跨仓库分析。
- 清理无引用快照及对应索引。

首版通过固定可执行文件的 CLI/JSON 调用接入，使用 `exec.CommandContext` 的参数数组，不拼接 shell 命令。限制执行时间、并发、输出量，支持取消并清理子进程；错误转换成可展示的业务状态。预留接口以便后续改用常驻 worker。

这是对 codebase-memory-mcp 引擎能力的应用集成，不依赖将其伪装成 HTTP MCP 服务，也不开放用户任意 stdio 命令。已有通用 MCP 客户端无需为此解除限制。

### 4.3 索引隔离与版本：开发前必须验证

- 每个索引项目使用服务端生成的 tenant/KB/repository/snapshot 标识；不直接使用仓库名，避免重名覆盖。
- 图查询和跨仓库目标由服务端从冻结清单生成，禁止全项目通配和用户任意图查询。
- 优先为不同 KB 使用隔离的索引存储目录。**当前尚未验证引擎支持怎样指定独立缓存根目录**；阶段 0 必须用实际版本验证，不能先假设某个环境变量有效。
- 如果引擎不能可靠隔离缓存，采用独立执行环境/worker 存储，确认隔离后再接入多租户；仅项目命名隔离不足以代替权限控制。
- 本机探索工具为 0.11.0，Docker 安装目标为 0.12.1。对实际使用的 CLI 参数、JSON 格式、跨仓库模式、并发写入和快照目录要求做兼容性验证；生产固定版本，不隐式自动升级。
- 若归档目录不满足索引器要求，验证是否需要初始化本地 Git 元数据；不能因此改变 GitLab commit 的来源标识，也不执行仓库代码。

### 4.4 Docker 与开发运行方式

主环境继续整合在现有 `docker-compose.yml`；开发环境同步修改 `docker-compose.dev.yml`，不增加单独的 codebase compose 文件。

- 建议代码快照放在独立持久卷，例如 `/data/code-wiki`，按 tenant/KB 分目录；索引缓存与快照分别管理，不混用普通文档存储策略。
- 当前 Dockerfile 基于已发布 app 镜像扩展，只安装工具；实现新的 Go 代码后，必须构建包含新后端的应用镜像，不能继续依赖原上游 app 二进制。前端同样需构建对应版本。
- 当前 dev compose 的 app/frontend 在宿主机运行，工具容器并不会自动成为宿主机后端的服务端点。首版开发后端使用本机 CLI，并对齐引擎版本和目录配置；Docker 环境运行同一适配层。
- 不通过挂载 Docker socket 来让应用调用工具容器。若后续需要独立 worker，应提供受控任务接口，届时再设计部署协议。
- 保留现有本地安装包构建能力，并更新 [安装文档](CODEBASE_MEMORY_DOCKER.md) 说明“安装工具”和“业务集成”的区别。

## 5. 多仓库联合分析与文档生成

### 5.1 生成流水线

`冻结仓库快照清单 → 仓库结构分析 → 跨仓库关联 → 文档目录规划 → 逐页检索证据与生成 → 引用校验 → 暂存 → 发布`

每阶段持久化产物，任务重试从已完成阶段继续，不重复下载和消耗全部模型额度。同一 KB 首版只允许一个活动生成批次；通过租约与执行代次校验阻止超时旧任务继续发布。

开始生成时，所有启用且参与本轮的仓库必须索引成功。失败仓库默认阻止发布，并列出原因；用户可以明确禁用后重新生成，文档必须展示实际纳入的范围，不能静默遗漏。

### 5.2 客户级文档目录

首版提供默认文档目录，并保留现有 Wiki 的自定义提示词能力。`WikiConfig.content_instructions` 控制内容风格、结构与侧重点，`extraction_instructions` 控制分析和抽取重点；Code Wiki 从首版起复用这两个配置及其编辑入口，将代码场景的指令传入目录规划、证据检索和正文生成等适用阶段。

用户可以要求“重点解释订单业务”“按前端页面到后端接口组织”“面向客户运维人员”等，目录规划据此调整章节及重点，并在生成批次中冻结本轮指令。系统仍负责引用协议、权限范围和输出结构校验；自定义指令不能要求捏造无证据的链路。

下表是没有额外要求时的默认目录，并非写死模板。可视化模板编辑器、任意变量和模板导入导出可后续实现，不影响首版通过提示词定制文档：

| 层级 | 内容 | 主要依据 |
| --- | --- | --- |
| 客户项目 | 项目概览、仓库清单、技术栈、系统边界 | 仓库清单、入口、依赖清单、部署配置 |
| 系统架构 | 前端、网关、后端、公共服务及依赖关系 | 符号图、服务配置和源码核验 |
| 前后端交互 | API 对照、认证过程、请求流向 | 前端请求封装、代理前缀、后端路由与处理函数 |
| 业务流程 | 选定业务的页面入口到后端处理链 | 调用链、接口、模型和数据访问代码 |
| 各仓库 | 目录结构、模块职责、入口、公开接口、依赖 | 仓库结构、符号、源码 |
| 开发运维 | 启动步骤、配置说明、构建和部署 | 脚本与配置文件，仅描述有证据的内容 |
| 分析说明 | 快照清单、覆盖范围、未确认关联 | commit manifest、索引覆盖报告 |

首版使用现有 `index`、`synthesis` 等合法 Wiki 页面类型，额外在元数据中记录 code_wiki 文档类别。避免直接加入大量新 page_type 导致现有类型白名单、筛选和图视图失效。

### 5.3 跨仓库关联规则

- 图中的 HTTP_CALLS、GRPC_CALLS 等作为关联候选，不直接等同于已证实的业务链路。
- 前端 API 与后端路由匹配需要综合 HTTP 方法、路径、baseURL、代理重写、服务身份和源码调用上下文；仅路径同名不足以确认。
- 动态路由、运行时配置、反射和无法解析的语言要保留未知状态。未确认关系在文档中单独列出，不编造完整链路。
- 跨仓库模式显式传入本批次项目清单；即使不同客户有同名接口，也不能跨 KB 搜索后混入文档。
- 每页先检索受预算约束的图信息和源码片段，再生成正文，不把整个仓库一次性塞进模型上下文。
- 仓库中的自然语言内容属于分析材料，不能覆盖生成规则、扩大查询范围或触发命令执行。

### 5.4 引用、编辑与发布

代码引用的最低结构：`repository_id + snapshot_id/commit_sha + path + start_line/end_line`，可附符号和证据类型。GitLab 跳转链接固定到 commit；本地源码查看接口也从同一快照读取。

生成前为证据分配 ID，模型只能引用给定证据。发布前校验引用归属、文件存在和行号范围；关键调用关系还需源码复核。对不完整覆盖明确降低结论强度，无法满足必要证据的页面返回待处理状态。

`SourceRefs/ChunkRefs` 继续保留文档语义；代码证据使用独立 DTO 和来源视图。证据按页面版本存储，同时扩展历史版本与回退路径，确保回退正文后引用仍一致。

现有仓储只保证单页版本更新的事务性，尚不能保证整轮文档同时发布。新增批次发布事务：

1. 生成页面和证据先进入暂存区。
2. 校验每个目标页面的基线版本；若生成期间被人工修改，整轮发布暂停并展示冲突。
3. 在同一数据库事务中更新本轮页面、历史版本、证据与 active_run_id；失败则回滚。
4. 首版限制单轮页数和内容量，使事务可控；不通过逐页公开写入制造半新半旧的可见状态。

发布过程复用或抽取现有页面服务中的版本、目录、链接维护逻辑，增加支持外部事务的批次仓储接口，不能逐页调用后就宣称整批原子发布。

人工编辑默认保护：重新生成的内容先作为候选，用户明确选择保留或替换；不静默覆盖。新生成删除的页面需按“系统生成/人工维护”区分处理，避免误删用户内容。

### 5.5 模型并发与成本

默认每轮一个模型请求并发，走现有后台模型 limiter；配置每页上下文、每轮页数、最大 token/调用次数和总执行时长，记录实际用量。

遇到 429，优先遵守 Retry-After，使用带抖动的退避和有限重试，界面显示“等待模型额度”。现有限流按 model ID 管理，同一供应商账号配置成多个模型或被外部系统共用时，仍可能触发账号级限流；不能承诺单轮串行就消除 429。需要时再增加共享账号维度的配额协调。

## 6. API、界面与权限

### 6.1 建议 API

路由命名在实现时沿用现有单数 `/knowledgebase/:kb_id` 前缀，避免另起一套风格。

| 操作 | 建议接口 |
| --- | --- |
| 仓库绑定列表/新增 | `GET/POST /knowledgebase/:kb_id/code-wiki/repositories` |
| 编辑/移除仓库绑定 | `PATCH/DELETE /knowledgebase/:kb_id/code-wiki/repositories/:id` |
| 同步仓库或全部仓库 | `POST /knowledgebase/:kb_id/code-wiki/sync`，body 可指定 repository_ids |
| 提交文档生成 | `POST /knowledgebase/:kb_id/code-wiki/runs` |
| 批次列表/状态 | `GET /knowledgebase/:kb_id/code-wiki/runs` 和 `.../runs/:id` |
| 取消/重试 | `POST .../runs/:id/cancel`、`POST .../runs/:id/retry` |
| 发布已校验候选 | `POST .../runs/:id/publish`，携带版本基线与冲突处理结果 |
| 查看代码证据 | `GET /knowledgebase/:kb_id/code-wiki/evidence/:id` |

异步提交返回 202 和任务/批次 ID；首版轮询阶段进度，不能用缺乏依据的百分比表示实际完成量。页面阅读、编辑、历史版本继续复用 Wiki API，并扩展代码证据响应。

所有接口复用 [知识库路由权限](../internal/router/routes_knowledge.go) 和 [RBAC](../internal/router/rbac.go)：读接口要求 KB 读权限，仓库管理、同步、生成和发布要求写权限；保留 API Key 能力限制、分享权限和审计。不能只校验 tenant_id 而忽略 KB 权限。

读取证据时先从授权 KB 解析证据 ID，不接收任意本地路径、图项目名或 GitLab Token。后台任务开始、阶段恢复和发布前再次检查 KB、数据源和绑定是否仍有效。

### 6.2 前端改动

- [KnowledgeBaseEditorModal.vue](../frontend/src/views/knowledge/KnowledgeBaseEditorModal.vue)：增加 Code Wiki 类型和语言配置；复用 Wiki 生成模型、内容指令和抽取指令设置；不要求普通文档上传设置或强制 Embedding。
- [KnowledgeBaseList.vue](../frontend/src/views/knowledge/KnowledgeBaseList.vue)：增加类型标识、仓库数量、生成状态，不用文档数代表代码索引进度。
- [KnowledgeBase.vue](../frontend/src/views/knowledge/KnowledgeBase.vue)：Code Wiki 默认展示 Wiki，增加“代码仓库”“生成记录”，按类型调整操作入口。
- [DataSourceEditorDialog.vue](../frontend/src/views/knowledge/settings/DataSourceEditorDialog.vue)：复用 GitLab 凭据与仓库选择；增加角色、ref 和过滤规则；绑定状态显示 commit、快照/索引状态及错误。
- [WikiBrowser.vue](../frontend/src/views/knowledge/wiki/WikiBrowser.vue)：展示代码来源侧栏、commit 行号跳转、生成批次、覆盖提示及人工修改冲突；历史版本使用对应版本的证据。
- [useKnowledgeBase.ts](../frontend/src/hooks/useKnowledgeBase.ts)：避免进入 Code Wiki 时默认启动文档列表/上传流程；同步扩展 API 类型和国际化资源。

现有 Wiki 图展示页面间链接，不等同于代码调用图。首版继续展示文档关系；如增加代码架构视图，应明确使用另一类数据和图例。

## 7. 代码改动落点

以下新路径是建议命名，已有路径是本次实际核验的扩展点。

| 模块 | 已有文件/目录 | 建议新增内容 |
| --- | --- | --- |
| 类型和能力 | internal/types/knowledgebase.go、indexing_strategy.go、task.go | code_wiki.go：配置、仓库、快照、批次、证据；完善能力与默认值 |
| 存储迁移 | internal/database/migration.go、migrations/versioned/、migrations/sqlite/ | 两种数据库的表、索引、版本证据和状态迁移；旧 KB 不变 |
| 仓储和接口 | internal/application/repository/、internal/types/interfaces/ | code_wiki 仓储、租约/幂等、批次发布事务、保留策略 |
| GitLab 拉取 | internal/datasource/connector/gitlab/、internal/datasource/httpclient.go | 复用受控客户端的 commit 解析和归档下载，代码模式配置验证 |
| 同步分流 | internal/application/service/datasource_service.go | ProcessSync 代码类型分流、同步日志和现有 schedule 复用 |
| 引擎适配 | 新增 internal/codebase/ | CLI executor、类型化结果、版本契约、路径/项目约束、覆盖检查 |
| 生成服务 | 新增 internal/application/service/code_wiki*.go | 同步、索引、联合分析、规划、生成、证据校验、发布与取消 |
| 提示词 | internal/agent/prompts_wiki.go 保留文档流程 | 新增代码文档模板和证据协议，不修改原文档抽取语义 |
| Wiki 版本 | internal/types/wiki_page.go、service/wiki_page.go、repository/wiki_page.go | 代码证据 DTO、历史与回退一致性、可复用的事务更新能力 |
| HTTP 接口 | internal/handler/、internal/router/routes_knowledge.go | code_wiki handler、参数校验、权限和审计 |
| 队列与依赖 | internal/types/task.go、internal/router/task.go、internal/container/ | 代码任务分类、worker 注册、启动恢复、删除清理和依赖注入 |
| 前端 | frontend/src/api/、views/knowledge/、hooks/、i18n/ | Code Wiki 创建/仓库/任务界面、代码证据和冲突处理 |
| 运行部署 | docker/Dockerfile.app-codebase、docker-compose.yml、docker-compose.dev.yml | 新代码镜像构建、快照卷、版本与适配器配置、安装说明更新 |

知识库删除时撤销活动任务、阻止后续发布，异步清理绑定、快照和索引；任务取消和服务重启后残留资源可回收。复制/克隆首版只复制允许的非敏感配置，不自动复制 GitLab 凭据、运行记录或本地索引，界面要求重新绑定连接并索引。

## 8. 分阶段实施与验收

### 阶段 0：验证引擎与两仓库最小链路

使用授权的同一客户前后端两个测试仓库，不改变现有业务数据。

- 验证固定 SHA 归档下载、受限目录解包和 codebase 索引。
- 验证 0.12.1 的 JSON、覆盖查询、跨仓库模式、并发行为、缓存目录隔离及清理。
- 对比本机与 Docker 运行契约，固定支持版本。
- 选取一条已知前后端调用，核验图候选与源码；记录支持语言和未解析范围。

验收：两个仓库可得到独立快照、限定范围的联合分析和可复查的 commit/行号证据；不同 KB 的同名仓库不可互相命中。缓存隔离或跨仓库能力不满足时，先调整适配方案，不继续假定其可用。

### 阶段 1：领域模型、权限与 API 契约

新增类型、配置、数据表、仓库绑定接口；确定 DataSource 的连接职责及唯一配置来源，完成前后端 DTO。

验收：创建一个 Code Wiki、绑定多个仓库；非法 KB/source 关联被拒绝；PostgreSQL/SQLite 迁移及旧 document/faq/Wiki 能力回归通过。

### 阶段 2：后台同步与索引

实现同步分流、归档快照、CLI 适配、索引状态、重试/取消/恢复及任务拓扑；添加仓库管理界面。

验收：一个 KB 的前后端仓库均可同步索引；分支同步期间推进不影响本次内容一致性；失败仓库可单独重试，源码不会进入普通文档解析流程。

### 阶段 3：客户级 Wiki 生成闭环

实现联合分析、默认目录及自定义指令驱动的目录规划、逐页证据检索、模型调用、引用校验、暂存和首批发布；复用 Wiki 展示并加入代码来源。

验收：从一个 KB 的至少两个仓库生成客户概览、系统关系、逐仓库文档和至少一条经源码确认的前后端交互说明；关键结论可定位到固定 commit。图无法确认的链路被明确标记。配置内容指令、抽取指令后，目录、分析重点和正文体现相应要求，同时不破坏引用与权限约束。

### 阶段 4：发布可靠性、编辑与部署验收

完成整批事务发布、历史引用/回退、人工冲突处理、429 重试与预算、权限审计、删除清理，以及主/开发 compose 的集成说明。

验收：模型失败、进程重启、用户取消和发布冲突均不会破坏上一版；人工修改不被静默覆盖；跨 KB 访问被拒绝；新后端与前端镜像均包含对应实现。

**阶段 0—4 构成可交付首版。** 阶段 1 的契约确定后，前端可与后端流水线开发交错推进，但阶段 3 不能跳过阶段 0 的隔离与能力验证。

### 阶段 5：增量与自动化

基于 commit diff、页面证据和依赖反向关系，计算受影响页面；配置/公共类型等全局变更扩大影响范围。处理 rename/delete、force-push、仓库移除和过滤规则变化，无法可靠判断时回退全量生成。

增加定时生成或 Webhook 去重、共享账号配额协调与更细成本控制。验收要求更新相关文档且不留下已删除代码的当前引用，旧版仍可追溯。

### 阶段 6：代码问答与更丰富的分析

先复用 Wiki 搜索/阅读回答已生成文档的问题，再增加受权限约束的代码检索和读取工具。现有 wiki_read_source_doc 面向文档，需新增代码来源读取能力；不要把代码片段伪装成普通文档 ID。

代码向量检索、可视化模板编辑器和独立代码关系图按实际需求追加，不作为首版文档生成的前置条件；提示词自定义已包含在首版。

## 9. 实现阶段的验证清单

| 范围 | 必须覆盖的用例 |
| --- | --- |
| 配置与迁移 | 新旧 KB 类型、能力默认值、无 Embedding 的代码生成配置、两种数据库迁移、旧数据读取 |
| GitLab | 多项目/不同 ref、固定 SHA、无权限/分支不存在、超时、下载中断、归档限制与路径穿越、私有部署网络配置 |
| 引擎契约 | 固定版本 JSON、超时/取消、输出超限、部分解析、重名项目、跨 KB 查询限制、两个执行进程的竞争 |
| 异步任务 | 重复提交、租约过期、断点恢复、队列分类、取消、KB/连接删除后的迟到任务、重启恢复 |
| 生成与证据 | 同名路由误匹配、代理前缀、动态路径未知、证据缺失、行号越界、提示注入材料、模型预算与 429 |
| 发布与历史 | 整批失败回滚、人工版本冲突、历史版本引用、回退来源一致性、重复发布幂等、快照保留与回收 |
| 权限 | 跨租户/跨 KB 仓库绑定、证据读取、分享只读账号、API Key 限制、后台重新鉴权、Token 不进入日志与提示词 |
| 前端与回归 | 多仓库选择、状态恢复、失败重试、来源查看、冲突处理、普通文档/FAQ/Wiki 的原有流程 |

测试以真实边界和状态转换为主：Go 对相关服务/仓储/路由做定向测试，引擎与 GitLab 用小型测试仓库做集成测试；前端执行类型检查、构建和相关交互验证。不要用只复述实现逻辑的测试替代权限、版本和恢复验证。

## 10. 当前结论

WeKnora 已有可复用的 GitLab 连接、异步调度、Wiki 页面、编辑、历史和权限基础。主要新增工作集中在“代码快照与索引适配 → 同客户多仓库联合分析 → 证据驱动的文档生成 → 批次发布”，而不是重新搭建知识库产品。

推荐从阶段 0 开始，以同一客户的两个前后端仓库验证实际引擎能力，再沿阶段 1—4 完成首版；避免先扩展文档文件白名单，最后才发现没有一致的代码版本、证据和客户级分析模型。
