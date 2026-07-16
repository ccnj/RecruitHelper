// Node 测试入口:导出 base 连接层与 program 原语,供 Node харness 用注入的浏览器全局跑
// 与生产同一份代码(connection/dispatcher/registry/debug)。仅环境(Node 全局 vs SW 全局)不同。
export { Connection } from '../src/base/connection'
export { registerDebugPrimitives } from '../src/program/primitives/debug'
