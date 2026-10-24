package types

import (
	"sort"
	"strings"
)

// CodeWikiDirectoryVersionV1 is frozen per run. An absent version keeps
// legacy directories; ordinary generation must not migrate existing pages.
const CodeWikiDirectoryVersionV1 = 1

type CodeWikiDirectoryCategory struct {
	Category string
	Title    string
}

// CodeWikiDirectoryCategories returns the fixed top-level order. Return a
// fresh slice so a caller cannot mutate the directory contract globally.
func CodeWikiDirectoryCategories() []CodeWikiDirectoryCategory {
	return []CodeWikiDirectoryCategory{
		{CodeWikiPageCategoryOverview, "项目概览"},
		{CodeWikiPageCategoryArchitecture, "系统架构"},
		{CodeWikiPageCategoryRepository, "仓库文档"},
		{CodeWikiPageCategoryInteraction, "接口与跨端契约"},
		{CodeWikiPageCategoryWorkflow, "功能链路"},
		{CodeWikiPageCategoryDevOps, "开发与联调"},
		{CodeWikiPageCategoryAnalysis, "分析范围与版本"},
	}
}

type CodeWikiDirectoryTopic struct {
	RepositoryID string      `json:"repository_id,omitempty"`
	Slug         string      `json:"slug"`
	Title        string      `json:"title"`
	Category     string      `json:"category"`
	CategoryPath StringArray `json:"category_path"`
	SortOrder    int         `json:"sort_order"`
}

// GitLab display names may contain namespace separators; they are labels,
// not user-supplied folder paths. Preserve the label without creating levels.
func CodeWikiDirectoryLabel(name string) string {
	name = strings.NewReplacer("/", "／", "\\", "＼", "\x00", "", "\n", " ", "\r", " ", "\t", " ").Replace(name)
	name = strings.TrimSpace(name)
	if len([]rune(name)) > 60 {
		name = string([]rune(name)[:60])
	}
	if name == "" {
		return "未命名仓库"
	}
	return name
}

// CodeWikiDirectoryTopics is the complete fixed topic catalog. A planner may
// select supported topics, but cannot rename them or use branches as identity.
func CodeWikiDirectoryTopics(repositories []*CodeWikiRunRepository) []CodeWikiDirectoryTopic {
	repositories = append([]*CodeWikiRunRepository(nil), repositories...)
	sort.SliceStable(repositories, func(i, j int) bool {
		if repositories[i] == nil {
			return false
		}
		if repositories[j] == nil {
			return true
		}
		return repositories[i].RepositoryID < repositories[j].RepositoryID
	})
	titles := map[string][]string{
		CodeWikiPageCategoryOverview:     {"项目介绍与能力地图", "仓库清单与阅读导航"},
		CodeWikiPageCategoryArchitecture: {"系统组成与仓库职责", "请求链路与服务边界", "数据存储与外部依赖", "认证与权限架构"},
		CodeWikiPageCategoryInteraction:  {"前后端接口对照", "认证、错误码与通用约定", "未匹配接口与兼容性问题"},
		CodeWikiPageCategoryWorkflow:     {"功能链路总览"},
		CodeWikiPageCategoryDevOps:       {"开发环境准备", "前后端联合启动", "环境配置与代理", "联调与常见问题", "系统部署说明"},
		CodeWikiPageCategoryAnalysis:     {"仓库、分支与代码版本", "分析范围与过滤规则", "覆盖情况与已知限制", "文档更新记录"},
	}
	var topics []CodeWikiDirectoryTopic
	for categoryIndex, category := range CodeWikiDirectoryCategories() {
		for i, title := range titles[category.Category] {
			topics = append(topics, CodeWikiDirectoryTopic{
				Slug: category.Category + "-" + string(rune('a'+i)), Title: title, Category: category.Category,
				CategoryPath: StringArray{category.Title}, SortOrder: categoryIndex*1000 + i,
			})
		}
	}
	roleTopics := map[string][]string{
		"frontend": {"项目结构与启动", "路由与页面", "组件与状态管理", "请求封装与错误处理", "配置与构建"},
		"backend":  {"项目结构与启动", "路由组织与处理入口", "数据模型与持久化", "中间件与权限", "配置与外部依赖", "测试与部署"},
		"library":  {"项目结构与使用方式", "类型与对外协议", "提供的复用能力", "依赖与兼容性"},
		"service":  {"项目结构与启动", "服务入口与对外能力", "核心模块与依赖", "配置、测试与部署"},
		"other":    {"项目结构与入口", "主要能力与依赖", "配置与运行说明"},
	}
	roleNames := map[string]string{"frontend": "前端", "backend": "后端", "library": "依赖库", "service": "服务", "other": "其他"}
	for repoIndex, repo := range repositories {
		if repo == nil || repo.Excluded {
			continue
		}
		role := repo.Role
		if _, ok := roleTopics[role]; !ok {
			role = "other"
		}
		for i, title := range roleTopics[role] {
			topics = append(topics, CodeWikiDirectoryTopic{RepositoryID: repo.RepositoryID, Slug: "repo-" + strings.ReplaceAll(repo.RepositoryID, "-", "") + "-" + string(rune('a'+i)),
				Title: title, Category: CodeWikiPageCategoryRepository,
				CategoryPath: StringArray{"仓库文档", roleNames[role] + " · " + CodeWikiDirectoryLabel(repo.DisplayName)}, SortOrder: 2000 + repoIndex*20 + i})
		}
	}
	return topics
}
