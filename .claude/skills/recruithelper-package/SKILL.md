---
name: recruithelper-package
description: 打包"招聘助手"(RecruitHelper)的 Windows 安装包 —— 升版本号、跑构建、核对产物、给出交付给客户前的注意事项。凡是甲方提到出安装包、打个包给客户、发新版本、升版本号、重新打包、setup.exe、给 Windows 用的包,或者改完代码要把新版本装到客户机上,都用这个 skill,不要自己去拼构建命令。注意本 skill 只服务本仓库这个新产品,与旧工作区 AutoZhilian 的 program/client 发版(zhilian-release)完全无关,别混用。
---

# 打包招聘助手 Windows 安装包

产出一个 `RecruitHelper-<版本>-setup.exe`,拷到 Windows 双击即装。全程在 macOS/Linux
本地完成,不需要 Docker、不需要 wine。

## 一、动手前先确认基线

打包打的是**当前工作树**,不是某个 commit。所以先确认站对了地方:

```bash
git branch --show-current && git log --oneline -1 && git status --short
```

- 通常应该在 `main` 上打包。如果在别的分支,先跟甲方确认这是有意的;
- **工作区有未提交改动时要问清楚**——那些改动会被打进包里,而包发出去之后无法从
  commit 追溯它到底装了什么;
- 别人的未跟踪文件(`plugin/dist.pre-*` 之类的备份)不影响构建,不要擅自清理。

## 二、升版本号(要升才做)

客户端与插件**永远同号**,一条命令改全:

```bash
./scripts/bump-version.sh 0.2.0
```

它会改五处 `version`,其中只有两处真正被消费:`client/electron/package.json`
(决定 setup.exe 文件名与控制面板里的 DisplayVersion)和 `plugin/manifest.json`
(插件上报给脑,显示在诊断台)。另外三处纯粹是为了不在同一个仓库里留下三个互不
相同的版本号。

版本号只接受 **1–4 段纯数字**;`0.2.0-beta` 这类后缀会让 Chrome 拒绝加载扩展,
脚本会直接挡下。

改完 `git diff` 过目再提交——脚本不碰 git,提交与否是甲方的决定。

> 插件版本一变,`manifest.json` 内容就变,插件目录指纹随之改变,客户端下次启动会
> 自动把新版换代到固定目录。这是设计内的行为,不需要额外操作。

## 三、打包

```bash
./scripts/build-win.sh
```

五步:交叉编译脑服务(`CGO_ENABLED=0 GOOS=windows GOARCH=amd64`)→ 构建 UI →
构建插件 → electron-builder 出 dir 包 → `makensis` 编译安装器。脚本末尾会自己核对
产物完整性,任一项缺失就非零退出。

产物:

```
release/RecruitHelper-<版本>-setup.exe   交付用,约 91MB
release/win-unpacked/                    免安装目录,调试用,约 306MB
```

首次构建会下载 Windows 版 Electron(约 115MB)。`release/` 是 gitignore 的。

## 四、核对产物

脚本已经查过四个关键文件和插件 `key`。交付前再补两项:

```bash
file release/RecruitHelper-*-setup.exe
# 期望:PE32 executable (GUI) ... Nullsoft Installer self-extracting archive

(cd release/win-unpacked/resources && npx --yes asar list app.asar)
# 期望:layout.js / main.js / package.json / pluginSeed.js / preload.js / service.js
```

asar 那项值得每次都看——`files` 用的是 `*.js` glob,漏掉任一模块的后果是客户端
装上去起不来,而这只有在 Windows 上才暴露。

## 五、交付给甲方时要说的话

包本身不是全部,这几条每次都要交代,漏一条就会有一轮来回:

1. **装之前从托盘退出旧客户端**(不是关窗口——关窗口只是收起 UI,脑还在跑)。
   安装器虽然会强制结束 `RecruitHelper.exe` 和 `RecruitHelperBrain.exe`,但那是强杀,
   进行中的 WAL 要靠下次启动的恢复轨收敛,正常退出更干净;
2. **SmartScreen 会拦**(包未签名):"更多信息" → "仍要运行";
3. **插件文件会自动换代,但 Chrome 里要手动点一次重载**
   (`chrome://extensions` → 刷新图标)。不点的话跑的还是旧代码,此时 contractMatch
   会挡住 effectful 派发——不会发错消息,但业务会停住;
4. 插件目录 `%LOCALAPPDATA%\RecruitHelper\plugin` 与扩展 ID
   `oankodckocoibcofboconjjeinpjpdnb` 都固定不变,**不需要删了重新加载**。

全新机器还要多三步(**都只在诊断台里,七页产品 UI 没有入口**):同步职位、绑定平台
账号(平台标识手输 `zhilian`)、配置模型连接。完整说明见
[`scripts/windows-run.md`](../../../scripts/windows-run.md),交付时可以直接把那份给甲方。

## 六、已知的坑(改构建链前必读)

这几条都是真机踩出来的,不是推测:

**别用 Homebrew 的 makensis。** 3.12 在 macOS 26 / arm64 上是坏的——Section 里只要有
`File`、`WriteUninstaller`、`ExecWait` 任一实质指令就 `std::bad_alloc` 退出,且不打印
任何有用错误。`build-win.sh` 刻意优先用 electron-builder 缓存里的 3.04,别把这个顺序
"顺手"改回来。

**`scripts/installer.nsi` 必须保持纯 ASCII,注释也是。** macOS 的 makensis 是 ANSI-only
构建,脚本里出现任何非 ASCII 字节都会 `Bad text encoding: line 1`,`-INPUTCHARSET UTF8`
和 UTF-8 BOM 都救不了。产品 UI 文案照常中文,只有安装器受限于程序标识 `RecruitHelper`。

**不要走 electron-builder 的 nsis target。** 它编译出 installer 后还要**运行**那个 32 位
exe 才能提取卸载器,在 Apple Silicon 上需要 wine,而 qemu 跑不了 32 位 Windows 程序
(Rosetta 也不覆盖 32 位)。现在用的是自有 NSIS 脚本 + 原生 makensis,`WriteUninstaller`
在编译期写出卸载器,不运行任何 exe。

**三个目录名不一致是有意的,别去统一:**

| 用途 | 目录 | 名字来自 |
|---|---|---|
| 安装位置 | `%LOCALAPPDATA%\Programs\RecruitHelper` | electron-builder 的 `build.productName` |
| 业务库与日志 | `%APPDATA%\recruithelper-client\` | `app.getName()`,读 package.json 顶层 `name` |
| 插件目录 | `%LOCALAPPDATA%\RecruitHelper\plugin` | `pluginSeed.js` 里硬编码 |

给 package.json 加个顶层 `productName` 确实能让业务库也叫 `RecruitHelper`,但那会让
**已经激活、已同步职位、已绑定账号的现有安装指向一个空目录**,等于全部重来。

## 七、这个包还不是正式交付物

没有代码签名、没有自动更新、插件在 Chrome 里的重载仍靠人工。这些是首客前的义务,
清单见 [`docs/插件交付与更新决策-2026-07-25.md`](../../../docs/插件交付与更新决策-2026-07-25.md)
第三节。甲方问"能不能直接给客户"时,如实说明这三条缺口。
