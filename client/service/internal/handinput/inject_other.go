//go:build !windows

package handinput

// 非 Windows 平台没有注入实现。
//
// 上游 hiBoss 有一份 macOS 的 CGEventPost 实现,**但那需要 cgo**,而本仓库
// 明令「禁止引入 cgo 依赖」(AGENTS.md 工程约定)。这条禁令的既定理由是保住
// `CGO_ENABLED=0 GOOS=windows go build` 这条发布路径;放宽它属于规范性修改,
// 按纪律不能在实现批次里自我授权。
//
// 后果如实记在这里:**端到端冒烟只能在 Windows 上跑**,开发机上能跑到
// 「生成计划」为止,再往下就是这个错误。这不是缺陷,是已知边界。
func NewInjector() (Injector, error) { return nil, ErrInjectorUnsupported }
