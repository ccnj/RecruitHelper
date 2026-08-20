package productapp

import (
	"errors"

	"recruithelper/client/service/internal/workflow"
)

// StartFailureText 只把已知业务哨兵映射为固定文案；未匹配的错误一律保持
// 笼统提示，底层错误链的细节不得进入产品面响应。产品 HTTP 面与每日自动
// 开始共用同一张表,两条路径给用户的解释必须一致。
func StartFailureText(err error) string {
	switch {
	// 全新开始已经改为跟随后台当前职位,不再因职位变化拒绝。这个哨兵此后只剩
	// 一种可达来路:有未完成的任务,而它锚定的职位与后台当前职位不是同一个。
	// 原文案"请刷新后重试"对这种情形是条死路——刷新只会把页面职位更新成后台
	// 那个,再点开始仍被同一道闸拦下。真正的出路是先结束本次任务。
	case errors.Is(err, ErrJobSelectionChanged):
		return "当前有未完成的任务，它绑定的职位与后台当前职位不同；要换职位请先结束本次任务"
	case errors.Is(err, workflow.ErrDailyWindowClosed):
		return "当前不在业务运行窗口内"
	case errors.Is(err, ErrAccountUnavailable):
		return "没有可运行的平台账号"
	case errors.Is(err, ErrHandUnavailable):
		return "Chrome 插件未连接，请确认 Chrome 已打开并加载插件后重试"
	case errors.Is(err, ErrHandAmbiguous):
		return "检测到多个在线插件，请只保留一个装有插件的 Chrome"
	case errors.Is(err, ErrLoginRequired):
		return "请先在 Chrome 中登录智联招聘端，再点击开始"
	case errors.Is(err, ErrJobConfigUnavailable):
		return "当前职位配置不可用"
	case errors.Is(err, ErrWechatNotConfigured):
		return "尚未在智联个人中心配置微信号，请到智联招聘端「个人中心」填写微信号后再开始"
	case errors.Is(err, ErrWechatCheckFailed):
		return "微信号配置检查未完成，请稍后重试"
	}
	return "当前状态无法启动工作流"
}
