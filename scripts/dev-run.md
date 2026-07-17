# 里程碑 1 · 人眼验收操作指南

三步跑起整套,亲眼看"装插件 → 手在线 → 点按钮 → 标签页切换 → 帧回显"。

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

## 3. 装插件(手端)

```bash
cd plugin && pnpm install && pnpm build
```
Chrome → `chrome://extensions` → 右上"开发者模式" → "加载已解压的扩展程序" → 选 `plugin/dist`。

## 4. 点一遍

1. 在 UI"配对"区点**开启配对窗** → 插件自动连上,"配对"区出现待配对项 → 点**确认** → "手"区显示 `hand-01 在线`。
2. "派发命令"区点 **debug.switchWindow** → 浏览器当前窗口的标签页**肉眼可见地切到下一个**。
3. "协议帧观测台"实时显示 `脑→手 cmd` / `手→脑 ack` / `手→脑 result` / `脑→手 ack`。
4. "命令账本"里该命令走到 **ok**。
5. 进阶:debug.slowEcho 选 **silent** → 约 5 分钟后(或改短 deadline)该命令进 **suspect** 队列 → 点**确认未发生**裁决。

看到第 1-4 步,里程碑 1 的闭环即人眼确认完成。
