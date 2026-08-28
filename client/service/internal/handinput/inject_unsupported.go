//go:build !windows && !darwin

package handinput

// 既不是 Windows(生产)也不是 macOS(开发机)——没有注入实现。
// 不挂路由、只记一行,业务一切照旧。
func NewInjector() (Injector, error) { return nil, ErrInjectorUnsupported }
