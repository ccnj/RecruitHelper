# Windows 开发验证包 · 构建与运行

本文件覆盖"**开发验证包**":能在一台 Windows 机器上把客户端跑起来做人眼验收。
它**不是**正式客户交付物 —— 没有代码签名,也没有自动更新;插件文件虽然已经迁到
安装目录之外的固定目录并由客户端自动换代,但**在 Chrome 里加载/重载仍靠人工**。
正式交付还欠的义务见
[`docs/插件交付与更新决策-2026-07-25.md`](../docs/插件交付与更新决策-2026-07-25.md) 第三节。

## 一、在 macOS 上构建

```bash
./scripts/build-win.sh
```

一条命令做五件事:交叉编译脑服务(`CGO_ENABLED=0 GOOS=windows GOARCH=amd64`)、
构建 UI、构建插件、打 Electron 壳(dir)、用 `makensis` 编译安装器,最后核对产物。

```
release/
├── RecruitHelper-0.1.0-setup.exe   ← 交付用,约 96MB
└── win-unpacked/                    ← 免安装目录,调试用,约 306MB
    ├── RecruitHelper.exe
    └── resources/
        ├── brain/RecruitHelperBrain.exe   ← Go 脑服务(单二进制,无 cgo)
        ├── ui/                            ← React 构建产物
        └── plugin/                        ← 插件母版(不是 Chrome 加载的那份,见第四节)
```

全程不需要 Docker、不需要 wine。首次构建会下载 Windows 版 Electron(约 115MB)。

> **构建工具链的两个坑**(改脚本前务必先读):
> 1. **别用 Homebrew 的 makensis。** 3.12 在 macOS 26 / arm64 上是坏的 —— Section 里
>    只要有 `File`、`WriteUninstaller`、`ExecWait` 任一实质指令就 `std::bad_alloc`
>    退出,且不打印任何有用错误。脚本刻意优先用 electron-builder 缓存里的 3.04。
> 2. **[`installer.nsi`](installer.nsi) 必须保持纯 ASCII,注释也是。** macOS 的
>    makensis 是 ANSI-only 构建,脚本里出现任何非 ASCII 字节都会
>    `Bad text encoding: line 1`,`-INPUTCHARSET UTF8` 和 UTF-8 BOM 都救不了。
>    产品 UI 文案照常中文,只有安装器受限于程序标识 `RecruitHelper`。

## 二、安装

把 `RecruitHelper-0.1.0-setup.exe` 拷到 Windows,双击。

- 装到 `%LOCALAPPDATA%\Programs\RecruitHelper`,**用户级安装,没有 UAC 提权弹窗**;
- 桌面与开始菜单会有 `RecruitHelper` 快捷方式,控制面板"应用和功能"里可卸载;
- **首次会被 SmartScreen 拦截**(未签名):点"更多信息" → "仍要运行"。

升级 = 直接装新版:安装器会先结束运行中的 `RecruitHelper.exe` 与
`RecruitHelperBrain.exe`,清掉旧文件再写入。脑被结束后,进行中的 WAL、未收束命令与
suspect 判定走既有恢复轨在下次启动时收敛 —— 安装器不为此另开旁路。

**卸载不会删业务数据与插件目录**,重装即可续用。要彻底清除得手工删
`%APPDATA%\recruithelper-client` 与 `%LOCALAPPDATA%\RecruitHelper`。

调试时也可以直接拷 `win-unpacked` 整个目录跑,不装。

## 三、首次在新机器上:五步

包里带的是壳、脑、UI、插件文件。**装完不等于能开工**,按顺序走完下面五步;
其中 4、5 两步已经自动化,人工要做的只有装 Chrome、加载插件和输入激活码。

### 1. Chrome 浏览器(前提,包里没有)

手跑在用户自己的 Chrome 里,包不携带也不该携带浏览器。机器上必须先装好 Chrome。

### 2. 加载插件(人工,一次)

**先启动一次客户端**,让它把插件安置到固定目录(见第四节),然后:

```
chrome://extensions → 右上"开发者模式" → "加载已解压的扩展程序"
→ 选 %LOCALAPPDATA%\RecruitHelper\plugin
```

注意选的是 `%LOCALAPPDATA%` 下那份,**不是**安装目录里的 `resources\plugin`
(那是母版,升级时会被整体替换)。

插件自己生成并保存 `handId`,连上本地脑后自动登记,不需要配对。
客户端 UI 的手区应显示该手在线。

### 3. 激活(产品 UI,2026-08-12 起不再填后台地址)

客户端激活页输入管理员提供的一次性激活码,绑定本机。后台地址内置在客户端代码里
(`jobconfig.DefaultBaseURL`),激活页没有地址输入项;开发要指向假后台时直接调
`/admin/job-config/activate` 并显式带 `base_url`。激活码只在本次表单内存中,
不落盘;Chrome 插件不接触激活码。

激活成功会**顺带同步职位配置与模型连接**(见下一步),不需要另外操作。

### 4. 模型连接(自动,2026-07-30 起不再手填)

`base_url`、API key 与 `model` 随旧后台 job-config 响应下发,写入
`%APPDATA%\recruithelper-client\data\llm-provider.json`(0600)。凡是客户端拉过一次
职位配置就会顺手刷新——激活时、产品页"同步职位"、产品页点"开始"都算。

**但它生效于下次脑启动**:AI 建议层是脑启动时一次性装配的,不做运行期热替换。
所以首次装机的顺序是:激活 → **完整退出并重启客户端一次** → AI 环节可用。

只有后台没配或值不合法(例如端点不是 https)时才需要人工兜底:
`Ctrl+Shift+D` 进诊断台 → "模型连接" → 填地址与 key。**手填的值会在下一次同步
职位配置时被后台下发的值覆盖**,诊断台文案已写明这点。key 只存在本机该文件里,
不进日志、审计、管理响应与 AI 请求正文。

### 5. 开始使用(账号自动识别,2026-07-30 起无需绑定)

在 Chrome 里**登录智联招聘端**(rd6.zhaopin.com,不是求职端),回客户端点"开始"。
点下去那一刻脑会探测当前登录的账号并自动建档绑定,然后进入采集——不需要进
诊断台做任何账号操作。

开始失败时按提示自助处理,常见三种:Chrome 没开或插件没加载("插件未连接")、
开了两个装插件的 Chrome("多个在线插件")、没登录或登的是求职端("请先登录
智联招聘端")。

**换账号**:结束当前工作流 → Chrome 里切号 → 再点"开始"。每个账号各有独立的
会话与候选人数据,互不混淆;切号期间旧账号的沟通巡检会因身份不符自动停下,
切回去即恢复。误登录过的账号会在本机留下一条空账号记录,无害,不用清理。

产品页在两次开始之间显示的是**最近一次验证过的账号**;刚切号还没点开始时,
页面可能短暂显示上一个号,点"开始"后即以实际登录的账号为准。

## 四、插件目录与扩展身份

Chrome 实际加载的是这个固定目录,它在安装目录**之外**:

```
%LOCALAPPDATA%\RecruitHelper\plugin
```

客户端每次启动会比对随包母版与该目录的内容指纹,不一致就先写临时目录、校验
manifest、再原子替换;替换失败保留旧版并记录,不阻断启动(此时插件版本落后,既有
contractMatch 会挡住 effectful 派发,不会带着错版插件发出副作用)。

**只在启动那一刻替换** —— 那时还没有任何批次在跑,天然落在业务安全窗口内。运行期间
的暂存、延迟重试与 `debug.reload` 握手不在本轮范围。

插件 manifest 里写死了公钥 `key`,扩展 ID 恒定为:

```
oankodckocoibcofboconjjeinpjpdnb
```

ID 不再随加载路径变化,以后挪目录也不会换身份、不会丢 `handId`。

> **从旧的免安装目录版升级过来时,扩展 ID 会变一次**(此前由路径派生)。需要在
> `chrome://extensions` 删掉旧扩展、重新加载新目录一次;`handId` 会重新生成,脑那边
> 旧手成为僵尸条目。一次性成本,之后不再发生。

**插件文件被替换后 Chrome 不会自动生效**,仍需人工在 `chrome://extensions` 点重载。

## 五、数据目录

打包态固定用 Electron 标准目录,**不认** `BRAIN_DATA_DIR`:

```
%APPDATA%\recruithelper-client\data
```

这条是硬的 —— 装到机器上的包不该因为一个残留环境变量去写另一份业务库。
开发态(`pnpm start`)仍可用 `BRAIN_DATA_DIR` 覆盖,见
[`scripts/dev-run.md`](dev-run.md)。

> **三个目录名不一致,是有意留着的,别去"统一"。**
>
> | 用途 | 目录 | 名字来自 |
> |---|---|---|
> | 安装位置 | `%LOCALAPPDATA%\Programs\RecruitHelper` | electron-builder 的 `build.productName` |
> | 业务库与日志 | `%APPDATA%\recruithelper-client\` | `app.getName()`,读 package.json 顶层 `name` |
> | 插件目录 | `%LOCALAPPDATA%\RecruitHelper\plugin` | pluginSeed.js 里硬编码 |
>
> 想让业务库也叫 `RecruitHelper`,只需给 package.json 加个顶层 `productName` ——
> 但那会让**已经激活、已同步职位、已绑定账号的现有安装指向一个空目录**,等于全部
> 重来。命名整齐不值这个代价。

## 六、业务运行窗口

正式规则照常:每天 `08:00～24:00`,由用户在客户端显式点开始/恢复。
本包**不内置** `RECRUITHELPER_DEV_ALLOW_OUT_OF_WINDOW`。有人值守验收确需放开时间门,
只能在启动时于命令行显式带上:

```bat
set RECRUITHELPER_DEV_ALLOW_OUT_OF_WINDOW=1 && RecruitHelper.exe
```

该开关只在脑启动时读取,不改系统时间、不改业务时间戳、不自动开跑任务。
恢复正式规则必须**完整退出**(托盘右键"退出",不是关窗口)后不带变量重启。

## 七、维护入口与日志

窗口内按 `Ctrl+Shift+D` 进出开发者诊断台(普通用户侧边栏不显示该入口)。

脑的运行日志(slog + GORM,也就是开发态在终端里看到的那些)落在:

```
%APPDATA%\recruithelper-client\logs\brain.log
```

打包后客户端是 GUI 进程、**没有控制台**,不落盘就什么都留不下 —— 现场排查先看这个
文件。每次启动会写一行 `=== 启动 <ISO 时间> ===` 作分隔。文件超过 32MB 时,下次启动
会把它改名为 `brain.log.old`,**只留一代**,再旧的被顶掉。日志写不了只降级为仅控制台,
不会拦停客户端启动。

内容边界与别处一致:不含 API key、聊天正文、简历正文、完整 prompt 或候选人明文身份。

## 八、已知限制

| 限制 | 现状 |
|---|---|
| 代码签名 | 无,SmartScreen 每台机器首次需人工放行 |
| 自动更新 | 无,换版本 = 发新 setup.exe 装一次 |
| 插件在 Chrome 里的重载 | 人工,`debug.reload` 自动握手未接 |
| exe 版本元数据 | 无(跳过了 rcedit,它在 arm64 上要 wine) |
| 安装器界面语言 | 英文(makensis 的 ANSI-only 限制) |
| 开机自启 | 无 |
