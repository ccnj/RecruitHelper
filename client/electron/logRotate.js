// 运行日志的按大小多代轮转(2026-07-31 甲方裁决"方案 A")。
//
// 换掉原来的单代方案,它有两个毛病:轮转只在进程启动时检查一次,客户端连开几天
// 文件就无限涨、32MB 上限形同虚设;而且只留一份 .old、每次启动直接覆盖——用户
// 遇到问题后重启两次,出问题那段日志就永久没了,而那正是现场上报最该带回来的东西。
//
// 这里改成:运行期按累计写入字节触发,保留 5 代。
//
// 两个实现要点:
// 1. 先关流再改名。Windows 不允许 rename 正在打开的文件,POSIX 允许——按能跑在
//    Windows 的写法来,那是客户端的发布目标。
// 2. 关流是异步的,这期间来的日志行进 pending 缓冲,新流开好后补写。否则轮转
//    那一瞬间的日志会丢,而故障往往就发生在日志暴涨的那一刻。
'use strict'
const fs = require('node:fs')
const path = require('node:path')

const DEFAULT_ROTATE_BYTES = 16 * 1024 * 1024
const DEFAULT_KEEP = 5

/**
 * 把 brain.log 挪成 brain.log.1，原 .1 挪成 .2，依此类推；最老的一代被覆盖。
 * 同步执行：调用方必须已经关掉写入流。
 */
function rotateFiles(logPath, keep = DEFAULT_KEEP) {
  for (let index = keep - 1; index >= 1; index -= 1) {
    const from = `${logPath}.${index}`
    if (fs.existsSync(from)) {
      fs.renameSync(from, `${logPath}.${index + 1}`)
    }
  }
  if (fs.existsSync(logPath)) {
    fs.renameSync(logPath, `${logPath}.1`)
  }
}

/** 上报打包要取的日志文件名，固定枚举而不是扫目录。含旧格式 .old 以免已装机器上的历史丢失。 */
function logFileNames(name = 'brain.log', keep = DEFAULT_KEEP) {
  const names = [name]
  for (let index = 1; index <= keep; index += 1) {
    names.push(`${name}.${index}`)
  }
  names.push(`${name}.old`)
  return names
}

class RotatingLog {
  /** @param {{dir:string, name?:string, rotateBytes?:number, keep?:number}} opts */
  constructor(opts) {
    this.dir = opts.dir
    this.name = opts.name || 'brain.log'
    this.rotateBytes = opts.rotateBytes || DEFAULT_ROTATE_BYTES
    this.keep = opts.keep || DEFAULT_KEEP
    this.logPath = path.join(this.dir, this.name)
    this.stream = null
    this.bytes = 0
    this.rotating = false
    this.pending = []
  }

  open() {
    try {
      fs.mkdirSync(this.dir, { recursive: true })
      // 上次运行结束时可能正好压线,启动先补一次判断。
      if (fs.existsSync(this.logPath) && fs.statSync(this.logPath).size >= this.rotateBytes) {
        rotateFiles(this.logPath, this.keep)
      }
    } catch {
      // 目录建不了就让下面开流失败，降级为只有控制台。
    }
    this.openStream()
    return this
  }

  openStream() {
    try {
      this.stream = fs.createWriteStream(this.logPath, { flags: 'a' })
      // 追加模式要从文件现有大小接着计，否则重启后能涨到远超阈值才轮转。
      this.bytes = fs.existsSync(this.logPath) ? fs.statSync(this.logPath).size : 0
    } catch {
      this.stream = null
      this.bytes = 0
    }
  }

  write(line) {
    const payload = `${line}\n`
    if (this.rotating) {
      this.pending.push(payload)
      return
    }
    if (!this.stream) return
    this.stream.write(payload)
    this.bytes += Buffer.byteLength(payload)
    if (this.bytes >= this.rotateBytes) {
      this.roll()
    }
  }

  roll() {
    const stream = this.stream
    if (!stream) return
    this.rotating = true
    this.stream = null
    stream.end(() => {
      try {
        rotateFiles(this.logPath, this.keep)
      } catch {
        // 改名失败就继续往原文件写：日志断了比日志长了更糟。
      }
      this.openStream()
      this.rotating = false
      const buffered = this.pending
      this.pending = []
      for (const payload of buffered) {
        if (!this.stream) break
        this.stream.write(payload)
        this.bytes += Buffer.byteLength(payload)
      }
    })
  }

  /** 等待轮转收尾。仅测试与退出流程需要。 */
  async settled() {
    while (this.rotating) {
      await new Promise((resolve) => setTimeout(resolve, 5))
    }
  }

  close() {
    if (!this.stream) return
    this.stream.end()
    this.stream = null
  }
}

module.exports = {
  RotatingLog,
  rotateFiles,
  logFileNames,
  DEFAULT_ROTATE_BYTES,
  DEFAULT_KEEP,
}
