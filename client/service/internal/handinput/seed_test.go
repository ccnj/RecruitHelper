package handinput

import "testing"

// 种子翻译按平台各写各的,因为差异的根源是"本平台的注入 API 收什么单位"。
//
// 这一条钉的是 2026-08-28 的真机两点实测(副屏 1470x956 @2x,页面缩放 100%):
//
//	ScaleX = 1.000000   OffsetX = 2560.000   ← 精确等于 window.screenX,差 0.0
//	ScaleY = 1.000000   OffsetY =  638.000   ← screenY(517) + 121(浏览器顶部高度)
//
// 在此之前只有一份照 Windows 抄的公式(offset = screenX × dpr)。macOS 的 CGEventPost
// 收 point 而不是物理像素,于是多乘一遍:副屏上 screenX=2560,种子把视口正中算到
// x=6590 —— 超出整个桌面右边界 2560 点,光标被钳死在边角、压根不在页面上,
// 观测不到落点就学不到东西,重试全成瞎扫。整整 34 秒。
func TestSeedCalibIsPlatformSpecific(t *testing.T) {
	inj, err := NewInjector()
	if err != nil {
		t.Skipf("本平台没有注入实现:%v", err)
	}
	defer inj.Close()

	// 那天的实测现场:窗口在副屏,原点 (2560, 517),Retina 2x。
	got := inj.SeedCalib(WindowHint{ScreenX: 2560, ScreenY: 517, DPR: 2})

	switch p := inj.Platform(); {
	case len(p) >= 6 && p[:6] == "darwin":
		if got.ScaleX != 1 || got.ScaleY != 1 {
			t.Fatalf("macOS 的 CGEventPost 收 point,scale 必须是 1,得到 %v/%v", got.ScaleX, got.ScaleY)
		}
		if got.OffsetX != 2560 {
			t.Fatalf("OffsetX 必须精确等于 screenX(实测差 0.0),得到 %v —— 乘了 dpr 就会是 5120,"+
				"那会把光标算到桌面外 2560 点", got.OffsetX)
		}
		if got.OffsetY != 517 {
			t.Fatalf("OffsetY 用裸 screenY,差的那 121 点交给搭车标定学(它小到光标还在页面上);"+
				"得到 %v", got.OffsetY)
		}
	case len(p) >= 7 && p[:7] == "windows":
		// SendInput 收虚拟桌面物理像素,上游 Windows 实测:150% 下不乘偏 227 物理像素。
		if got.ScaleX != 2 || got.OffsetX != 5120 {
			t.Fatalf("Windows 侧必须乘 dpr,得到 scale=%v offsetX=%v", got.ScaleX, got.OffsetX)
		}
	default:
		t.Skipf("未覆盖的平台 %s", p)
	}
}
