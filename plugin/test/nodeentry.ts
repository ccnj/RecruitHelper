// Node 测试入口:导出生产 base 连接层与 program 原语,供 Node harness
// 在注入的浏览器边界上运行。连接、注册表、分发器与原语实现均不复制。
import { registerPlatform } from '../src/program/platform/registry'
import { zhilianAdapter } from '../src/program/platform/zhilian'

// 与 src/base/background.ts 同一件事:原语按 cmd.context.platform 路由,
// 平台不注册就没有实现可路由。端到端跑的是真原语,这一步不能省。
registerPlatform(zhilianAdapter)

export { Connection } from '../src/base/connection'
export { registerDebugPrimitives } from '../src/program/primitives/debug'
export { registerM2Primitives } from '../src/program/primitives/m2'
export { registerM3Primitives } from '../src/program/primitives/m3'
export { registerM4Primitives } from '../src/program/primitives/m4'
export { registerM5Primitives } from '../src/program/primitives/m5'
export { registerM6Primitives } from '../src/program/primitives/m6'
export { registerM7Primitives } from '../src/program/primitives/m7'
export { putSessionBlob, sessionBlobParams } from '../src/base/capture'
