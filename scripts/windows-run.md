# Windows 开发验证包 · 构建与运行

本文件覆盖"**开发验证包**":能在一台 Windows 机器上把客户端跑起来做人眼验收。
它**不是**正式客户交付物 —— 插件仍靠人工加载,没有代码签名、安装器与自动更新。
正式交付还欠的义务见
[`docs/插件交付与更新决策-2026-07-25.md`](../docs/插件交付与更新决策-2026-07-25.md) 第三节。

## 一、在 macOS 上构建

```bash
./scripts/build-win.sh
```

一条命令做四件事:交叉编译脑服务(`CGO_ENABLED=0 GOOS=windows GOARCH=amd64`)、
构建 UI、构建插件、打 Electron 壳,最后核对产物完整性。产物在:

```
release/win-unpacked/
├── RecruitHelper.exe          ← 双击这个
└── resources/
    ├── brain/service.exe      ← Go 脑服务(单二进制,无 cgo)
    ├── ui/                    ← React 构建产物
    └── plugin/                ← Chrome MV3 插件(需人工加载)
```

首次构建会下载 Windows 版 Electron(约 100MB),慢或失败重跑即可。
`target=dir` 不需要 wine。

## 二、拷到 Windows 并启动

1. 把**整个** `win-unpacked` 目录拷到 Windows 机器(目录内文件互相依赖,不能只拷 exe);
2. 双击 `RecruitHelper.exe`。

**首次会被 SmartScreen 拦截**(包未签名):点"更多信息" → "仍要运行"。

启动后壳会拉起 `resources\brain\service.exe`、等 `admin/health` 就绪、再开窗。
若包不完整(缺脑二进制),会**当场弹窗报错并退出**,不会留一个开了窗却没有脑的壳。

## 三、装插件(人工,一次)

```
chrome://extensions → 右上"开发者模式" → "加载已解压的扩展程序"
→ 选 win-unpacked\resources\plugin
```

插件自己生成并保存 `handId`,连上本地脑后自动登记,不需要配对。
客户端 UI 的手区应显示该手在线。

> 注意:`resources\plugin` 是随包目录,重新拷包会覆盖它。正式交付要求的固定目录、
> 暂存→原子替换、保留上一版等能力**本包没有**,更新插件目前只能重新加载。

## 四、数据目录

打包态固定用 Electron 标准目录,**不认** `BRAIN_DATA_DIR`:

```
%APPDATA%\RecruitHelper\data
```

这条是硬的 —— 装到机器上的包不该因为一个残留环境变量去写另一份业务库。
开发态(`pnpm start`)仍可用 `BRAIN_DATA_DIR` 覆盖,见
[`scripts/dev-run.md`](dev-run.md)。

## 五、业务运行窗口

正式规则照常:每天 `08:00～24:00`,由用户在客户端显式点开始/恢复。
本包**不内置** `RECRUITHELPER_DEV_ALLOW_OUT_OF_WINDOW`。有人值守验收确需放开时间门,
只能在启动时于命令行显式带上:

```bat
set RECRUITHELPER_DEV_ALLOW_OUT_OF_WINDOW=1 && RecruitHelper.exe
```

该开关只在脑启动时读取,不改系统时间、不改业务时间戳、不自动开跑任务。
恢复正式规则必须**完整退出**(托盘右键"退出",不是关窗口)后不带变量重启。

## 六、维护入口

窗口内按 `Ctrl+Shift+D` 进出开发者诊断台(普通用户侧边栏不显示该入口)。

## 七、已知限制

| 限制 | 现状 |
|---|---|
| 代码签名 | 无,SmartScreen 每台机器首次需人工放行 |
| 安装器 | 无,拷目录即用 |
| 自动更新 | 无,换版本 = 重新拷包 |
| 插件固定目录与原子替换 | 未实现,人工加载 |
| 开机自启 | 无 |
