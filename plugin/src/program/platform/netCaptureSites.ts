// 请求录制的平台域名表。**这张表就是能力上限**(2026-09-08 甲方要求):只记这两家
// 站点发出的请求,别的网站一律不记。
//
// 这是平台知识,住在 program;判定机制与监听注册在 base/telemetry。刻意不从适配器
// 注册表推导:注册一个新平台不该悄悄扩大录制范围,要扩必须在这里明写一行。
// 写可注册域而不是页面主机——登录页、子域上的 iframe 与页面 service worker 都是
// 同一个站发出的请求。
import type { CaptureSite } from '../../base/telemetry/netCapture'

export const netCaptureSites: readonly CaptureSite[] = [
  { id: 'boss', domains: ['zhipin.com'] },
  { id: 'zhilian', domains: ['zhaopin.com'] },
]
