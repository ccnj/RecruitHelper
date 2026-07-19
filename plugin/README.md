# 招聘助手 · 手端(Chrome MV3 插件)

脑手协议的**手端**。base = 稳定壳(连接/心跳/重连/命令分发/去重/监听注册),
program = 业务原语(注册表 + debug 三原语)。协议类型来自 `../contract` codegen,禁止手写。

## 结构

```
src/base/        连接、分发器、配置/身份、SW 入口(唯一注册 chrome 监听处)
src/program/     原语注册表 + primitives/debug.ts(program 不注册任何 chrome 监听)
src/options/     options 页(连接状态 + 脑地址配置)
test/run.mjs     Node 集成 harness(注入浏览器全局,跑真实 base 代码连真脑)
```

## 构建

```bash
pnpm install
pnpm build       # 打包到 dist/(即 unpacked 扩展目录)
pnpm typecheck   # tsc --noEmit(strict)
```

## 在真实 Chrome 加载(里程碑 1 最终验收 · 需人操作)

1. 起脑服务:仓库根 `go run ./client/service`(默认 17872,与插件默认端口一致)。
2. Chrome → `chrome://extensions` → 打开右上"开发者模式" → "加载已解压的扩展程序" → 选 `plugin/dist`。
3. 插件首次启动时在 `chrome.storage.local` 的基础设施配置中生成稳定随机 `handId`，随即自动连接并在脑端登记，无需人工操作。
4. 从客户端诊断页选择已自动登记的手派发命令，浏览器标签页应肉眼可见切换。

> 说明:MV3 service worker 在 headless/脚本化 Chrome 里的自启动不稳定(自动化环境
> 常见),故本仓库用 `test/run.mjs`(见下)自动验证插件逻辑;上面的"真实 Chrome 加载"
> 是里程碑 1 需人眼确认的最终一步(SW 生死/真标签页)。

## 自动验证(Node harness,免真机)

跑真实 base 代码(connection/dispatcher/registry/debug 同生产)+ 注入浏览器全局,连真脑:

```bash
# 先起脑:go run ./client/service -port 17872
pnpm test:node
```

覆盖:稳定本地 handId→自动 hello/welcome→在线+能力集→正式绑定/账号 actor 四条 M2 原语，并保留 ping/switchWindow/slowEcho 账本回归。不覆盖 SW 生死(人端尾巴)。

## 硬约束(改动前必读)

- program 不注册任何 chrome 监听、只填原语注册表(宪法禁令 5;交付机制悬置的前提)。
- 手侧持久化只放基础设施数据(稳定 handId/脑地址),禁存业务状态(禁令 2)。
- 无业务定时器;alarms/心跳仅基础设施用途(禁令 1)。
- 协议字段全部来自 `../contract` codegen(宪法 A5)。
