//go:build darwin

package handinput

import (
	"math"
	"os"
	"testing"
	"time"
)

// 这条用例**会真的动本机鼠标**,所以默认跳过,要显式点头才跑:
//
//	RECRUITHELPER_DEV_MOUSE_TEST=1 go test ./client/service/internal/handinput/ -run Darwin -v
//
// 它只移动、**绝不点击**:开发机上一次误点可能落在任何东西上,而移动是无副作用的。
// 跑完把光标放回原处。
func TestDarwinInjectorMovesRealCursor(t *testing.T) {
	if os.Getenv("RECRUITHELPER_DEV_MOUSE_TEST") != "1" {
		t.Skip("需要 RECRUITHELPER_DEV_MOUSE_TEST=1 —— 本用例会真的动鼠标")
	}
	inj, err := NewInjector()
	if err != nil {
		t.Fatalf("造不出注入器:%v", err)
	}
	defer inj.Close()
	t.Logf("平台:%s", inj.Platform())

	x0, y0, err := inj.CursorPos()
	if err != nil {
		t.Fatalf("读不到光标位置:%v", err)
	}
	t.Logf("起始光标 (%d, %d)", x0, y0)
	defer func() { _ = inj.MouseMove(float64(x0), float64(y0)) }()

	// 往回移一点点,别跑到屏幕外
	tx, ty := float64(x0)-40, float64(y0)-30
	if tx < 10 || ty < 10 {
		tx, ty = float64(x0)+40, float64(y0)+30
	}
	if err := inj.MouseMove(tx, ty); err != nil {
		t.Fatalf("MouseMove 报错:%v", err)
	}
	time.Sleep(60 * time.Millisecond) // 给系统一点时间把事件送到

	x1, y1, err := inj.CursorPos()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("移动后光标 (%d, %d),目标 (%.0f, %.0f)", x1, y1, tx, ty)
	if d := math.Hypot(float64(x1)-tx, float64(y1)-ty); d > 2 {
		// 未授权时 CGEventPost **不报错**,事件被系统静默丢弃 —— 症状正是
		// 「程序说发完了,屏幕上什么也没发生」。所以判据必须是回读,不是返回值。
		// 授权对象是**启动本进程的那个应用**(Terminal / iTerm / IDE),不是 go test
		// 编出来的临时二进制——后者每次路径都不同,单独授权它没有意义。
		t.Fatalf("光标没到位,偏 %.1f 像素。Platform() 报的授权状态见上一行;"+
			"未授权时 CGEventPost 不报错、事件被静默丢弃,所以判据只能是回读", d)
	}
}

// 结构体传值与结构体返回是 purego 在 arm64 上最容易出问题的两处(CGPoint 走 HFA),
// 所以单独验一次读回:它同时覆盖 CGEventCreate / CGEventGetLocation / CFRelease。
// 这条**不动鼠标**,可以随时跑。
func TestDarwinCursorPosReadsPlausibleCoordinates(t *testing.T) {
	inj, err := NewInjector()
	if err != nil {
		t.Skipf("本机造不出注入器:%v", err)
	}
	defer inj.Close()
	x, y, err := inj.CursorPos()
	if err != nil {
		t.Fatal(err)
	}
	if x < -20000 || x > 20000 || y < -20000 || y > 20000 {
		t.Fatalf("读回的光标坐标离谱 (%d, %d) —— 多半是 CGPoint 的结构体返回没走对 ABI", x, y)
	}
	t.Logf("光标 (%d, %d) —— CGPoint 的结构体返回走通了", x, y)
}
