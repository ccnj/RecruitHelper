package dispatch

import "testing"

// debug.osClick 在脑侧只许有一个生产者:诊断台那个手动端点。
//
// 它会点页面上 selector 指着的任意元素——比 osProbe 的可逆开关、osType 的输入框都宽:
// 业务路径里一句"顺手点一下"就可能落在发送、确认、删除上。门禁与 osProbe 那道同款。
func TestOsClickHasNoBusinessProducer(t *testing.T) {
	allowed := map[string]bool{
		"client/service/internal/dispatch/osclick_dispatch.go":      true,
		"client/service/internal/dispatch/osclick_producer_test.go": true,
		"client/service/internal/adminhttp/os_click.go":             true,
	}
	assertSoleProducer(t, allowed, []string{"PrimDebugOsClick", ".OsClick("},
		"debug.osClick 只许由诊断台手动端点生产,以下文件越界了:%v\n它会点页面上的任意元素——业务路径一律不得铸它")
}
