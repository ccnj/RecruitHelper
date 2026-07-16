// options 页脚本(纯 JS,不打包)。只读连接状态 + 配置脑地址。
const KEY = 'infra'

function refresh() {
  chrome.runtime.sendMessage({ type: 'getStatus' }, (s) => {
    if (chrome.runtime.lastError || !s) {
      document.getElementById('phase').textContent = '手端未就绪'
      return
    }
    document.getElementById('phase').textContent = s.phase
    document.getElementById('session').textContent = s.session || '-'
    document.getElementById('bootId').textContent = s.bootId || '-'
  })
}

chrome.storage.local.get(KEY, (o) => {
  const c = o[KEY] || {}
  if (c.wsUrl) document.getElementById('wsUrl').value = c.wsUrl
})

document.getElementById('save').addEventListener('click', () => {
  const url = document.getElementById('wsUrl').value.trim()
  chrome.storage.local.get(KEY, (o) => {
    const c = o[KEY] || {}
    if (url) c.wsUrl = url
    else delete c.wsUrl
    chrome.storage.local.set({ [KEY]: c }, () => alert('已保存,重新加载扩展生效'))
  })
})

refresh()
setInterval(refresh, 2000)
