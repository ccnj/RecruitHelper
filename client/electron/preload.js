// 预加载只暴露本次进程的本地管理连接参数。token 不落盘、不进 URL/localStorage；
// renderer 只能把它作为 Authorization header 交回 127.0.0.1 脑服务。
'use strict'
const { contextBridge, ipcRenderer } = require('electron')

function argument(name) {
  const prefix = `--${name}=`
  const raw = process.argv.find((value) => value.startsWith(prefix))
  return raw ? raw.slice(prefix.length) : ''
}

contextBridge.exposeInMainWorld('recruitHelper', Object.freeze({
  adminBase: argument('recruit-helper-admin-base'),
  adminToken: argument('recruit-helper-admin-token'),
  // 安装新版必须在主进程做:renderer 起不了进程,也不该能起。它只发一个意图,
  // 由主进程去问脑"现在能不能装",拿到已经重新校验过的路径后才动手。
  installUpdate: () => ipcRenderer.invoke('recruit-helper:install-update'),
}))
