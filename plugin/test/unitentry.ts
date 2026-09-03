// 纯 Node 单元测试入口：只导出生产 base/registry，不复制分发逻辑。
// 生成器自带的验证用例也必须在 Node 门禁中真正执行，不能只经过 typecheck。
import '../../contract/gen/ts/validation.test'
import { registerPlatform } from '../src/program/platform/registry'
import { registerDebugPrimitives } from '../src/program/primitives/debug'
import { bossAdapter } from '../src/program/platform/boss'
import { zhilianAdapter } from '../src/program/platform/zhilian'

// 本文件是测试侧的 background.ts:生产接线在 src/base/background.ts 注册平台,
// 这里做同一件事,好让经原语层的用例真正走到平台路由,而不是绕过它。
registerPlatform(zhilianAdapter)
registerPlatform(bossAdapter)
registerDebugPrimitives()

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
export { capabilities, capabilitiesByPlatform, lookup, register } from '../src/program/registry'
export {
  callPlatform,
  callPlatformUnbound,
  lookupPlatform,
  registerPlatform,
  hasCapability,
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
export { bossSite, BOSS_PLATFORM, BOSS_MATCH } from '../src/program/platform/bossSite'
export { bossAdapter } from '../src/program/platform/boss'
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
export { planMove, planType, mulberry32, DEFAULT_MAX_DWELL_MS, OSENGINE_SOURCE } from '../src/program/osengine/plan'
// 只为门禁导出:钉住上游标点键集合,它变了 Go 侧的键码表就得跟着补。
export { PUNCT_KEY, tokenize, keyFor } from '../src/program/osengine/vendor/compose/pinyin.mjs'
export { refuseBeforeMoving, refuseWhenNotInFront, describeFront, osProbeContractData, runOsProbe, clickAimPoint, SPREAD_FRACTIONS, composeClearKeys } from '../src/program/platform/osinput'
export {
  append as telemetryAppend,
  readAll as telemetryReadAll,
  clear as telemetryClear,
  CHUNK as TELEMETRY_CHUNK,
  KIND_CLICK as TELEMETRY_KIND_CLICK,
  KIND_UPLOAD as TELEMETRY_KIND_UPLOAD,
  MAX_UPLOAD_CHUNKS as TELEMETRY_MAX_UPLOAD_CHUNKS,
  MAX_CLICK_CHUNKS as TELEMETRY_MAX_CLICK_CHUNKS,
} from '../src/base/telemetry/store'
export { bodyText, deepFind, recordUpload } from '../src/base/telemetry/capture'
export { bossTelemetrySite, telemetrySites } from '../src/program/platform/telemetrySites'
export { classifyBossEntry, bossCodeLabel, bossCodeMeaning } from '../src/program/platform/telemetrySites'
export { parseLedger as telemetryParseLedger } from '../src/base/telemetry/counters'
export { BOSS_INPUT_COUNTERS, REPORT_EVERY as BOSS_REPORT_EVERY } from '../src/program/platform/bossInputCounters'
export { stripNewlines, bossTestHooks, identityCacheUsable, resetBossIdentityCacheForTest, BOSS_DISMISS_WHITELIST } from '../src/program/platform/boss'
export {
  tabNavigationGeneration, noteMainFrameNavigation, forgetTab, resetTabGenerationsForTest, registerTabGenerationTracking,
} from '../src/base/tabGeneration'
export { registerM4Primitives } from '../src/program/primitives/m4'
export { registerM5Primitives } from '../src/program/primitives/m5'
export { registerM7Primitives } from '../src/program/primitives/m7'
export { registerJobPublishPrimitives } from '../src/program/primitives/jobPublish'
export { registerAccountPrimitives } from '../src/program/primitives/account'
export { registerDebugPrimitives } from '../src/program/primitives/debug'
