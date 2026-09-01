package handinput

import (
	"fmt"
	"math"
	"sort"
)

// 打字计划。字段与插件侧排版器的产出逐一对应(`osengine/vendor/compose/planner.d.mts`)。
//
// **本包不生成计划,只播放它** —— 分界见 doc.go 的「数据 / 过程」。这里出现任何
// "算一个时刻""挑一个键"的逻辑,分界就破了。
type TypePlan struct {
	StartTime float64    `json:"startTime"`
	Words     []PlanWord `json:"words"`
}

// PlanWord 是一个上屏单元。
//
// `Direct` 为真表示直接键入(标点),不走 composition,也就没有 `Commit`。
// 其余的走输入法:敲完 `Keys` 里的拼音字母,再按 `Commit` 上屏。
type PlanWord struct {
	Text   string    `json:"text"`
	Direct bool      `json:"direct"`
	Keys   []PlanKey `json:"keys"`
	Commit *PlanKey  `json:"commit"`
	// Splits 是音节边界,**只影响 TIP 组字区的显示**(微软拼音把 nihao 显示成
	// ni'hao,撇号不是按出来的)。本包自己不用,原样转交给 TIP(Windows,第二段)。
	Splits []int `json:"splits"`
}

// PlanKey 是一次按键。`Down`/`Up` 是**相对计划起点的毫秒**,不是间隔。
//
// 相邻键会重叠(rollover):上一个键的 `Up` 常常晚于下一个键的 `Down`,真人快打时
// 就是这样,普通字母键无害。修饰键不同——见 shiftGuardMs。
type PlanKey struct {
	Code     string  `json:"code"`
	Down     float64 `json:"down"`
	Up       float64 `json:"up"`
	Shift    bool    `json:"shift"`    // 本键是在 Shift 按住时按下的
	Modifier bool    `json:"modifier"` // 本键**就是**修饰键
}

// shiftGuardMs:修饰键必须在下一个键按下之前至少这么久松开。
//
// 上游 2026-08-21 真机教训。按键重叠本身不是 bug,但修饰键多按住一会儿,下一个字母
// 就变成大写:实测「？」之后 x 在 Shift 仍按住时按下,输入法收到大写 X 后当成英文,
// **「薪资」变成了「Xin子」**。
//
// down→down 的间隔模型不管上一个键何时松手,所以这条约束**必须由排版器排进计划**——
// 只有它有全局视野知道前后键在哪。本包只负责发现排错了,不负责补救。
const shiftGuardMs = 40.0

type keyEvent struct {
	At   float64
	Code string
	Down bool
}

// ShiftConflict 报告某个修饰键的可用窗口不足:无论怎么发,它都会压到下一个键上。
type ShiftConflict struct {
	NextCode string  // 会被误带成大写的那个键
	NextDown float64 // 它的按下时刻
	ShiftUp  float64 // 修饰键的松手时刻
}

func (c ShiftConflict) String() string {
	return fmt.Sprintf("修饰键松手于 %.0fms,而 %s 在 %.0fms 按下,间隔 %.0fms < %.0fms",
		c.ShiftUp, c.NextCode, c.NextDown, c.NextDown-c.ShiftUp, shiftGuardMs)
}

// keys 摊平出全部按键,含上屏键。
func (p *TypePlan) keys() []PlanKey {
	out := make([]PlanKey, 0, len(p.Words)*6)
	for _, w := range p.Words {
		out = append(out, w.Keys...)
		if w.Commit != nil {
			out = append(out, *w.Commit)
		}
	}
	return out
}

// Flatten 把计划摊成按时刻排序的 down/up 事件,并顺带查修饰键窗口。
//
// **同一时刻的抬起排在按下之前。** 计划里的时刻是毫秒整数,撞点是常事;若把按下
// 排在前面,一个"上一键松手"与"下一键按下"同刻的场面会短暂地同时按住两个键——
// 对普通字母无害,对修饰键就是上面那个大写字母的 bug。
func (p *TypePlan) Flatten() ([]keyEvent, []ShiftConflict) {
	all := p.keys()
	evs := make([]keyEvent, 0, len(all)*2)
	for _, k := range all {
		evs = append(evs, keyEvent{At: k.Down, Code: k.Code, Down: true})
		evs = append(evs, keyEvent{At: k.Up, Code: k.Code, Down: false})
	}
	sort.SliceStable(evs, func(i, j int) bool {
		if evs[i].At != evs[j].At {
			return evs[i].At < evs[j].At
		}
		return !evs[i].Down && evs[j].Down
	})

	var conflicts []ShiftConflict
	for _, m := range all {
		if !m.Modifier {
			continue
		}
		// 找修饰键松手之后**最早**按下的那个键。它是唯一有风险的:再往后的更远。
		next := math.Inf(1)
		code := ""
		for _, k := range all {
			if k.Code == m.Code && k.Down == m.Down {
				continue // 修饰键自己
			}
			if k.Down >= m.Up && k.Down < next {
				next, code = k.Down, k.Code
			}
		}
		if code != "" && next-m.Up < shiftGuardMs {
			conflicts = append(conflicts, ShiftConflict{NextCode: code, NextDown: next, ShiftUp: m.Up})
		}
	}
	return evs, conflicts
}

// Validate 在发出任何一次按键**之前**把计划否掉的那些理由。
//
// 只查本包管得着的:键码认不认识、修饰键窗口够不够。**不查"像不像人"** ——
// 那是排版器闭环自验的事,而且它用的判据在插件那边(criteria.mjs)。
func (p *TypePlan) Validate(known func(string) error) error {
	if len(p.Words) == 0 {
		return fmt.Errorf("空计划")
	}
	all := p.keys()
	if len(all) == 0 {
		return fmt.Errorf("计划里一个按键都没有")
	}
	for _, k := range all {
		if k.Up < k.Down {
			return fmt.Errorf("按键 %s 的松手(%.0fms)早于按下(%.0fms)", k.Code, k.Up, k.Down)
		}
		if err := known(k.Code); err != nil {
			return err
		}
	}
	if _, conflicts := p.Flatten(); len(conflicts) > 0 {
		// 显式报,不静默出错字。这属于排版器该重采的情形,注入层补救不了——
		// 它没有全局视野,挪谁都会连累别人。
		return fmt.Errorf("修饰键窗口不足(%d 处),第一处:%s;这属于排版器该重采的情形",
			len(conflicts), conflicts[0])
	}
	return nil
}
