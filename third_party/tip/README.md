# hiboss_tip.dll —— 自研 TSF 输入法

**这是本仓库唯一一个我们不编译的二进制。** 提交进来是 2026-09-01 甲方裁决：
它要随客户端安装包一起走，而它的源码在平级仓库 hiBoss 里，让 `build-win.sh`
去那边编等于给 RecruitHelper 的构建加一条跨仓库依赖（打包机得装 Rust + mingw-w64，
而且 hiBoss 必须在场）。两条都有味道，选了更简单诚实的这条：提交二进制，
把出处写清楚。

## 这一份是哪来的

| | |
|---|---|
| 源码 | `hiBoss/lab/engine/tip/`（本仓库之外） |
| 源码版本 | `6e60bff` 2026-08-23 `feat(tip): 组字区补上音节分隔撇号——画上去的，不是按出来的` |
| 构建 | `cargo build --release --target x86_64-pc-windows-gnu` |
| sha256 | `9f6427ad70d006eb88575b520e5a5f30571dad60de2fd171df798ccc4699efc4` |
| 大小 | 340480 字节 |
| 真机 | 2026-08-23 Windows 11 / Chrome：9 字计划 6 个词，**6/6 全部上屏**，文案一字不差 |

**换新版时这张表必须同批更新**，否则半年后没人知道机器上跑的是哪一版。

## 它是全部吗——是

`crate-type = ["cdylib"]`，产物就这一个文件。**不查词库**（上屏什么由脑经命名管道
指定，那正是它存在的理由），没有配置文件、没有配套 exe、没有 `.reg`——注册逻辑
写在 DLL 自己的 `DllRegisterServer` 里。

静态链接（`-C target-feature=+crt-static`）。**这不是可选项**：不静态链接的话产物
依赖 `libgcc_s_seh-1.dll` 等，而这个 DLL 要被 TSF 加载进 chrome.exe，
**那里找不到 mingw 运行库，加载直接失败**。核对办法：

```bash
x86_64-w64-mingw32-objdump -p third_party/tip/hiboss_tip.dll | grep 'DLL Name'
```

只该出现系统 DLL 与 UCRT（`api-ms-win-*`）。出现 `libgcc_s_seh-1` 或
`libwinpthread-1` 就是构建配置掉了，别装。

## 装到 Windows 上

随客户端整包携带，落在 `<安装目录>\resources\tip\hiboss_tip.dll`，
安装目录是 `%LOCALAPPDATA%\Programs\RecruitHelper`。

```
regsvr32 hiboss_tip.dll        注册，必须管理员
regsvr32 /u hiboss_tip.dll     反注册
```

注册写 `HKCR\CLSID`（= HKLM）与 TSF profile，**必须提权**；没有官方支持的免管理员
per-user 路径，MSIX 也走不通。装完**必须重启 Chrome**——TSF 只在进程启动时加载 TIP。

判据是 `tasklist /m hiboss_tip.dll`，**不是「能打字」**：`regsvr32` 会对没生效的
注册照样弹「成功」，而 TIP 没加载时按键落到底层美式键盘，打出来的字母一模一样。

## ⚠ 现在这个位置是临时的

放在安装目录里对**真机验证**够用，但不是它最终该待的地方。两条理由：

1. **NSIS 升级整体替换安装目录，而 TSF 加载期间 Windows 锁着这个文件。** 插件当年
   就是为这个从安装目录搬出去的（见 `client/electron/pluginSeed.js` 开头）——
   Chrome 开着读扩展目录时被覆盖会报「扩展损坏」。DLL 更硬：文件锁住，覆盖直接失败。
2. **`regsvr32` 把绝对路径写进注册表**，所以它落在哪儿是个长期承诺。

按插件的先例，最终位置应是安装目录之外的 `%LOCALAPPDATA%\RecruitHelper\tip\`，
由壳启动时安置。**没有现在就做，是因为它和「TIP 怎么装到客户机」那个悬着的问题
绑在一起**（注册要管理员，而本产品的安装器刻意是 `RequestExecutionLevel user`、
不弹 UAC，静默升级也靠这一点）。等真机验完这条线成立，再一起定。

相关：`docs/boss/键盘线架构-2026-08-31.md`「九、两个尚未解决的真问题」、
`docs/boss/Windows真机操作单-键盘线-2026-09-01.md` 第 4 段。
