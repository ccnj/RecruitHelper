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
npm install
npm run build       # 打包到 dist/(即 unpacked 扩展目录)
npm run typecheck   # tsc --noEmit(strict)
```

## 在真实 Chrome 加载(里程碑 1 最终验收 · 需人操作)

1. 起脑服务:仓库根 `go run ./client/service`(默认 17872,与插件默认端口一致)。
2. Chrome → `chrome://extensions` → 打开右上"开发者模式" → "加载已解压的扩展程序" → 选 `plugin/dist`。
3. 客户端点"配对"(或临时 `curl -X POST localhost:17872/admin/pairing/open`),插件自动连上并领工牌。
4. 用测试页(或 `curl -X POST localhost:17872/admin/cmd -d '{"handId":"hand-01","name":"debug.switchWindow","args":{}}'`)派命令,浏览器标签页应肉眼可见切换。

> 说明:MV3 service worker 在 headless/脚本化 Chrome 里的自启动不稳定(自动化环境
> 常见),故本仓库用 `test/run.mjs`(见下)自动验证插件逻辑;上面的"真实 Chrome 加载"
> 是里程碑 1 需人眼确认的最终一步(SW 生死/真标签页)。

## 自动验证(Node harness,免真机)

跑真实 base 代码(connection/dispatcher/registry/debug 同生产)+ 注入浏览器全局,连真脑:

```bash
# 先起脑:go run ./client/service -port 17872
npm run test:node
```

覆盖:null hello→配对→welcome{issued}存工牌→在线+能力集→派发 ping/switchWindow/
slowEcho 账本走到 ok→switchWindow 真的调 chrome.tabs.update。不覆盖 SW 生死(人端尾巴)。

## 硬约束(改动前必读)

- program 不注册任何 chrome 监听、只填原语注册表(宪法禁令 5;交付机制悬置的前提)。
- 手侧持久化只放基础设施数据(工牌/端口),禁存业务状态(禁令 2)。
- 无业务定时器;alarms/心跳仅基础设施用途(禁令 1)。
- 协议字段全部来自 `../contract` codegen(宪法 A5)。
