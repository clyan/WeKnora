package types

// Version 2 replaces the article-first outline with source-backed frontend
// entries. Historical versions remain readable, not selectable for new runs.
const CodeWikiDirectoryVersionFrontend = 2
const CodeWikiFrontendTemplate = "frontend_catalog_v1"

const CodeWikiFrontendContentInstructions = `从当前代码建立知识基线，只写能从实现及使用关系查证的事实。先识别项目入口、操作、调用与条件，再组织页面、API、组件、架构、流程、规范和待澄清问题；最后汇总概览。目录名不决定分类，一个主题可跨文件，一个文件可有多个主题，不填充空类型。先说明用途与入口，再用操作、参数、状态及关联表格展开。README、已有文档、注释、Mock和样例不作为业务依据；不确定业务含义交由人工澄清，人工解释与代码事实分开。`

const CodeWikiFrontendExtractionInstructions = `页面：从实际注册和挂载追踪按钮、菜单、行操作、表单及自动触发，记录校验、显示/禁用条件、状态、请求、跳转与异常。API：逐调用点追踪参数构造、字段来源、展开/默认值/条件转换、实际响应使用和错误处理；区分类型声明与真实传入。组件：输入、事件、暴露方法、状态、实际使用位置。架构：仅描述有实现依据的应用边界、路由、权限、状态和请求机制，不把包集成写成微前端。规范区分代码配置与观察到的写法，不采用文档或注释约定。动态边界、不可访问依赖及未确认业务含义进入问题，不推断后端或线上行为。`

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
