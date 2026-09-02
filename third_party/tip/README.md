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
| 源码版本 | `4a4ff70` 2026-09-01 `feat(tip): 直接段该不该被吃，改由词表说了算——一个标记还清四类账`（hiBoss HEAD `95607a3`） |
| 构建 | `cargo build --release --target x86_64-pc-windows-gnu` |
| sha256 | `a41771c6646e3cc1b0ef8f9548bd63e5172345bf6344faf810f3ceecb028d85e` |
| 大小 | 341504 字节 |
| 真机 | 上游 2026-09-02 Windows：passthrough 五项对账 28/29/5/5/55 全 PASS，209 字覆盖面测试逐字相同；**本仓库这一版零次** |
| 上一版 | `6e60bff` sha256 `9f6427ad…4699efc4`，本仓库 2026-09-01 Windows 三条文案全过（那时线格式还是两段） |

## 这一版与上一版的线格式差一段

`词|音节边界|标志`，第三段 `P` = 透传（TIP 不吃这个键、只推进词序号）。老 DLL 收到
三段格式会把 `2|P` 整个当音节字段、静默滤掉；新 DLL 收到两段格式标志为空 = 走组字，
与旧行为一致。**所以 DLL 与脑必须一起换**，不能只换一边——本仓库把两者放在同一个
merge 里，回退也一起回退。

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

随客户端整包携带,但**不留在安装目录**。壳启动时把它安置到:

```
%LOCALAPPDATA%\RecruitHelper\tip\hiboss_tip.dll
```

与插件的固定目录(`…\RecruitHelper\plugin\`)同级。安装目录里那份
(`…\Programs\RecruitHelper\resources\tip\`)只是母版,不是被注册的那个。

**为什么非搬不可**,两条,第二条更硬:

1. **NSIS 升级整体替换安装目录**,而 TSF 把 DLL 映射进 chrome.exe 等进程,
   映像文件在 Windows 上锁死——覆盖或改名直接失败。插件当年就是为同类理由搬出去的
   (`client/electron/pluginSeed.js` 开头)。
2. **`regsvr32` 把绝对路径写进 `HKCR\CLSID`。** 位置一旦被人注册过就是长期承诺:
   改路径等于让已注册的客户机指向一个不存在的文件,而症状是「输入法还在语言栏里,
   但一按键什么都不发生」——跟「从没装上」现场看起来一模一样。

安置只放文件、**不注册**;注册要管理员,而本产品的安装器刻意是
`RequestExecutionLevel user`、不弹 UAC(静默升级正靠这一点)。

```
cd %LOCALAPPDATA%\RecruitHelper\tip
regsvr32 hiboss_tip.dll        注册,必须管理员
regsvr32 /u hiboss_tip.dll     反注册
```

注册写 `HKCR\CLSID`(= HKLM)与 TSF profile;没有官方支持的免管理员 per-user 路径,
MSIX 也走不通。装完**必须重启 Chrome**——TSF 只在进程启动时加载 TIP。

判据是 `tasklist /m hiboss_tip.dll`,**不是「能打字」**:`regsvr32` 会对没生效的
注册照样弹「成功」,而 TIP 没加载时按键落到底层美式键盘,打出来的字母一模一样。

### 换 TIP 版本时

壳会在下次启动时自动把新版换进固定目录(指纹不同才动手)。两个注意:

- **Chrome 开着且 TIP 已注册时,替换会失败**——映像锁死。失败是**降级**:
  旧版原封不动、记一行日志、客户端照常起来。想换就关掉 Chrome 再重启客户端。
- **路径没变,所以注册通常照旧有效**;但 CLSID 与 TSF profile 的 GUID 是烧在 DLL
  里的,它们要是变了就得重新 `regsvr32`。升 TIP 版本时一并想清楚。

## 悬着的:怎么让客户机完成注册

固定目录解决了"文件放哪",没解决"谁来提权跑那一行"。这仍是
`docs/boss/键盘线架构-2026-08-31.md`「九、两个尚未解决的真问题」里的第一条:

> `regsvr32` 注册需要管理员权限,而本产品的交付方式是「客户端整包携带 + 首次人工
> 远程协助」。**它是键盘线能不能落地的最终关卡。**

首次安装本来就有人在场做手工步骤(插件也要开发者模式手动加载),所以多一行
`regsvr32` 不改变交付模型的性质;**新增的是"要管理员权限"**——插件那一步不需要。
等真机验完这条线成立,再定。

相关：`docs/boss/键盘线架构-2026-08-31.md`、
`docs/boss/Windows真机操作单-键盘线-2026-09-01.md` 第 4 段、
`client/electron/pluginSeed.js`。
