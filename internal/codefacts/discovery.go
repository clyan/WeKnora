package codefacts

import "strings"

// Discovery adapters identify syntax to trace, not runtime/business facts.
// The generic nodes remain available for unknown wrappers and frameworks.
type Candidate struct {
	StartOffset int        `json:"start_offset"`
	EndOffset   int        `json:"end_offset"`
	Kind        string     `json:"kind"`
	Adapter     string     `json:"adapter"`
	StartLine   int        `json:"start_line"`
	EndLine     int        `json:"end_line"`
	Expression  string     `json:"expression,omitempty"`
	Properties  []Property `json:"properties,omitempty"`
	Arguments   []string   `json:"arguments,omitempty"`
	Unknowns    []string   `json:"unknowns,omitempty"`
}

type Discovery struct {
	Candidates []Candidate `json:"candidates"`
	Boundaries []string    `json:"boundaries"`
}

func Discover(nodes []Node) Discovery {
	result := Discovery{Boundaries: []string{
		"结构候选只证明语法存在；注册关系、调用点及参数字段仍需逐项追踪",
		"动态注册、动态属性、未知请求封装和不可访问依赖尚未解析；不代表全项目覆盖",
	}}
	for _, node := range nodes {
		candidate := Candidate{StartLine: node.StartLine, EndLine: node.EndLine, StartOffset: node.StartOffset, EndOffset: node.EndOffset}
		switch node.Kind {
		case "component":
			for _, property := range node.Properties {
				if strings.HasPrefix(property.Name, "on") && len(property.Name) > 2 {
					operation := candidate
					operation.Kind, operation.Adapter, operation.Expression = "interaction", "jsx-event", node.Name
					operation.Properties = node.Properties
					result.Candidates = append(result.Candidates, operation)
					break
				}
			}
			if node.Name == "Route" || strings.HasSuffix(node.Name, ".Route") {
				candidate.Kind, candidate.Adapter = "route", "react-route-jsx"
				candidate.Properties = node.Properties
				candidate.Unknowns = []string{"需核对组件导入和实际 Router/Routes 挂载；名称本身不足以证明注册"}
				result.Candidates = append(result.Candidates, candidate)
			}
		case "object":
			hasPath, hasTarget := false, false
			for _, p := range node.Properties {
				name := strings.Trim(p.Name, "\"'")
				hasPath = hasPath || name == "path"
				hasTarget = hasTarget || name == "element" || name == "Component" || name == "component" || name == "children"
			}
			if hasPath && hasTarget {
				candidate.Kind, candidate.Adapter = "route", "route-config"
				candidate.Properties = node.Properties
				candidate.Unknowns = []string{"需沿配置使用位置确认路由注册，不能只凭对象形状断言页面可访问"}
				result.Candidates = append(result.Candidates, candidate)
			}
		case "call":
			candidate.Expression, candidate.Arguments = node.Callee, node.Arguments
			switch {
			case node.Callee == "fetch" || node.Callee == "window.fetch" || node.Callee == "globalThis.fetch":
				candidate.Kind, candidate.Adapter = "request", "fetch"
			case node.Callee == "axios" || strings.HasPrefix(node.Callee, "axios."):
				candidate.Kind, candidate.Adapter = "request", "axios"
			case node.Callee == "request" || strings.HasPrefix(node.Callee, "request."):
				candidate.Kind, candidate.Adapter = "request", "request-wrapper"
				candidate.Unknowns = append(candidate.Unknowns, "请求封装内部的方法、前缀及追加字段必须回读实现")
			case node.Callee == "createBrowserRouter" || node.Callee == "createHashRouter" || node.Callee == "useRoutes":
				candidate.Kind, candidate.Adapter = "route_registration", "react-router"
			}
			if candidate.Kind != "" {
				candidate.Unknowns = append(candidate.Unknowns, "需确认导入/绑定和调用可达性；静态调用不证明线上已执行")
				result.Candidates = append(result.Candidates, candidate)
			}
		}
	}
	return result
}
