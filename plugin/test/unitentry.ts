// 纯 Node 单元测试入口：只导出生产 base/registry，不复制分发逻辑。
// 生成器自带的验证用例也必须在 Node 门禁中真正执行，不能只经过 typecheck。
import '../../contract/gen/ts/validation.test'

export const GENERATED_CONTRACT_VALIDATION_EXECUTED = true
export { Dispatcher } from '../src/base/dispatcher'
export { WitnessStore, WitnessStoreError } from '../src/base/witness'
export { Connection, heartbeatDelayMs, utf8ByteLength } from '../src/base/connection'
export { getHandId, getWsUrl, normalizeLocalWsUrl, RECONNECT_STABLE_MS, setWsUrl } from '../src/base/config'
export { handleInfrastructureMessage } from '../src/base/optionsBridge'
export { armRuntimeReload, acknowledgeRuntimeReloadResult, refreshPagesAfterRuntimeReload } from '../src/base/reload'
export { ContentSensor } from '../src/base/contentSensor'
export { readZhilianUnreadTotal, ZHILIAN_UNREAD_BADGE_SELECTOR } from '../src/base/contentDom'
export { SensorBridge } from '../src/base/sensorBridge'
export { NavigationTracker, navigationTracker } from '../src/base/navigation'
export { CONTENT_MESSAGE, MANUAL_EMIT_MIN_MS } from '../src/base/contentMessages'
export { capabilities, lookup, register } from '../src/program/registry'
export { registerM3Primitives } from '../src/program/primitives/m3'
export { registerM6Primitives } from '../src/program/primitives/m6'
export {
  applyZhilianSourcingFilters,
  acceptZhilianWechatRequest,
  identifyZhilianCurrentConversation,
  ensureZhilianIM,
  readZhilianList,
  readZhilianThread,
  readZhilianCurrentCandidate,
  readZhilianResume,
  readZhilianSourcingResume,
  readZhilianSourcingTargetResume,
  readZhilianSourcingWindow,
  readZhilianGreetingOutcome,
  readZhilianWechatExchangeOutcome,
  inspectZhilianSendSurfaceDiagnostic,
  sendZhilianGreeting,
  sendZhilianInviteCard,
  sendZhilianMessage,
  sendZhilianWechatInvite,
  normalizeZhilianMessageText,
  zhilianTestHooks,
  ZhilianPlatformError,
} from '../src/program/platform/zhilian'
export {
  AckStatus,
  ErrorCode,
  ERROR_CODE_META,
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
  Retryable,
  SideEffect,
  WitnessUnavailableReason,
  validatePrimitiveResult,
} from '../src/base/protocol'
