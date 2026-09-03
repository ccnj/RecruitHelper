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

// 考古探针的 selector 上限与滚动距离上限,与契约同数;这里先说清楚理由再交契约校验。
const (
	osSelectorMaxRunes = 256
	osScrollMaxPx      = 20000
)

// OsScroll 派发开发期 OS 滚轮探针并等待逻辑终局。
//
// 与 OsProbe 同族:intrusive,不铸 effect intent、不落 WAL、不碰任何发送轨状态;
// 携带账号上下文进账号串行域,与该账号的巡检/发送命令互斥——它会接管鼠标一小会儿。
//
// **selector 在脑侧完全不透明**:不解释、不核对、不落任何业务表,原样交给手。它进
// args 是 debug.* 命名空间的显式例外(契约 note),生产原语不得照抄。index 为 nil 表示
// "没给",手侧命中不唯一即拒、不猜第一个;所以 0 与 nil 必须分得开——生成的 struct 用了
// omitempty,0 会被吞掉,这里改用 map 编码。
//
// **脑侧只有这一个生产者。** 巡检、工作流、沟通 v4 一律不铸它,门禁盯着
// (osscroll_producer_test.go)。
func (d *Dispatcher) OsScroll(
	ctx context.Context,
	platform, accountRef, selector string,
	index *int,
	direction protocol.OsScrollDirection,
	distancePx int,
) (*store.LogicalDispatchState, error) {
	platform = strings.TrimSpace(platform)
	accountRef = strings.TrimSpace(accountRef)
	if platform == "" || accountRef == "" {
		return nil, errors.New("缺少有效的账号标识")
	}
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return nil, errors.New("selector 为空:没有可定位的容器")
	}
	if n := utf8.RuneCountInString(selector); n > osSelectorMaxRunes {
		return nil, errors.New("selector 过长:上限 " + strconv.Itoa(osSelectorMaxRunes) + " 字元,给了 " + strconv.Itoa(n))
	}
	if index != nil && *index < 0 {
		return nil, errors.New("index 不能为负")
	}
	switch direction {
	case protocol.OsScrollDirectionDown, protocol.OsScrollDirectionUp:
	default:
		return nil, errors.New("direction 只能是 down 或 up")
	}
	if distancePx < 1 || distancePx > osScrollMaxPx {
		return nil, errors.New("distancePx 必须在 1~" + strconv.Itoa(osScrollMaxPx) + " 之间")
	}
	args := map[string]any{"selector": selector, "direction": direction, "distancePx": distancePx}
	if index != nil {
		args["index"] = *index
	}
	argsRaw, err := protocol.Encode(args)
	if err != nil {
		return nil, err
	}
	meta := protocol.Primitives[protocol.PrimDebugOsScroll]
	if err := protocol.ValidatePrimitiveArgs(protocol.PrimDebugOsScroll, meta.Ver, argsRaw); err != nil {
		return nil, err
	}
	bound, err := d.currentBoundHand(platform, accountRef)
	if err != nil {
		return nil, err
	}
	return d.Run(ctx, bound.request(protocol.PrimDebugOsScroll, argsRaw))
}
