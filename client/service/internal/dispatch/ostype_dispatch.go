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

// 文案长度上限。契约的 maxLength 是 512,这里按**字元**再收一道。
//
// 收得更紧不是为了安全,是为了别在开发期探针上跑一分钟:一句 512 字的中文要敲上
// 千次键,而 execBudgetMs 是 180 秒。真机上撞预算比撞契约先发生。
const osTypeMaxRunes = 120

// OsType 派发开发期 OS 打字探针并等待逻辑终局。
//
// 与 OsProbe 同族:intrusive,不铸 effect intent、不落 WAL、不碰任何发送轨状态;
// 携带账号上下文进账号串行域,与该账号的巡检/发送命令互斥——它会接管键鼠一小会儿,
// 不能与别的命令并发操作同一页面现场。
//
// **它打完就停手,不点发送。** 草稿只是页面本地状态,没有任何东西到达候选人。
// 输入框非空时手侧先以真实按键全选删除再打(2026-09-03 裁决撤销 composer.empty),清不空才拒。
//
// **脑侧只有这一个生产者。** 巡检、工作流、沟通 v4 一律不铸它,门禁盯着
// (ostype_producer_test.go)。少了那道门禁,哪天有人顺手在巡检里加一句
// "顺便打一行试试",键盘就会在无人值守的运行里被接管——而键盘比鼠标更坏:
// 鼠标被接管顶多点错地方,键盘被接管会往候选人的输入框里写字。
func (d *Dispatcher) OsType(
	ctx context.Context,
	platform, accountRef, text string,
) (*store.LogicalDispatchState, error) {
	platform = strings.TrimSpace(platform)
	accountRef = strings.TrimSpace(accountRef)
	if platform == "" || accountRef == "" {
		return nil, errors.New("缺少有效的账号标识")
	}
	// 文案**不 TrimSpace**:首尾空白也是要打的内容,替调用方修剪等于替他改文案。
	// 空文案则明确拒绝——契约的 minLength 也是 1,这里先说清楚理由。
	if text == "" {
		return nil, errors.New("文案为空:没有可打的内容")
	}
	if n := utf8.RuneCountInString(text); n > osTypeMaxRunes {
		return nil, errors.New("文案过长:开发期探针上限 " + strconv.Itoa(osTypeMaxRunes) + " 字元,给了 " + strconv.Itoa(n))
	}
	argsRaw, err := protocol.Encode(protocol.DebugOsTypeArgs{Text: text})
	if err != nil {
		return nil, err
	}
	meta := protocol.Primitives[protocol.PrimDebugOsType]
	if err := protocol.ValidatePrimitiveArgs(protocol.PrimDebugOsType, meta.Ver, argsRaw); err != nil {
		return nil, err
	}
	bound, err := d.currentBoundHand(platform, accountRef)
	if err != nil {
		return nil, err
	}
	return d.Run(ctx, bound.request(protocol.PrimDebugOsType, argsRaw))
}
