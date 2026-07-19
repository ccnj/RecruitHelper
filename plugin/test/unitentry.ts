// 纯 Node 单元测试入口：只导出生产 base/registry，不复制分发逻辑。
// 生成器自带的验证用例也必须在 Node 门禁中真正执行，不能只经过 typecheck。
import '../../contract/gen/ts/validation.test'

export const GENERATED_CONTRACT_VALIDATION_EXECUTED = true
export { Dispatcher } from '../src/base/dispatcher'
export { Connection, heartbeatDelayMs, utf8ByteLength } from '../src/base/connection'
export { getHandId, getWsUrl, normalizeLocalWsUrl, RECONNECT_STABLE_MS, setWsUrl } from '../src/base/config'
export { handleInfrastructureMessage } from '../src/base/optionsBridge'
export { ContentSensor } from '../src/base/contentSensor'
export { readZhilianUnreadTotal, ZHILIAN_UNREAD_BADGE_SELECTOR } from '../src/base/contentDom'
export { SensorBridge } from '../src/base/sensorBridge'
export { NavigationTracker } from '../src/base/navigation'
export { CONTENT_MESSAGE, MANUAL_EMIT_MIN_MS } from '../src/base/contentMessages'
export { register } from '../src/program/registry'
export {
  readZhilianList,
  readZhilianThread,
  zhilianTestHooks,
  ZhilianPlatformError,
} from '../src/program/platform/zhilian'
export {
  AckStatus,
  ErrorCode,
  Kind,
  ResultStatus,
  CmdClass,
  DEFAULTS,
  EventName,
  Feature,
  LoginState,
  ManualInteractionKind,
  NotReadyReason,
  PageKind,
  Primitive,
  PROTO_VERSION,
} from '../src/base/protocol'
