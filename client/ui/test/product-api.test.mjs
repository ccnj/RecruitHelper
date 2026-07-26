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

await productAPI.startProductWorkflow('full', '42')
await productAPI.startProductWorkflow('replyOnly')
await productAPI.pauseProductWorkflow()
await productAPI.resumeProductWorkflow()
await productAPI.endProductWorkflow()
await productAPI.sendProductConfirmation('batch-one', ['profile-a', 'profile-b'])

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
    '{"mode":"full","backendJobId":"42"}',
    '{"mode":"replyOnly"}',
    '{}',
    '{}',
    '{}',
    '{"batchId":"batch-one","profileIds":["profile-a","profile-b"]}',
  ].join('|'),
  '工作流与整批确认请求体保持精确',
)
check(
  requests.every((request) => !request.url.includes('product-memory-token')),
  'bearer 不进入 URL',
)

if (fail) process.exit(1)
console.log('产品 UI 写入口测试通过')
