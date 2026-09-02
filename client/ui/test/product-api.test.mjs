import * as esbuild from 'esbuild'
import { mkdirSync } from 'node:fs'
import { pathToFileURL } from 'node:url'

globalThis.window = {
  recruitHelper: {
    adminBase: 'http://127.0.0.1:18888',
    adminToken: 'product-memory-token',
  },
}

const requests = []
globalThis.fetch = async (url, init = {}) => {
  requests.push({
    url: String(url),
    method: init.method,
    headers: new Headers(init.headers),
    body: init.body,
  })
  return new Response('{"accepted":true}', { status: 202 })
}

mkdirSync('test/dist', { recursive: true })
await esbuild.build({
  entryPoints: ['src/product/api.ts'],
  bundle: true,
  format: 'esm',
  platform: 'neutral',
  outfile: 'test/dist/product-api.mjs',
  logLevel: 'error',
})

const productAPI = await import(
  pathToFileURL(process.cwd() + '/test/dist/product-api.mjs').href + `?run=${Date.now()}`
)

await productAPI.startProductWorkflow('full')
await productAPI.startProductWorkflow('replyOnly')
await productAPI.pauseProductWorkflow()
await productAPI.resumeProductWorkflow()
await productAPI.endProductWorkflow()
await productAPI.sendProductConfirmation('batch-one', ['profile-a', 'profile-b'])
await productAPI.startProductWorkflow('replyOnly', 'boss')

let fail = 0
const check = (condition, message) => {
  console.log(condition ? '  PASS' : '  FAIL', message)
  if (!condition) fail++
}

check(
  requests.map((request) => request.url).join('|') === [
    'http://127.0.0.1:18888/app/workflow/start',
    'http://127.0.0.1:18888/app/workflow/start',
    'http://127.0.0.1:18888/app/workflow/pause',
    'http://127.0.0.1:18888/app/workflow/resume',
    'http://127.0.0.1:18888/app/workflow/end',
    'http://127.0.0.1:18888/app/confirmation/send',
    'http://127.0.0.1:18888/app/workflow/start',
  ].join('|'),
  '六类产品操作只访问 /app/* 正式入口',
)
check(
  requests.every((request) => request.method === 'POST'),
  '产品操作全部使用 POST',
)
check(
  requests.every(
    (request) => request.headers.get('Authorization') === 'Bearer product-memory-token',
  ),
  '产品操作携带 preload 内存 bearer',
)
check(
  requests.map((request) => request.body).join('|') === [
    '{"mode":"full"}',
    '{"mode":"replyOnly"}',
    '{}',
    '{}',
    '{}',
    '{"batchId":"batch-one","profileIds":["profile-a","profile-b"]}',
    '{"mode":"replyOnly","platform":"boss"}',
  ].join('|'),
  '工作流与整批确认请求体保持精确',
)
check(
  requests.every((request) => !request.url.includes('product-memory-token')),
  'bearer 不进入 URL',
)

// 多平台歧义(2026-09-02 批 D 2.1):409 带 platforms 候选列表 → PlatformChoiceRequired。
globalThis.fetch = async () => new Response(
  '{"error":"检测到多个招聘平台已登录，请选择本次要运行的平台","platforms":["zhilian","boss"]}',
  { status: 409, headers: { 'Content-Type': 'application/json' } },
)
let choice = null
try {
  await productAPI.startProductWorkflow('full')
} catch (reason) {
  choice = reason
}
check(choice instanceof productAPI.PlatformChoiceRequired, '歧义响应应抛 PlatformChoiceRequired')
check(
  choice && Array.isArray(choice.platforms) && choice.platforms.join(',') === 'zhilian,boss',
  '候选平台列表原样带出',
)
check(choice && choice.message.includes('多个招聘平台'), '错误文案来自脑的 error 字段')
globalThis.fetch = async () => new Response('{"error":"当前不在业务运行窗口内"}', { status: 409 })
let plain = null
try {
  await productAPI.startProductWorkflow('full')
} catch (reason) {
  plain = reason
}
check(plain && !(plain instanceof productAPI.PlatformChoiceRequired) && plain.message === '当前不在业务运行窗口内',
  '不带 platforms 的失败仍是普通错误')

if (fail) process.exit(1)
console.log('产品 UI 写入口测试通过')
