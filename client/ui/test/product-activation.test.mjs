import * as esbuild from 'esbuild'
import { mkdirSync } from 'node:fs'
import { pathToFileURL } from 'node:url'

mkdirSync('test/dist', { recursive: true })
await esbuild.build({
  entryPoints: ['src/product/activation.ts'],
  bundle: true,
  format: 'esm',
  platform: 'neutral',
  outfile: 'test/dist/product-activation.mjs',
  logLevel: 'error',
})

const moduleUrl = pathToFileURL(process.cwd() + '/test/dist/product-activation.mjs').href
const {
  activationInputError,
  buildActivationInput,
  shouldShowActivation,
} = await import(moduleUrl)

let fail = 0
const check = (condition, message) => {
  console.log(condition ? '  PASS' : '  FAIL', message)
  if (!condition) fail++
}

const unconfigured = {
  configured: false,
  baseUrlConfigured: false,
  machineIdConfigured: false,
  licenseTokenConfigured: false,
  machineIdentityReady: true,
  machineMatch: false,
}

check(
  !shouldShowActivation('loading', { authorized: false, activationRequired: true }),
  '产品投影尚未读取成功时不误弹激活页',
)
check(
  !shouldShowActivation('stale', { authorized: false, activationRequired: true }),
  '产品投影读取失败时不把未知状态当成未授权',
)
check(
  shouldShowActivation('ready', { authorized: false, activationRequired: true }),
  '产品投影明确未授权时展示激活页',
)
check(
  !shouldShowActivation('ready', { authorized: true, activationRequired: false }),
  '当前已授权机器直接进入七页工作台',
)
check(
  !shouldShowActivation('ready', { authorized: false, activationRequired: false }),
  '授权状态不可判定时不冒充首次激活',
)

check(
  activationInputError(unconfigured, 'code') === null,
  '本机未配置后台地址也可激活——地址已内置于脑',
)
check(
  activationInputError(unconfigured, '') === '请输入激活码。',
  '激活码为空时不会提交',
)
check(
  activationInputError(
    { ...unconfigured, machineIdentityReady: false },
    'code',
  )?.includes('机器身份'),
  '机器身份不可用时在表单边界阻断',
)

const request = buildActivationInput(' one-time-code ')
check(
  request.invite_code === 'one-time-code',
  '请求只携带修剪后的一次性激活码',
)
check(
  Object.keys(request).sort().join(',') === 'invite_code',
  '激活页不拼装后台地址、machineId 或 licenseToken',
)

if (fail > 0) process.exit(1)
