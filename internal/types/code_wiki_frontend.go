package types

// Version 2 replaces the article-first outline with source-backed frontend
// entries. Historical versions remain readable, not selectable for new runs.
const CodeWikiDirectoryVersionFrontend = 2
const CodeWikiFrontendTemplate = "frontend_catalog_v1"

const CodeWikiFrontendContentInstructions = `目标：从当前代码建立前端知识基线，只写能从实现及使用关系查证的事实。

取证
- 必须通过工具读取源码与配置后再下结论，不得凭文件名、目录名或常识推断。
- 每条事实必须关联已读取的源码证据，由程序输出文件:行号锚点；模型不得编造锚点。
- 只陈述实际取证范围；检索不到时写明检索范围，不宣称全项目没有。
- README、已有文档、注释、Mock、样例不作为业务依据。

组织
- 顺序：先识别项目入口、操作、调用与条件；再组织页面、API、组件、架构、流程、规范和待澄清问题；最后汇总概览。
- 分类由内容决定，不由目录名决定；不填充空类型。
- 默认一页面、一请求模块或一组件一篇，复杂主题可拆分；微前端按应用分别组织。
- 一个文档聚焦一个主题；跨主题关系优先用链接表达，允许保留理解当前主题必需的简短上下文。
- API 模块内逐接口展开；未发现调用点也可记录定义侧事实。
- 架构按实际存在的主题分别成篇：主子应用关系、通信、技术栈、基础设施、跨仓依赖等。
- 业务流程依据可查证的页面跳转、调用或事件关系梳理；并发、异步和条件分支分别描述，不将源码排列顺序视为执行时序。
- 每篇先说明职责与入口，再用操作、参数、状态及关联表格展开。

不确定性
- 不确定的业务含义写入待澄清问题交人工澄清；人工解释与代码事实分开。
- 仅对明确提供的人工内容标记来源与确认状态；不得自行生成“人工确认”内容。人工段落的保护由程序负责。
- 不编造后端链接与反向统计；无法确认则留空。
- 元数据由程序管理；模型不得输出 owner、last_verified、confidence，程序无可靠依据时省略。`

const CodeWikiFrontendExtractionInstructions = `页面：从实际注册和挂载追踪按钮、菜单、行操作、表单及自动触发，记录校验、显示/禁用条件、权限点与无权限时的降级表现、状态读写、请求、跳转传参与异常。
API：分开记录类型声明、封装定义侧参数与各调用点真实传入；追踪参数构造、字段来源、展开/默认值/条件转换、实际响应使用和错误处理。无调用点时注明检索范围。
组件：输入必填与默认值、事件载荷、暴露方法、插槽、状态、内部接口和实际使用位置。
架构：按主题核对主子应用加载与生命周期、通信双方与清理、技术栈声明/锁定版本/实际使用、基础设施及跨仓依赖；不把包集成写成微前端。
规范：区分代码配置、已执行检查与观察到的写法；不采用文档或注释中的约定。
总览与反向关系：只汇总已有且可追溯的文档与关系；无统计不能填零。
动态边界、不可访问依赖及未确认业务含义进入问题，不推断后端或线上行为。`

func CodeWikiFrontendCategories() []CodeWikiDirectoryCategory {
	return []CodeWikiDirectoryCategory{
		{"frontend_page", "页面模块"}, {"frontend_api", "API模块"},
		{"frontend_component", "组件文档"}, {"frontend_architecture", "架构总览"},
		{"frontend_workflow", "架构总览"}, {"frontend_convention", "规范"},
		{"frontend_reference", "references"}, {"frontend_question", "待澄清问题"},
	}
}

func IsCodeWikiFrontendCategory(category string) bool {
	for _, item := range CodeWikiFrontendCategories() {
		if item.Category == category {
			return true
		}
	}
	return false
}
