// options 页脚本(纯 JS,不打包)。只读连接状态 + 配置脑地址。
const KEY = 'infra'

// 基础设施显式重载入口：便于在无法自动化 chrome://extensions 时重载 unpacked 扩展。
// 先移除 query，避免新 SW 打开/刷新 options 后形成重载循环。
const startupParams = new URLSearchParams(window.location.search)
if (startupParams.get('reload') === '1') {
  window.history.replaceState(null, '', window.location.pathname)
  window.setTimeout(() => chrome.runtime.reload(), 0)
}

function refresh() {
  chrome.runtime.sendMessage({ type: 'getStatus' }, (s) => {
    if (chrome.runtime.lastError || !s) {
      document.getElementById('phase').textContent = '手端未就绪'
      return
    }
    const phaseText = {
      connecting: '连接中',
      preSession: '等待握手',
      session: '已连接',
      closed: '未连接',
    }
    document.getElementById('phase').textContent = phaseText[s.phase] || s.phase
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
  const button = document.getElementById('save')
  button.disabled = true
  chrome.runtime.sendMessage({ type: 'setWsUrl', wsUrl: url }, (response) => {
    button.disabled = false
    if (chrome.runtime.lastError) {
      alert(`保存失败：${chrome.runtime.lastError.message}`)
      return
    }
    if (!response?.ok) {
      alert(`保存失败：${response?.error || '未知错误'}`)
      return
    }
    document.getElementById('wsUrl').value = response.wsUrl || url
    alert('已保存,重新加载扩展生效')
  })
})

refresh()
setInterval(refresh, 2000)
