// 预加载:M1 UI 直接 fetch 本地 admin(同机 localhost),暂不需要 IPC 桥。
// 业务批次接入云端/敏感操作时,再经此暴露受控 window.api。
'use strict'
