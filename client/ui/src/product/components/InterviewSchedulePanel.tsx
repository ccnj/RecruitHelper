import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  readInterviewSchedule,
  saveInterviewSchedule,
  type InterviewSchedule,
  type InterviewWindow,
} from '../api'

// 网格 08:00-21:00,每小时一格。最后一格是 20:00-21:00。
const GRID_START_HOUR = 8
const GRID_END_HOUR = 21

const FALLBACK_WEEKDAYS = ['周一', '周二', '周三', '周四', '周五', '周六', '周日']

function hourLabels(): string[] {
  const labels: string[] = []
  for (let hour = GRID_START_HOUR; hour < GRID_END_HOUR; hour += 1) {
    labels.push(`${String(hour).padStart(2, '0')}:00`)
  }
  return labels
}

function nextHour(clock: string): string {
  const hour = Number(clock.slice(0, 2)) + 1
  return `${String(hour).padStart(2, '0')}:00`
}

/** 把窗口展开成"小时起点"集合,便于逐格判断选中态。 */
export function expandToCells(windows: InterviewWindow[] | undefined): Set<string> {
  const cells = new Set<string>()
  for (const window of windows ?? []) {
    let clock = window.start
    while (clock < window.end) {
      cells.add(clock)
      clock = nextHour(clock)
    }
  }
  return cells
}

/** 把小时起点集合合并回连续窗口,相邻小时并成一段。 */
export function mergeToWindows(cells: string[]): InterviewWindow[] {
  if (cells.length === 0) return []
  const sorted = [...new Set(cells)].sort()
  const windows: InterviewWindow[] = []
  let start = sorted[0]
  let last = sorted[0]
  for (let index = 1; index < sorted.length; index += 1) {
    if (nextHour(last) === sorted[index]) {
      last = sorted[index]
    } else {
      windows.push({ start, end: nextHour(last) })
      start = sorted[index]
      last = sorted[index]
    }
  }
  windows.push({ start, end: nextHour(last) })
  return windows
}

export function countHours(schedule: InterviewSchedule, weekdays: string[]): number {
  return weekdays.reduce((total, day) => total + expandToCells(schedule[day]).size, 0)
}

export function defaultSchedule(weekdays: string[]): InterviewSchedule {
  const schedule: InterviewSchedule = {}
  weekdays.forEach((day, index) => {
    schedule[day] = index < 5 ? [{ start: '09:00', end: '18:00' }] : []
  })
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

  const hours = useMemo(() => hourLabels(), [])

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
          if (dragTurnsOn.current) cells.add(hours[rowIndex])
          else cells.delete(hours[rowIndex])
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
  }, [dragOrigin, dragCurrent, schedule, weekdays, hours, persist])

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
          按住鼠标拖过格子可连续选择或取消。AI 只会在这些格子里挑面试时间,改动立即生效,
          对已经开始的那轮对话不追溯。
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
            {hours.map((clock, rowIndex) => (
              <Fragment key={clock}>
                <div className="rh-schedule-clock">{clock}</div>
                {weekdays.map((day) => {
                  const selected = selectedCells[day]?.has(clock) ?? false
                  const dragging = insideDragRect(day, rowIndex)
                  const shown = dragging ? dragTurnsOn.current : selected
                  return (
                    <div
                      key={day}
                      role="gridcell"
                      aria-label={`${day} ${clock}`}
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
          </div>
          </div>

          <aside className="rh-schedule-summary">
            <span className="rh-section-label">已选时间</span>
            <strong>共 {totalHours} 个小时</strong>
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
