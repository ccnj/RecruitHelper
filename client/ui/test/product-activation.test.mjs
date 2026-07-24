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
const configured = {
  ...unconfigured,
  configured: true,
  baseUrlConfigured: true,
  machineIdConfigured: true,
  licenseTokenConfigured: true,
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
  activationInputError(unconfigured, '', 'code') === '请输入管理员提供的后台地址。',
  '本机未配置后台地址时必须由用户显式输入',
)
check(
  activationInputError(configured, '', 'code') === null,
  '本机已有后台地址时允许留空沿用',
)
check(
  activationInputError(unconfigured, 'ftp://backend.example', 'code')?.includes('http://'),
  '后台地址只接受明确的 HTTP(S) 地址',
)
check(
  activationInputError(unconfigured, 'https://backend.example', '') === '请输入激活码。',
  '激活码为空时不会提交',
)
check(
  activationInputError(
    { ...unconfigured, machineIdentityReady: false },
    'https://backend.example',
    'code',
  )?.includes('机器身份'),
  '机器身份不可用时在表单边界阻断',
)

const request = buildActivationInput(' https://backend.example/ ', ' one-time-code ')
check(
  request.base_url === 'https://backend.example/' && request.invite_code === 'one-time-code',
  '请求只携带修剪后的后台地址与一次性激活码',
)
check(
  Object.keys(request).sort().join(',') === 'base_url,invite_code',
  '激活页不接收或拼装 machineId/licenseToken',
)

if (fail > 0) process.exit(1)
