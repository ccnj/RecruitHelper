// 纯 Node 单元测试入口：只导出生产 base/registry，不复制分发逻辑。
// 生成器自带的验证用例也必须在 Node 门禁中真正执行，不能只经过 typecheck。
import '../../contract/gen/ts/validation.test'
import { registerPlatform } from '../src/program/platform/registry'
import { zhilianAdapter } from '../src/program/platform/zhilian'

// 本文件是测试侧的 background.ts:生产接线在 src/base/background.ts 注册平台,
// 这里做同一件事,好让经原语层的用例真正走到平台路由,而不是绕过它。
registerPlatform(zhilianAdapter)

export const GENERATED_CONTRACT_VALIDATION_EXECUTED = true
export { Dispatcher } from '../src/base/dispatcher'
export { WitnessStore, WitnessStoreError } from '../src/base/witness'
export { Connection, heartbeatDelayMs, utf8ByteLength } from '../src/base/connection'
export { getHandId, getWsUrl, normalizeLocalWsUrl, RECONNECT_STABLE_MS, setWsUrl } from '../src/base/config'
export { handleInfrastructureMessage } from '../src/base/optionsBridge'
export { armRuntimeReload, acknowledgeRuntimeReloadResult, refreshPagesAfterRuntimeReload } from '../src/base/reload'
export { ContentSensor } from '../src/base/contentSensor'
export { SensorBridge } from '../src/base/sensorBridge'
export { CONTENT_MESSAGE } from '../src/base/contentMessages'
export { capabilities, lookup, register } from '../src/program/registry'
export {
  callPlatform,
  callPlatformUnbound,
  lookupPlatform,
  registerPlatform,
  registeredPlatforms,
  requireCapability,
  resolveAdapter,
  resetPlatformsForTest,
} from '../src/program/platform/registry'
export { PlatformError } from '../src/program/platform/types'
export {
  allSites,
  resetSitesForTest,
  setSitesForTest,
  siteById,
  siteForURL,
} from '../src/program/platform/sites'
export { zhilianSite } from '../src/program/platform/zhilianSite'
export { MAIN_ERROR_SENTINEL, runInPage, unwrapInjection } from '../src/program/platform/inject'
export {
  parseZhilianUnreadBadgeText,
  zhilianAdapter,
  ZHILIAN_UNREAD_BADGE_SELECTOR,
} from '../src/program/platform/zhilian'
export { registerM3Primitives } from '../src/program/primitives/m3'
export { registerM2Primitives } from '../src/program/primitives/m2'
export { registerM6Primitives } from '../src/program/primitives/m6'
export {
  applyZhilianSourcingFilters,
  acceptZhilianWechatRequest,
  canonicalZhilianTab,
  identifyZhilianCurrentConversation,
  ensureZhilianIM,
  openZhilianConversation,
  readZhilianList,
  readZhilianThread,
  readZhilianCurrentCandidate,
  readZhilianResume,
  readZhilianSourcingResume,
  readZhilianSourcingTargetResume,
  readZhilianSourcingWindow,
  parsedKeywordSections,
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
export { HandLogCode, installHandLogSink } from '../src/base/handLog'
export {
  netGuardStats,
  noteCommandDispatched,
  registerNetGuard,
  resetNetGuardForTest,
} from '../src/base/netGuard'
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
