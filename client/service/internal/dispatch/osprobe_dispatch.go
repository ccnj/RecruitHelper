package dispatch

import (
	"context"
	"errors"
	"strings"

	"recruithelper/client/service/internal/store"
	"recruithelper/contract/gen/go/protocol"
)

// OsProbe 派发开发期 OS 注入探针并等待逻辑终局。
//
// 它是 intrusive:不铸 effect intent、不落 WAL、不碰任何发送轨状态。携带账号
// 上下文进账号串行域,与该账号的巡检/发送命令互斥——探针会接管鼠标一小会儿,
// 不能与别的命令并发操作同一页面现场。
//
// **脑侧只有这一个生产者。** 巡检、工作流、沟通 v4 一律不铸它,门禁盯着
// (osprobe_producer_test.go)。少了那道门禁,哪天有人顺手在巡检里加一句
// "顺便探一下",鼠标就会在无人值守的运行里被接管,而且事后从代码里很难一眼看出来。
func (d *Dispatcher) OsProbe(
	ctx context.Context,
	platform, accountRef string,
	target protocol.OsProbeTarget,
) (*store.LogicalDispatchState, error) {
	platform = strings.TrimSpace(platform)
	accountRef = strings.TrimSpace(accountRef)
	if platform == "" || accountRef == "" {
		return nil, errors.New("缺少有效的账号标识")
	}
	// 靶子是白名单,不是"契约里有就能派"。
	//
	//   viewportSpread    只在视口里移几个散开的点,**绝不点击**
	//   reversibleToggle  移到平台指定的一个可逆控件上,过完闸按一下
	//
	// 契约里将来多一个值,这里不改就派不出去 —— 这是刻意的:新靶子意味着新的
	// 副作用面,该经一次显式裁决,而不是随 codegen 顺带生效。
	switch target {
	case protocol.OsProbeTargetViewportSpread, protocol.OsProbeTargetReversibleToggle:
	default:
		return nil, errors.New("未开放的 OS 探针靶子")
	}
	argsRaw, err := protocol.Encode(protocol.DebugOsProbeArgs{Target: target})
	if err != nil {
		return nil, err
	}
	meta := protocol.Primitives[protocol.PrimDebugOsProbe]
	if err := protocol.ValidatePrimitiveArgs(protocol.PrimDebugOsProbe, meta.Ver, argsRaw); err != nil {
		return nil, err
	}
	bound, err := d.currentBoundHand(platform, accountRef)
	if err != nil {
		return nil, err
	}
	return d.Run(ctx, bound.request(protocol.PrimDebugOsProbe, argsRaw))
}
