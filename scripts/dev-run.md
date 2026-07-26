# 里程碑 1 · 人眼验收操作指南

三步跑起整套,亲眼看"装插件 → 手自动在线 → 点按钮 → 标签页切换 → 帧回显"。

## 1. 起脑服务

```bash
cd <仓库根>
go run ./client/service -port 17872
```

看到 `HTTP/WS 监听 addr=127.0.0.1:17872` 即就绪。

## 2. 起客户端 UI(两选一)

**A. 浏览器直开(最快):**
```bash
cd client/ui && pnpm install && pnpm dev
# 打开 http://localhost:5273
```

**B. Electron 外壳(即"打开客户端"):**
```bash
cd client/ui && pnpm install && pnpm build   # 先出 UI 构建产物
cd ../electron && pnpm install                  # 装 electron(约 200MB)
pnpm start                                        # 壳自动起脑 + 开窗
# 若想连已在跑的脑并用 vite dev:UI_URL=http://localhost:5273 pnpm start
```

若开发期需要让 Electron 继续使用仓库现有的 `data/brain.db`，只覆盖脑的数据
目录，不要用 `--user-data-dir` 指向仓库根：

```bash
BRAIN_DATA_DIR=<仓库根>/data pnpm start
```

### 2.1 业务窗口外的有人值守开发验收

正式规则仍是每天 08:00～24:00。只有在已授权测试账号、有人值守的本地验收
中，才可让本次脑进程临时把业务窗口视为全天开放：

```bash
# 直接启动脑
RECRUITHELPER_DEV_ALLOW_OUT_OF_WINDOW=1 go run ./client/service -port 17872

# 或启动正式 Electron 客户端
cd client/electron
RECRUITHELPER_DEV_ALLOW_OUT_OF_WINDOW=1 pnpm start
```

开关只在脑启动时读取。Electron 关闭窗口后通常仍留在托盘运行，因此切换开关
前必须从托盘“退出”或在开发终端结束原进程，再重新启动；重复打开窗口不会
改变已运行脑的设置。恢复正式 08:00～24:00 规则时，完整退出客户端并执行不带
该变量的普通 `pnpm start`。

该开关不修改系统时间和任何业务时间戳，也不会自动开始任务。它只放开时间
门，之后仍须在客户端显式点击开始或恢复；暂停、账号身份、契约匹配、人工闸
以及消息发送安全轨全部照常生效。正式客户启动脚本与安装包不得设置该变量。

## 3. 装插件(手端)

```bash
cd plugin && pnpm install && pnpm build
```
Chrome → `chrome://extensions` → 右上"开发者模式" → "加载已解压的扩展程序" → 选 `plugin/dist`。

## 4. 点一遍

1. 打开 UI，插件首次运行时生成并保存自己的 `handId`，连接本地脑后自动登记；“手”区应直接显示该手在线，无需配对或确认。
2. "派发命令"区点 **debug.switchWindow** → 浏览器当前窗口的标签页**肉眼可见地切到下一个**。
3. "协议帧观测台"实时显示 `脑→手 cmd` / `手→脑 ack` / `手→脑 result` / `脑→手 ack`。
4. "命令账本"里该命令走到 **ok**。
5. 进阶:debug.slowEcho 选 **silent** → 约 5 分钟后(或改短 deadline)该命令进 **suspect** 队列 → 点**确认未发生**裁决。

看到第 1-4 步,里程碑 1 的闭环即人眼确认完成。

## 5. 七页客户端中的开发者诊断入口

普通用户的七页侧边栏不显示诊断入口。维护人员在客户端窗口按
`Cmd+Shift+D`（Windows 使用 `Ctrl+Shift+D`）可进入或退出既有诊断台；
诊断能力与写入口没有删除，只是不再成为普通用户可见的第三个业务入口。
