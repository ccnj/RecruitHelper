# BOSS 真机调试必看（2026-09-03）

开新窗口在 BOSS 上做考古或调试，先读完这一页。它只讲**怎么读、怎么动、什么时候停**，细节全部链接出去，不在这里复制。

## 0. 一句话原则

**读用 Claude Chrome 插件，动只走 OS 注入。** 任何在 BOSS 页面上"发生"的事——点、滚、打字——都必须是真实鼠标键盘做出来的，经客户端脑 → 我方插件 → 手服务那条生产链。Claude Chrome 插件的点击、输入、导航一律不用：它经 CDP 合成事件，没有 OS 轨迹，2026-09-03 当天两次风控都是它触发的。

## 1. 开工前三查

1. **脑是谁拉起的、账本是哪个。** 客户端由 Electron 用 `go run` 从主检出拉起，`ps -o command= -p $(pgrep -f "/service -port 17872 -data" | head -1)` 看 `-data` 指向哪个目录（开发期 BOSS 账本是 `data-boss*`）。admin token 在脑进程环境里：
   ```bash
   ps eww $(pgrep -f "/service -port 17872 -data" | head -1) | tr ' ' '\n' | grep '^RECRUITHELPER_ADMIN_TOKEN='
   ```
   脑不能由会话手起的替换（产品 bearer 与它配对），换代码要重启客户端；重启套路见记忆 `client-relaunch-brain-data-dir`。
2. **插件是否换代到当前契约。** `GET /admin/hands/health` 看 `contractMatch=true`、`caps` 里有 `debug.osScroll@1` 与 `debug.osClick@1`。脑启动时契约变了会自动派 `debug.reload`；纯逻辑改动不动契约时要显式 `POST /admin/hands/reload`（JSON 头 + `{}`）。
3. **账号身份是否在当前手会话。** 插件换代后 `GET /admin/accounts` 的 `identityCurrent` 变 false，所有带账号的命令都会拒（`账号身份未在当前手会话验证`）。用同一个 accountRef 重绑一次即可，它只跑 `probe.platform`、刷新身份会话，**不开巡检**：
   ```bash
   curl -s -X POST -H "Authorization: Bearer $ADMIN" -H 'Content-Type: application/json' \
     -d '{"platform":"boss","handId":"<handId>","accountRef":"<accountRef>"}' http://127.0.0.1:17872/admin/accounts/bind
   ```
4. **BOSS 页必须是 Chrome 的激活标签。** 前台闸看的是"平台页是当前标签且可见"，Chrome 在最前面但激活的是别的标签也会拒（等 20 秒后报 `标签激活=否`）。切标签：
   ```bash
   osascript -e 'tell application "Google Chrome" to activate' -e 'tell application "Google Chrome" to repeat with w in windows' -e 'set i to 0' -e 'repeat with t in tabs of w' -e 'set i to i + 1' -e 'if URL of t contains "zhipin.com" then' -e 'set active tab index of w to i' -e 'set index of w to 1' -e 'return' -e 'end if' -e 'end repeat' -e 'end repeat' -e 'end tell'
   ```
5. **手服务里可能残留上一块屏的标定。** 脑进程不重启，手服务的标定就一直在；窗口换了显示器（09-04：08-28 在副屏学到的 OffsetX≈2560 留到了主屏），每趟都把光标推出屏幕右缘，页面只观察到光标被钉在边上的入口点，冷启动三趟都报同一个落点、永不收敛。判据：`POST /handinput/state`（JSON `{}`）看 `cursorCssX/Y` 荒谬（负几千或超视口）且 `samples>0 residualPx` 很大。解法一条：`POST /handinput/reseed` 带当前窗口粗估 `{"hint":{"screenX":0,"screenY":33,"dpr":2}}`（`screenX/Y` 取 `window.screenX/Y`），随后第一条 move 走冷启动两趟即收敛。

## 2. 读：Claude Chrome 插件

四种读法都允许：**页面文本、可访问树、截图、页面内存**（javascript_tool 跑在页面上下文，Vue 实例、`list$`、`conversation$`、`message-list` 的数组都读得到，08-28 那轮形状事实就是这么来的）。四条边界：

1. **只读表达式，不发请求。** 不 fetch、不 XHR，不调页面 SDK 里会打接口的方法——BOSS 上连经 SDK 拉历史都是 08-28 裁决明令禁止的，08-29 的安全验证就是四十来次页面 origin 请求触发的。
2. **不留全局变量。** 只写表达式，不 `var`、不往 `window` 挂东西。BOSS 风控把 `window` 上的未知键名原样上送（码 800001 的 p6）。
3. **凭据不落纸。** `user$` 旁边挨着 token、wt、手机号。只取要的那几个字段（如 userId 的形状），不整块 dump，不进文档、不进聊天记录。
4. **读不改。** 不给响应式对象赋值，不调组件方法。改状态就是"动"，动只走 OS。

两条附注：
- **调试器横幅会挪页面，高 56 CSS px。** 插件挂上调试器时标签页顶部出一条横幅，页面内容整体下移（2026-09-04 实测：插件读页面时视口高 746，不读时 802）。OS 注入的标定是从落点学出来的，横幅出现或消失，下一次落点就偏这么多——闸会拦住、连拒两次就把标定作废，冷启动重学还要白跑一两条命令。横幅在插件读完十几秒后会自己撤掉，所以**每次插件读完等 30 秒再派 OS 动作**，让动作全在无横幅状态下跑；09-04 照此做，后续四次点击偏差全为 0。
- **把 BOSS 标签拖进 Claude 分组的新窗口，旧标定随即作废。** 插件只能看见自己那个标签分组里的页面，开工要把 BOSS 标签拖进去，窗口几何一变，热路径第一条动作会打偏几百像素、闸自动重置，再一两条 move 才学回来。做法：拖完先派一条 move 让它学完，再开始正式动作。
- **同一时间只能有一个人在页面上动。** 诊断台的 osProbe 与 Claude 的 osClick 共用同一套标定与同一账号串行域。09-04 甲方在 Claude 两条命令之间跑了一次诊断台鼠标测试（默认靶「收藏」页签，列表被切空），Claude 下一条命令定位就失败。要插手先打招呼，动完让 Claude 重读页面。
- **CDP 挂调试器本身会不会被 BOSS 识别，尚无实证。** 已实证的触发只有两种：HTTP 节奏、非 isTrusted 输入。所以是"可以用，出问题先看埋点"，不是"绝对安全"。

生产只读原语（读列表、读会话等）**不经诊断台派**：`/admin/cmd` 只收 `debug.*`。它们是巡检的事，考古不借用。

## 3. 动：三条 OS 探针

鉴权同一把 admin bearer，账号取 `GET /admin/accounts`。响应 `{msgId,status,result}`，`result.data.outcome` 是收场，`detail` 是判定现场。参数与收场全表见 [`考古探针-滚轮与点击-2026-09-03.md`](考古探针-滚轮与点击-2026-09-03.md)。

**osClick：落到元素上（不点）或落上去按一次。**
```bash
curl -s -X POST -H "Authorization: Bearer $ADMIN" -H 'Content-Type: application/json' -d \
'{"platform":"boss","accountRef":"<accountRef>","selector":".geek-item","index":1,"mode":"move","expectText":"…"}' \
http://127.0.0.1:17872/admin/osclick
```
`mode` 为 `move` 只落不点；`click` 至多按一次。`index` 不给时命中不唯一就拒，不猜第一个；`expectText` 给了就与元素文本（去首尾空白）逐字相等才动，点前最后一次命中测试再核一遍。

靶子在同源 iframe 里时,selector 写 `iframe选择器 >>> 内层选择器`(只支持一层),两条探针都认;推荐页整张列表画在 `iframe[name=recommendFrame]` 里,例如 `iframe[name=recommendFrame] >>> button.btn-greet`,滚它的文档本身用 `iframe[name=recommendFrame] >>> html`(2026-09-04)。

**osScroll：把容器朝一个方向滚指定像素，每簇回读 scrollTop。**
```bash
curl -s -X POST -H "Authorization: Bearer $ADMIN" -H 'Content-Type: application/json' -d \
'{"platform":"boss","accountRef":"<accountRef>","selector":".user-list","direction":"down","distancePx":600}' \
http://127.0.0.1:17872/admin/osscroll
```
`scrolled` 滚够；`edge` 到顶/到底或没有可滚内容；`stuck` 纹丝不动或方向反了（如实报，不换方向）。已钉的容器：会话列表 `.user-list`，消息区 `.chat-message-list`（到顶带 `is-to-top` 类）。Mac 一格 120px。

**osType：把一句中文打进输入框然后停手。** `POST /admin/ostype {"platform","accountRef","text"}`，不点发送；框里已有内容先以真实按键全选删除再打。

通用纪律：
- **一步一动、动完必读。** 每次只动一个靶子，动完用插件读一次或截一张再决定下一步。
- **只点可逆或已裁决的控件。** 探针是考古工具，不是对发送、确认、删除的授权。
- **靶子不在视口先滚。** 可见部分不足 16px 会拒(2026-09-04 自 24 放宽)。
- **不重试。** 被拒就看 detail 里的原因，改法，不是再来一次。
- **相邻动作留人的节奏。** 探针内部已带 1～1.8 秒间隔与拟人轨迹，外层不要连珠炮。

## 4. 一次考古的标准节奏

1. 先翻 [`BOSS平台事实-2026-08-28.md`](BOSS平台事实-2026-08-28.md) 与相关裁决底稿：这个事实是不是已经钉过。
2. 三查过一遍（第 1 节）。
3. 用插件读当前页面：形状、selector、内存态。能读到的不动。
4. 只在"页面必须先露出这个事实"时才动：切页签、点开面板、滚到底加载历史。一动一读。
5. 事实落账（第 6 节）。做过的动作与拒绝原因保留在账本（`GET /admin/ledger` 最近 50 条，含 `resultBody`）。

## 5. 什么时候停

出现任一即停手、报告，不绕：
- 插件埋点观测页（扩展 `options/telemetry.html`）结论区「踩雷」行出现新码，或诊断台「插件能力测试页」的观测卡报高风险码。
- 页面跳到安全验证页。**不许由机器过验证**。
- 掉登录、身份指纹变了。
- 同一靶子闸连续拒两次（遮挡、落点不被接受）：不是"再试一次"的信号，是"有东西盖着"的信号。

## 6. 落账规则

- 页面事实（selector、DOM 结构、内存形状、状态跃迁）写进 `BOSS平台事实`，标日期与取得手段（插件读 / 探针动）。
- 需要裁决的写底稿，不在事实文档里夹私货。
- 仓库文档不带候选人 PII；探针 detail 里的元素文本头几个字若含人名，落文档时抹掉。
- 拒绝原因、耗时、每簇观测这类判定现场留在账本与手侧日志，不要只记结论。

## 7. 红线复述

- 不用 CDP 点击、输入、导航；动只走 OS 注入。
- 插件不以自身身份对平台发任何请求；Claude 的插件读也不发请求。
- 不过安全验证。
- 只在甲方指定的测试账号上跑；批次运行期间不刷新推荐页、不换代插件。

## 8. 相关文档

- [`考古探针-滚轮与点击-2026-09-03.md`](考古探针-滚轮与点击-2026-09-03.md)：三条探针的落点、参数、收场、真机记录。
- [`BOSS平台事实-2026-08-28.md`](BOSS平台事实-2026-08-28.md)：已钉的页面事实。
- [`BOSS取数通道决策-2026-08-28.md`](BOSS取数通道决策-2026-08-28.md)：读什么、从哪读的裁决。
- [`场景一实现说明-2026-09-03.md`](场景一实现说明-2026-09-03.md)：七条会话原语怎么分工。
- [`BOSS输入检测实测-2026-08-29.md`](BOSS输入检测实测-2026-08-29.md)：为什么必须是真实输入。
