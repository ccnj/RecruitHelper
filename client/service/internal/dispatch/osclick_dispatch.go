package dispatch

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

const osExpectTextMaxRunes = 256

// OsClick 派发开发期 OS 点击探针并等待逻辑终局。
//
// 与 OsProbe 同族,但它是本战役唯一会点东西的探针:靶子由 selector 指定,点下去对页面
// 做什么由靶子决定——不可逆控件点了就是真副作用。防线在手侧(命中唯一、expectText、
// 点前最后一次命中测试、至多一次点击);脑侧只把 mode 收成白名单、把 selector 原样转交。
// 使用者仍须只对可逆或已裁决的控件用 click 模式:它是考古工具,不是授权。
//
// selector 与 index 的口径见 OsScroll。expectText 不 TrimSpace 之外的改写:手侧与元素
// 文本(去首尾空白)逐字相等才点。
//
// **脑侧只有这一个生产者。** 巡检、工作流、沟通 v4 一律不铸它,门禁盯着
// (osclick_producer_test.go)——它会点页面上的任意元素,业务路径一律不得铸它。
func (d *Dispatcher) OsClick(
	ctx context.Context,
	platform, accountRef, selector string,
	index *int,
	mode protocol.OsClickMode,
	expectText string,
) (*store.LogicalDispatchState, error) {
	platform = strings.TrimSpace(platform)
	accountRef = strings.TrimSpace(accountRef)
	if platform == "" || accountRef == "" {
		return nil, errors.New("缺少有效的账号标识")
	}
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return nil, errors.New("selector 为空:没有可定位的靶子")
	}
	if n := utf8.RuneCountInString(selector); n > osSelectorMaxRunes {
		return nil, errors.New("selector 过长:上限 " + strconv.Itoa(osSelectorMaxRunes) + " 字元,给了 " + strconv.Itoa(n))
	}
	if index != nil && *index < 0 {
		return nil, errors.New("index 不能为负")
	}
	// 模式是白名单,不是"契约里有就能派":将来多一个值意味着新的副作用面,该经一次显式裁决。
	switch mode {
	case protocol.OsClickModeMove, protocol.OsClickModeClick:
	default:
		return nil, errors.New("mode 只能是 move 或 click")
	}
	expectText = strings.TrimSpace(expectText)
	if n := utf8.RuneCountInString(expectText); n > osExpectTextMaxRunes {
		return nil, errors.New("expectText 过长:上限 " + strconv.Itoa(osExpectTextMaxRunes) + " 字元,给了 " + strconv.Itoa(n))
	}
	args := map[string]any{"selector": selector, "mode": mode}
	if index != nil {
		args["index"] = *index
	}
	if expectText != "" {
		args["expectText"] = expectText
	}
	argsRaw, err := protocol.Encode(args)
	if err != nil {
		return nil, err
	}
	meta := protocol.Primitives[protocol.PrimDebugOsClick]
	if err := protocol.ValidatePrimitiveArgs(protocol.PrimDebugOsClick, meta.Ver, argsRaw); err != nil {
		return nil, err
	}
	bound, err := d.currentBoundHand(platform, accountRef)
	if err != nil {
		return nil, err
	}
	return d.Run(ctx, bound.request(protocol.PrimDebugOsClick, argsRaw))
}
