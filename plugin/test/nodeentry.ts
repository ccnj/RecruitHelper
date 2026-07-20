// Node 测试入口:导出生产 base 连接层与 program 原语,供 Node harness
// 在注入的浏览器边界上运行。连接、注册表、分发器与原语实现均不复制。
export { Connection } from '../src/base/connection'
export { registerDebugPrimitives } from '../src/program/primitives/debug'
export { registerM2Primitives } from '../src/program/primitives/m2'
export { registerM3Primitives } from '../src/program/primitives/m3'
export { registerM4Primitives } from '../src/program/primitives/m4'
