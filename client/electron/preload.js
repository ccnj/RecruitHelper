// 预加载只暴露本次进程的本地管理连接参数。token 不落盘、不进 URL/localStorage；
// renderer 只能把它作为 Authorization header 交回 127.0.0.1 脑服务。
'use strict'
const { contextBridge } = require('electron')

function argument(name) {
  const prefix = `--${name}=`
  const raw = process.argv.find((value) => value.startsWith(prefix))
  return raw ? raw.slice(prefix.length) : ''
}

contextBridge.exposeInMainWorld('recruitHelper', Object.freeze({
  adminBase: argument('recruit-helper-admin-base'),
  adminToken: argument('recruit-helper-admin-token'),
}))
