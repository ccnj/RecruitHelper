package handinput

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// 会驱动上屏词的假注入器。**刻意与 fakeInjector 分开**:后者不实现 wordDriver,
// 那就是 macOS 那一路,既有用例正好在验"不驱动上屏词的平台照常打字"。
type drivingInjector struct {
	fakeInjector
	drives    int
	gotWords  []PlanWord
	driveErr  error
	closed    int
	settled   string
	closedAt  int // Close 被调用时已经发出去多少次按键
	settleGot time.Duration
}

func (d *drivingInjector) DriveWords(words []PlanWord, wait time.Duration) (WordSession, error) {
	d.drives++
	if d.driveErr != nil {
		return nil, d.driveErr
	}
	d.gotWords = words
	_ = wait
	return &fakeSession{owner: d}, nil
}

type fakeSession struct{ owner *drivingInjector }

func (f *fakeSession) Settle(t time.Duration) string {
	f.owner.settleGot = t
	return f.owner.settled
}
func (f *fakeSession) Close() {
	f.owner.closed++
	f.owner.closedAt = len(f.owner.keys)
}

// 一份最小的可播计划:两个词,第二个走 composition。
func twoWordPlan() TypePlan {
	return TypePlan{Words: []PlanWord{
		{Text: "你好", Keys: []PlanKey{
			{Code: "KeyN", Down: 0, Up: 20}, {Code: "KeyI", Down: 30, Up: 50},
		}, Commit: &PlanKey{Code: "Space", Down: 60, Up: 80}, Splits: []int{2}},
		{Text: "，", Direct: true, Keys: []PlanKey{
			{Code: "Comma", Down: 100, Up: 120},
		}},
	}}
}

func TestTypeWithoutWordDriverStillTypes(t *testing.T) {
	// macOS 那一路。**不驱动上屏词不是缺口**,是"上屏词由系统输入法挑、我方
	// 不可控"的如实表达——段一真机里「加个」出成「价格」正是这个。
	f := &fakeInjector{}
	res, err := NewService(f).Type(twoWordPlan())
	if err != nil {
		t.Fatalf("不该失败:%v", err)
	}
	if res.Keys != 8 {
		t.Errorf("应发 8 次按键,实得 %d", res.Keys)
	}
	if res.Words != "" {
		t.Errorf("本平台不驱动上屏词,Words 该是空的,实得 %q", res.Words)
	}
}

func TestTypeHandsWholeWordListToDriver(t *testing.T) {
	// 词表**原样转交**,含音节边界——Splits 本包自己不用,是 TIP 组字区
	// 显示撇号用的(nihao 显示成 ni'hao)。转交时丢了不会报错,只是撇号没了。
	d := &drivingInjector{settled: "TIP 上屏 2/2 词,词表已用完"}
	res, err := NewService(d).Type(twoWordPlan())
	if err != nil {
		t.Fatalf("不该失败:%v", err)
	}
	if len(d.gotWords) != 2 {
		t.Fatalf("该转交 2 个词,实得 %d", len(d.gotWords))
	}
	if d.gotWords[0].Text != "你好" || len(d.gotWords[0].Splits) != 1 || d.gotWords[0].Splits[0] != 2 {
		t.Errorf("第一个词或它的音节边界没原样带过去:%+v", d.gotWords[0])
	}
	if res.Words != "TIP 上屏 2/2 词,词表已用完" {
		t.Errorf("对账结论没带回来:%q", res.Words)
	}
	if d.settleGot != tipSettleWait {
		t.Errorf("Settle 该拿到 %v,实得 %v", tipSettleWait, d.settleGot)
	}
}

func TestTypeSendsNoKeyWhenDriverRefuses(t *testing.T) {
	// **Windows 上没有 TIP 就不打。** 照打的话出来的是微软拼音的首选词,
	// 而那与"TIP 装了但选错"在现场长得一模一样,会把一次真机趟白白烧掉。
	// 失效方向是"一个键都没发"。
	d := &drivingInjector{driveErr: errors.New("没有 TIP 连上来")}
	res, err := NewService(d).Type(twoWordPlan())
	if err == nil {
		t.Fatal("驱动器拒绝时必须失败")
	}
	if !strings.Contains(err.Error(), "没有 TIP") {
		t.Errorf("原因该原样带出,实得:%v", err)
	}
	if len(d.keys) != 0 || res.Keys != 0 {
		t.Errorf("一个键都不该发,实得 %d 次:%v", res.Keys, d.keys)
	}
}

func TestTypeAlwaysClosesSession(t *testing.T) {
	// **留在受驱动状态的输入法比按住的 Shift 更要紧**:招聘人员随手敲一句话,
	// 屏幕上出来的会是我们的词表内容。所以收尾必须无条件发生。
	for _, tc := range []struct {
		name     string
		failAt   int
		wantFail bool
	}{
		{"打完全程", 0, false},
		{"打到一半失败", 3, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &drivingInjector{}
			d.keyFailAt = tc.failAt
			_, err := NewService(d).Type(twoWordPlan())
			if tc.wantFail && err == nil {
				t.Fatal("该失败却成功了")
			}
			if !tc.wantFail && err != nil {
				t.Fatalf("不该失败:%v", err)
			}
			if d.closed != 1 {
				t.Fatalf("Close 该恰好调用一次,实得 %d", d.closed)
			}
		})
	}
}

func TestTypeClosesSessionAfterKeysNotBefore(t *testing.T) {
	// 次序有实质意义:CLEAR 早于最后一个按键的话,末尾几个词会退回微软拼音,
	// 而这种"前面对、后面错"的症状最容易被误读成排版器的锅。
	d := &drivingInjector{settled: "TIP 上屏 2/2 词,词表已用完"}
	if _, err := NewService(d).Type(twoWordPlan()); err != nil {
		t.Fatalf("不该失败:%v", err)
	}
	if d.closedAt != len(d.keys) {
		t.Errorf("Close 发生在第 %d 次按键后,而总共发了 %d 次 —— 收尾早了",
			d.closedAt, len(d.keys))
	}
}

func TestTypeRefusesBadPlanBeforeTouchingDriver(t *testing.T) {
	// 计划本身不合格时连管道都不该起:起了就要等 TIP 连上来(秒级),
	// 而结论早已注定。校验永远在最前面。
	d := &drivingInjector{}
	d.unknownKey = "KeyN"
	if _, err := NewService(d).Type(twoWordPlan()); err == nil {
		t.Fatal("计划里有不认识的键,必须失败")
	}
	if d.gotWords != nil {
		t.Error("校验没过就不该去驱动上屏词")
	}
	if d.closed != 0 {
		t.Error("从没起过会话,不该有收尾")
	}
}

func TestTypeTwiceInARowBothWork(t *testing.T) {
	// **2026-09-01 Windows 真机的回归。** 那天第一条命令全绿(7/7 上屏、回读逐字
	// 相同),第二条立刻失败、整条 6ms、一个键都没发——上一条的收尾把下一条需要的
	// 东西拆了(命名管道被关掉,而 go-winio 用 FILE_CREATE、同名管道有实例就建不回来)。
	//
	// 那个具体故障只在 Windows 上可见,但**它的形状在这里可测**:每条命令各自
	// 起一次会话、各自收尾,后一条不受前一条收尾的影响。
	d := &drivingInjector{settled: "TIP 上屏 2/2 词,词表已用完"}
	s := NewService(d)
	for i := 1; i <= 3; i++ {
		res, err := s.Type(twoWordPlan())
		if err != nil {
			t.Fatalf("第 %d 条命令失败:%v", i, err)
		}
		if res.Keys != 8 {
			t.Errorf("第 %d 条只发了 %d 次按键", i, res.Keys)
		}
		if res.Words == "" {
			t.Errorf("第 %d 条没带回对账结论", i)
		}
		if d.drives != i {
			t.Errorf("第 %d 条之后 DriveWords 调了 %d 次,应为 %d", i, d.drives, i)
		}
		if d.closed != i {
			t.Errorf("第 %d 条之后 Close 调了 %d 次,应为 %d —— 每条命令都要收尾", i, d.closed, i)
		}
	}
}
