// 页面级错误边界。
//
// 立此边界的起因：2026-08-02 客户机跑十个职位的阶段 A，脑返回的一个 null 数组
// 让渲染时抛了 TypeError，React 把**整棵树**卸掉——诊断台整个变白，连侧栏、
// suspect 队列和已经跑完的阶段 A 结果一起没了，而且不会自己恢复。
//
// 诊断台恰恰是出事时唯一能看的东西，它不该被任何一处渲染异常整体带走。边界只
// 包住当前页：出错时这一页换成一张可读的错误卡，外壳、导航与 suspect 队列照常
// 活着，换一页或点重试就能继续用。
//
// 它不是"容错"，不掩盖问题：错误原文与堆栈原样显示出来，并照常抛进 console，
// 便于照着排查。
import { Component, ErrorInfo, ReactNode } from 'react'

interface Props {
  /** 换页时重置边界：新页面不该继承上一页的错误态。 */
  resetKey: string
  children: ReactNode
}

interface State {
  error: Error | null
  stack: string
}

export class ConsoleErrorBoundary extends Component<Props, State> {
  state: State = { error: null, stack: '' }

  static getDerivedStateFromError(error: Error): Partial<State> {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // 照常抛进 console：边界只防止整棵树被卸掉，不吞掉排查线索。
    console.error('诊断台页面渲染失败', error, info.componentStack)
    this.setState({ stack: info.componentStack ?? '' })
  }

  componentDidUpdate(previous: Props) {
    if (previous.resetKey !== this.props.resetKey && this.state.error) {
      this.setState({ error: null, stack: '' })
    }
  }

  render() {
    const { error, stack } = this.state
    if (!error) return this.props.children
    return (
      <section className="dc-boundary" role="alert">
        <strong>这一页渲染失败了</strong>
        <p className="dc-boundary-message">{error.message || String(error)}</p>
        <p className="dc-note">
          侧栏、suspect 队列与脑都还活着；换一页或点下面的重试可以继续用。
          已经跑完的结果留在脑里，重新读一次就能看到。
        </p>
        <button type="button" onClick={() => this.setState({ error: null, stack: '' })}>
          重试渲染这一页
        </button>
        {stack && (
          <details className="dc-boundary-stack">
            <summary>组件栈</summary>
            <pre className="mono">{stack.trim()}</pre>
          </details>
        )}
      </section>
    )
  }
}
