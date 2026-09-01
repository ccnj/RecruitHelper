package handinput

import (
	"strings"
	"testing"
)

// 一份最小的合法计划:「你」= ni + Space 上屏。时刻取小,免得用例真去睡。
func planNi() TypePlan {
	return TypePlan{Words: []PlanWord{{
		Text:   "你",
		Splits: []int{2},
		Keys: []PlanKey{
			{Code: "KeyN", Down: 0, Up: 3},
			{Code: "KeyI", Down: 2, Up: 5},
		},
		Commit: &PlanKey{Code: "Space", Down: 7, Up: 9},
	}}}
}

// 带修饰键的计划:「？」= Shift 按住时按 Slash。
func planQuestion(shiftUp, nextDown float64) TypePlan {
	return TypePlan{Words: []PlanWord{{
		Text:   "？",
		Direct: true,
		Keys: []PlanKey{
			{Code: "ShiftLeft", Down: 0, Up: shiftUp, Modifier: true},
			{Code: "Slash", Down: 1, Up: 3, Shift: true},
			{Code: "KeyX", Down: nextDown, Up: nextDown + 2},
		},
	}}}
}

func TestTypeFlattenOrdersReleaseBeforePressAtSameInstant(t *testing.T) {
	// 同刻的抬起必须排在按下之前。若反过来,「上一键松手」与「下一键按下」同刻时
	// 会短暂同时按住两个键——对普通字母无害,对修饰键就是那个大写字母的 bug。
	p := TypePlan{Words: []PlanWord{{Keys: []PlanKey{
		{Code: "KeyA", Down: 0, Up: 10},
		{Code: "KeyB", Down: 10, Up: 20},
	}}}}
	evs, _ := p.Flatten()
	var got []string
	for _, e := range evs {
		arrow := "↑"
		if e.Down {
			arrow = "↓"
		}
		got = append(got, e.Code+arrow)
	}
	want := []string{"KeyA↓", "KeyA↑", "KeyB↓", "KeyB↑"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("事件次序 %v,应为 %v", got, want)
	}
}

func TestTypeRejectsShiftWindowTooTight(t *testing.T) {
	// 「薪资」→「Xin子」那个 bug:Shift 松手于 100,而下一个字母在 120 按下,
	// 间隔 20ms < 40ms —— 输入法会收到大写 X 并当成英文。
	f := &fakeInjector{}
	s := NewService(f)
	if _, err := s.Type(planQuestion(100, 120)); err == nil {
		t.Fatal("修饰键窗口只有 20ms,必须拒绝")
	} else if !strings.Contains(err.Error(), "修饰键窗口不足") {
		t.Fatalf("错误信息要说清是修饰键窗口:%v", err)
	}
	if len(f.keys) != 0 {
		t.Fatalf("校验不过时一个键都不该发,却发了 %v", f.keys)
	}

	// 窗口够(100 → 141,间隔 41ms)时照常通过。
	f2 := &fakeInjector{}
	if _, err := NewService(f2).Type(planQuestion(100, 141)); err != nil {
		t.Fatalf("41ms 的窗口应当通过:%v", err)
	}
}

func TestTypeRefusesUnknownKeyBeforeSendingAnything(t *testing.T) {
	// 失效方向必须是「一个都没发」而不是「发了一半」。边发边试的话,打到一半失败
	// 会留下按住的修饰键,而那时用户的键盘此后都带着 Shift。
	f := &fakeInjector{unknownKey: "Space"}
	if _, err := NewService(f).Type(planNi()); err == nil {
		t.Fatal("计划里有注入器不认识的键,必须拒绝")
	}
	if len(f.keys) != 0 {
		t.Fatalf("一个键都不该发,却发了 %v", f.keys)
	}
}

func TestTypeReleasesHeldKeysWhenInjectionFailsMidway(t *testing.T) {
	// **这条是键盘独有的安全性质。** 鼠标注入失败,光标停在哪儿是个静态事实;
	// 键盘注入失败若留下按住的修饰键,用户的整台机器此后都带着 Shift,而且没有
	// 任何东西会自动收拾。
	f := &fakeInjector{keyFailAt: 2} // 第 2 次按键动作(Slash↓)当场失败
	if _, err := NewService(f).Type(planQuestion(100, 200)); err == nil {
		t.Fatal("注入失败应当报出来")
	}
	// 发出的两次是 ShiftLeft↓、Slash↓;收场时两个都必须被放开,后按的先放。
	want := []string{"ShiftLeft↓", "Slash↓", "Slash↑", "ShiftLeft↑"}
	if strings.Join(f.keys, ",") != strings.Join(want, ",") {
		t.Fatalf("收场序列 %v,应为 %v —— 按住的键必须放开", f.keys, want)
	}
}

func TestTypeDisarmsClick(t *testing.T) {
	// 打字改变了世界,上一次的落点确认已经过期。
	f := &fakeInjector{}
	s := NewService(f)
	s.armed = true
	if _, err := s.Type(planNi()); err != nil {
		t.Fatalf("合法计划应当通过:%v", err)
	}
	if s.armed {
		t.Fatal("打字之后必须解除点击放行")
	}
}

func TestTypeSendsEveryKeyOnceInPlanOrder(t *testing.T) {
	f := &fakeInjector{}
	res, err := NewService(f).Type(planNi())
	if err != nil {
		t.Fatalf("合法计划应当通过:%v", err)
	}
	want := []string{
		"KeyN↓", "KeyI↓", "KeyN↑", "KeyI↑", "Space↓", "Space↑",
	}
	if strings.Join(f.keys, ",") != strings.Join(want, ",") {
		t.Fatalf("按键序列 %v,应为 %v", f.keys, want)
	}
	if res.Keys != len(want) {
		t.Fatalf("回执说发了 %d 次,实际 %d 次", res.Keys, len(want))
	}
}

func TestTypeRejectsEmptyAndBackwardsKeys(t *testing.T) {
	f := &fakeInjector{}
	s := NewService(f)
	if _, err := s.Type(TypePlan{}); err == nil {
		t.Error("空计划必须拒绝")
	}
	bad := TypePlan{Words: []PlanWord{{Keys: []PlanKey{{Code: "KeyN", Down: 10, Up: 3}}}}}
	if _, err := s.Type(bad); err == nil {
		t.Error("松手早于按下的键必须拒绝")
	}
	if len(f.keys) != 0 {
		t.Fatalf("一个键都不该发,却发了 %v", f.keys)
	}
}
