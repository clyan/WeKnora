package types

// Version 2 replaces the article-first outline with source-backed frontend
// entries. Historical versions remain readable, not selectable for new runs.
const CodeWikiDirectoryVersionFrontend = 2
const CodeWikiFrontendTemplate = "frontend_catalog_v1"

const CodeWikiFrontendContentInstructions = `先阅读真实源码，理解项目入口、技术栈和组织方式，再按有证据的知识主题组织页面、API、组件、架构、前端流程、规范、参考资料与待澄清问题。目录名称不是分类依据；一个主题可关联多个文件，一个文件可包含多个主题，不必填满所有类型。先说明用途与入口，再用操作、参数、状态和关联表格展开，链接已有知识条目。仅描述提供源码能够证明的当前实现；前端接口调用与后端实现分开，缺失证据写为待确认。`

const CodeWikiFrontendExtractionInstructions = `页面：路由、操作、表单校验、显示/禁用条件、状态变化、接口调用和跳转。API：回到真实调用点确认参数、响应字段和异常处理，标注前端调用视角及 Mock。组件：职责、输入、事件、内部状态、依赖和使用位置。架构：应用边界、路由、权限、状态管理、请求封装；仅在实际存在时分析主子应用通信。规范只提取代码或资料已有约定。不可访问的依赖止于调用边界，不推断服务端权限、数据库和事务。`

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
