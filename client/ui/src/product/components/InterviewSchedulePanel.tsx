import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  readInterviewSchedule,
  saveInterviewSchedule,
  type InterviewSchedule,
  type InterviewWindow,
} from '../api'

// 网格 08:00-21:00,每半小时一格(2026-09-04 甲方裁决,此前每小时一格),最后一格是
// 20:30-21:00。步长与脑侧 m5ai.InterviewSlotStepMinutes 同值,两端各存一份;
// test/product-interview-schedule.test.mjs 里默认周表"共 63 小时"那条断言是对齐点。
const GRID_START = '08:00'
const GRID_END = '21:00'
const STEP_MINUTES = 30

const FALLBACK_WEEKDAYS = ['周一', '周二', '周三', '周四', '周五', '周六', '周日']

function toMinutes(clock: string): number {
  return Number(clock.slice(0, 2)) * 60 + Number(clock.slice(3, 5))
}

function toClock(minutes: number): string {
  return `${String(Math.floor(minutes / 60)).padStart(2, '0')}:${String(minutes % 60).padStart(2, '0')}`
}

/** 网格每一行的起点时刻:08:00、08:30 … 20:30。 */
function cellStarts(): string[] {
  const starts: string[] = []
  for (let minutes = toMinutes(GRID_START); minutes < toMinutes(GRID_END); minutes += STEP_MINUTES) {
    starts.push(toClock(minutes))
  }
  return starts
}

function nextCell(clock: string): string {
  return toClock(toMinutes(clock) + STEP_MINUTES)
}

/** 把窗口展开成"半小时格起点"集合,便于逐格判断选中态。 */
export function expandToCells(windows: InterviewWindow[] | undefined): Set<string> {
  const cells = new Set<string>()
  for (const window of windows ?? []) {
    let clock = window.start
    // 24:00 是硬上界:起止格式坏掉时字符串比较可能永远为真,不能靠它收敛。
    while (clock < window.end && toMinutes(clock) < 24 * 60) {
      cells.add(clock)
      clock = nextCell(clock)
    }
  }
  return cells
}

/** 把半小时格起点集合合并回连续窗口,相邻格并成一段。 */
export function mergeToWindows(cells: string[]): InterviewWindow[] {
  if (cells.length === 0) return []
  const sorted = [...new Set(cells)].sort()
  const windows: InterviewWindow[] = []
  let start = sorted[0]
  let last = sorted[0]
  for (let index = 1; index < sorted.length; index += 1) {
    if (nextCell(last) === sorted[index]) {
      last = sorted[index]
    } else {
      windows.push({ start, end: nextCell(last) })
      start = sorted[index]
      last = sorted[index]
    }
  }
  windows.push({ start, end: nextCell(last) })
  return windows
}

/** 已选小时数,半小时格计 0.5。 */
export function countHours(schedule: InterviewSchedule, weekdays: string[]): number {
  const cells = weekdays.reduce((total, day) => total + expandToCells(schedule[day]).size, 0)
  return (cells * STEP_MINUTES) / 60
}

function formatHours(total: number): string {
  return Number.isInteger(total) ? String(total) : total.toFixed(1)
}

/** 与脑侧 m5ai.DefaultInterviewSchedule 保持一致：七天全 09:00-18:00。 */
export function defaultSchedule(weekdays: string[]): InterviewSchedule {
  const schedule: InterviewSchedule = {}
  for (const day of weekdays) {
    schedule[day] = [{ start: '09:00', end: '18:00' }]
  }
  return schedule
}

interface DragCell {
  day: string
  rowIndex: number
}

type SaveState =
  | { kind: 'idle' }
  | { kind: 'saving' }
  | { kind: 'saved' }
  | { kind: 'error'; message: string }

export function InterviewSchedulePanel() {
  const [schedule, setSchedule] = useState<InterviewSchedule | null>(null)
  const [weekdays, setWeekdays] = useState<string[]>(FALLBACK_WEEKDAYS)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [saveState, setSaveState] = useState<SaveState>({ kind: 'idle' })

  const rows = useMemo(() => cellStarts(), [])

  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        const response = await readInterviewSchedule()
        if (cancelled) return
        const days = response.weekdays?.length ? response.weekdays : FALLBACK_WEEKDAYS
        setWeekdays(days)
        setSchedule(response.schedule ?? {})
        setLoadError(null)
      } catch (error) {
        if (cancelled) return
        // 读不出来就不给可编辑的网格 —— 显示一张猜出来的表,用户会以为那就是生效的配置。
        setLoadError(error instanceof Error ? error.message : '可面试时段读取失败')
      }
    })()
    return () => {
      cancelled = true
    }
  }, [])

  const persist = useCallback(async (next: InterviewSchedule, previous: InterviewSchedule) => {
    setSaveState({ kind: 'saving' })
    try {
      await saveInterviewSchedule(next)
      setSaveState({ kind: 'saved' })
    } catch (error) {
      // 保存失败必须把界面退回落库前的样子。否则界面显示新表、库里还是旧表,
      // 用户以为改好了 —— 这正是老项目那个前后端不一致的坑。
      setSchedule(previous)
      setSaveState({
        kind: 'error',
        message: error instanceof Error ? error.message : '保存失败',
      })
    }
  }, [])

  const [dragOrigin, setDragOrigin] = useState<DragCell | null>(null)
  const [dragCurrent, setDragCurrent] = useState<DragCell | null>(null)
  const dragTurnsOn = useRef(true)

  // 全局 mouseup:拖到表外释放也要能提交,否则会留下悬空的拖拽态。
  useEffect(() => {
    if (!dragOrigin || !dragCurrent || !schedule) return
    function commit() {
      const origin = dragOrigin as DragCell
      const current = dragCurrent as DragCell
      const base = schedule as InterviewSchedule
      const dayFrom = Math.min(weekdays.indexOf(origin.day), weekdays.indexOf(current.day))
      const dayTo = Math.max(weekdays.indexOf(origin.day), weekdays.indexOf(current.day))
      const rowFrom = Math.min(origin.rowIndex, current.rowIndex)
      const rowTo = Math.max(origin.rowIndex, current.rowIndex)

      const next: InterviewSchedule = { ...base }
      for (let dayIndex = dayFrom; dayIndex <= dayTo; dayIndex += 1) {
        const day = weekdays[dayIndex]
        const cells = expandToCells(next[day])
        for (let rowIndex = rowFrom; rowIndex <= rowTo; rowIndex += 1) {
          if (dragTurnsOn.current) cells.add(rows[rowIndex])
          else cells.delete(rows[rowIndex])
        }
        next[day] = mergeToWindows([...cells])
      }
      setDragOrigin(null)
      setDragCurrent(null)
      // 甲方裁决:至少保留一个时段。整次拖拽作废而不是保底留一格 ——
      // 悄悄替用户决定留哪一格,比拒绝更难解释。脑侧另有同一道校验兜底。
      if (countHours(next, weekdays) === 0) {
        setSaveState({ kind: 'error', message: '至少要保留一个可面试时段,本次修改未保存' })
        return
      }
      setSchedule(next)
      void persist(next, base)
    }
    window.addEventListener('mouseup', commit)
    return () => window.removeEventListener('mouseup', commit)
  }, [dragOrigin, dragCurrent, schedule, weekdays, rows, persist])

  function insideDragRect(day: string, rowIndex: number): boolean {
    if (!dragOrigin || !dragCurrent) return false
    const dayIndex = weekdays.indexOf(day)
    const dayFrom = Math.min(weekdays.indexOf(dragOrigin.day), weekdays.indexOf(dragCurrent.day))
    const dayTo = Math.max(weekdays.indexOf(dragOrigin.day), weekdays.indexOf(dragCurrent.day))
    const rowFrom = Math.min(dragOrigin.rowIndex, dragCurrent.rowIndex)
    const rowTo = Math.max(dragOrigin.rowIndex, dragCurrent.rowIndex)
    return dayIndex >= dayFrom && dayIndex <= dayTo && rowIndex >= rowFrom && rowIndex <= rowTo
  }

  const selectedCells = useMemo(() => {
    const map: Record<string, Set<string>> = {}
    for (const day of weekdays) map[day] = expandToCells(schedule?.[day])
    return map
  }, [schedule, weekdays])

  const totalHours = useMemo(
    () => (schedule ? countHours(schedule, weekdays) : 0),
    [schedule, weekdays],
  )

  if (loadError) {
    return (
      <section className="rh-panel rh-schedule-panel">
        <div className="rh-panel-heading">
          <div>
            <span className="rh-section-label">面试时间</span>
            <h2>可面试时段</h2>
          </div>
        </div>
        <div className="rh-schedule-load-error">
          <strong>读取失败,暂时不能修改</strong>
          <p>{loadError}</p>
        </div>
      </section>
    )
  }

  if (!schedule) {
    return (
      <section className="rh-panel rh-schedule-panel">
        <div className="rh-panel-heading">
          <div>
            <span className="rh-section-label">面试时间</span>
            <h2>可面试时段</h2>
          </div>
        </div>
        <div className="rh-schedule-loading">读取中…</div>
      </section>
    )
  }

  return (
    <section className="rh-panel rh-schedule-panel">
      <div className="rh-panel-heading">
        <div>
          <span className="rh-section-label">面试时间</span>
          <h2>可面试时段</h2>
        </div>
        <div className="rh-schedule-actions">
          <span className={`rh-schedule-state is-${saveState.kind}`}>
            {saveState.kind === 'saving' && '保存中…'}
            {saveState.kind === 'saved' && '已保存'}
            {saveState.kind === 'error' && saveState.message}
          </span>
          <button
            type="button"
            className="rh-button is-quiet"
            onClick={() => {
              const base = schedule
              const next = defaultSchedule(weekdays)
              setSchedule(next)
              void persist(next, base)
            }}
          >
            恢复默认
          </button>
        </div>
      </div>

      <div className="rh-schedule-body">
        <p className="rh-schedule-hint">
          每格半小时,时间写在格子的分界线上。按住鼠标拖过格子可连续选择或取消。
          AI 只会在这些格子里挑面试时间,改动立即生效,对已经开始的那轮对话不追溯。
        </p>

        <div className="rh-schedule-layout">
          <div className="rh-schedule-scroll">
          <div
            className="rh-schedule-grid"
            style={{ gridTemplateColumns: `64px repeat(${weekdays.length}, minmax(44px, 1fr))` }}
          >
            <div className="rh-schedule-corner">时间</div>
            {weekdays.map((day) => (
              <div key={day} className="rh-schedule-head">
                {day}
              </div>
            ))}
            {rows.map((clock, rowIndex) => (
              <Fragment key={clock}>
                {/* 标签压在本行上缘的缝上;半点标签淡一档,整点一眼能找到。 */}
                <div className={'rh-schedule-clock' + (clock.endsWith(':30') ? ' is-half' : '')}>
                  <span>{clock}</span>
                </div>
                {weekdays.map((day) => {
                  const selected = selectedCells[day]?.has(clock) ?? false
                  const dragging = insideDragRect(day, rowIndex)
                  const shown = dragging ? dragTurnsOn.current : selected
                  return (
                    <div
                      key={day}
                      role="gridcell"
                      aria-label={`${day} ${clock}-${nextCell(clock)}`}
                      aria-selected={shown}
                      className={
                        'rh-schedule-cell' +
                        (shown ? ' is-on' : '') +
                        (dragging ? ' is-preview' : '')
                      }
                      onMouseDown={(event) => {
                        event.preventDefault()
                        dragTurnsOn.current = !selected
                        setDragOrigin({ day, rowIndex })
                        setDragCurrent({ day, rowIndex })
                      }}
                      onMouseEnter={() => {
                        if (dragOrigin) setDragCurrent({ day, rowIndex })
                      }}
                    />
                  )
                })}
              </Fragment>
            ))}
            {/* 收尾一行高度为零,只为把 21:00 压在末格下缘的缝上。 */}
            <div className="rh-schedule-clock is-end">
              <span>{GRID_END}</span>
            </div>
            {weekdays.map((day) => (
              <div key={day} className="rh-schedule-cell-end" aria-hidden="true" />
            ))}
          </div>
          </div>

          <aside className="rh-schedule-summary">
            <span className="rh-section-label">已选时间</span>
            <strong>共 {formatHours(totalHours)} 个小时</strong>
            <div className="rh-schedule-summary-list">
              {weekdays.map((day) => {
                const windows = schedule[day] ?? []
                return (
                  <div key={day} className="rh-schedule-summary-day">
                    <span className="rh-schedule-summary-label">{day}</span>
                    <div className="rh-schedule-summary-slots">
                      {windows.length === 0 ? (
                        <span className="rh-schedule-summary-empty">—</span>
                      ) : (
                        windows.map((window) => (
                          <span key={window.start} className="rh-schedule-chip">
                            {window.start}–{window.end}
                          </span>
                        ))
                      )}
                    </div>
                  </div>
                )
              })}
            </div>
          </aside>
        </div>
      </div>
    </section>
  )
}
