// 诊断台外壳：侧栏导航 + 常驻仪表带 + 当前页。
//
// 所有跨页共享的状态都留在这一层：所选账号、所选会话、忙碌闸、操作回执，
// 以及 health/hands/accounts/conversations/messages/audits 六条轮询。轮询
// 无条件运行，与当前在哪一页无关——拆页前它们也是无条件跑的，切页不该改变
// 脑侧的读取节奏，更不该让"换一页再换回来"变成一次隐式重置。
import { useEffect, useState } from 'react'
import { api, Health, MutationResult } from '../api'
import { ConsoleSidebar, type ConsolePage } from './ConsoleSidebar'
import { Suspects } from './Suspects'
import { accountIdentity, errorText } from './format'
import { AccountPage } from './pages/AccountPage'
import { ConversationPage } from './pages/ConversationPage'
import { DevToolsPage } from './pages/DevToolsPage'
import { JobPublishPage } from './pages/JobPublishPage'
import { ModelConfigPage } from './pages/ModelConfigPage'
import { OverviewPage } from './pages/OverviewPage'
import { HandsBar, ServiceSeal } from './shared/Topbar'
import { usePolling } from './usePolling'

export function ConsoleApp() {
  const [activePage, setActivePage] = useState<ConsolePage>('overview')
  const health = usePolling<Health>(api.health, 1800, 'health')
  const hands = usePolling(api.handsHealth, 1500, 'hands')
  const accounts = usePolling(api.accounts, 2400, 'accounts')
  const [selectedAccountKey, setSelectedAccountKey] = useState('')
  const [selectedConversationRef, setSelectedConversationRef] = useState('')
  const [activityTab, setActivityTab] = useState<'messages' | 'audits'>('messages')
  const [busy, setBusy] = useState('')
  const [notice, setNotice] = useState<{ kind: 'ok' | 'bad'; text: string } | null>(null)

  const accountRows = accounts.data?.accounts ?? []
  const selectedAccount = accountRows.find((row) => accountIdentity(row) === selectedAccountKey) ?? null
  const accountKey = selectedAccount ? accountIdentity(selectedAccount) : 'none'
  const conversations = usePolling(
    () => selectedAccount
      ? api.conversations(selectedAccount.platform, selectedAccount.accountRef)
      : Promise.resolve({ conversations: [] }),
    2800,
    accountKey,
  )
  const selectedConversation = (conversations.data?.conversations ?? [])
    .find((row) => row.conversationRef === selectedConversationRef) ?? null
  const conversationKey = selectedConversation ? `${accountKey}:${selectedConversation.conversationRef}` : `${accountKey}:none`
  const messages = usePolling(
    () => selectedAccount && selectedConversation
      ? api.messages(selectedAccount.platform, selectedAccount.accountRef, selectedConversation.conversationRef)
      : Promise.resolve({ messages: [] }),
    3200,
    conversationKey,
  )
  const audits = usePolling(
    () => selectedAccount
      ? api.audits(selectedAccount.platform, selectedAccount.accountRef)
      : Promise.resolve({ audits: [] }),
    4200,
    accountKey,
  )

  useEffect(() => {
    if (accountRows.length === 0) {
      setSelectedAccountKey('')
      return
    }
    if (!accountRows.some((row) => accountIdentity(row) === selectedAccountKey)) {
      setSelectedAccountKey(accountIdentity(accountRows[0]))
    }
  }, [accountRows, selectedAccountKey])

  useEffect(() => {
    const rows = conversations.data?.conversations ?? []
    if (rows.length === 0) {
      setSelectedConversationRef('')
      return
    }
    if (!rows.some((row) => row.conversationRef === selectedConversationRef)) {
      setSelectedConversationRef(rows[0].conversationRef)
    }
  }, [accountKey, conversations.data, selectedConversationRef])

  const runMutation = async (key: string, success: string, operation: () => Promise<MutationResult>) => {
    setBusy(key)
    setNotice(null)
    try {
      const result = await operation()
      if (result.error) throw new Error(result.error)
      if (result.account?.accountRef) setSelectedAccountKey(accountIdentity(result.account))
      setNotice({ kind: 'ok', text: success })
      accounts.refresh()
      conversations.refresh()
      messages.refresh()
      audits.refresh()
    } catch (reason) {
      setNotice({ kind: 'bad', text: errorText(reason) })
    } finally {
      setBusy('')
    }
  }

  const target = selectedAccount
    ? { platform: selectedAccount.platform, accountRef: selectedAccount.accountRef }
    : null

  let page: JSX.Element
  if (activePage === 'account') {
    page = (
      <AccountPage
        accounts={accountRows}
        accountsError={accounts.error}
        accountsLoading={accounts.loading}
        hands={hands.data?.hands ?? []}
        selectedKey={selectedAccountKey}
        selectedAccount={selectedAccount}
        accountKey={accountKey}
        busy={busy}
        onSelect={setSelectedAccountKey}
        onBind={(platform, handId, accountRef) => runMutation(
          accountRef ? 'rebind' : 'bind',
          accountRef ? '账号绑定已更新' : '当前平台账号已加入值班台',
          () => api.bindAccount(platform, handId, accountRef),
        )}
        onEnable={() => target && runMutation('enable', '已开启今天的自动巡检', () => api.enableAccount(target))}
        onStop={() => target && runMutation('stop', '今天的自动巡检已停止', () => api.stopAccount(target))}
        onPause={() => target && runMutation('pause', '已立即暂停，等待人工恢复', () => api.pauseAccount(target))}
        onRun={() => target && runMutation('run', '已请求立即巡检一轮', () => api.runAccount(target))}
      />
    )
  } else if (activePage === 'conversation') {
    page = (
      <ConversationPage
        account={selectedAccount}
        accountsError={accounts.error}
        accountsLoading={accounts.loading}
        conversations={conversations.data?.conversations ?? []}
        conversationsLoading={conversations.loading}
        conversationsError={conversations.error}
        selectedConversationRef={selectedConversationRef}
        selectedConversation={selectedConversation}
        messages={messages.data?.messages ?? []}
        audits={audits.data?.audits ?? []}
        messagesError={messages.error}
        auditsError={audits.error}
        tab={activityTab}
        busy={busy}
        onTab={setActivityTab}
        onSelectConversation={(conversationRef) => {
          setSelectedConversationRef(conversationRef)
          setActivityTab('messages')
        }}
        onTrack={() => selectedAccount && selectedConversation && runMutation(
          'track',
          '该会话已纳入跟踪；现有消息只建立基线，不算作新消息',
          () => api.trackConversation(selectedAccount.platform, selectedAccount.accountRef, selectedConversation.conversationRef),
        )}
        onSelectM5Trial={() => selectedAccount && selectedConversation && runMutation(
          'm5-trial',
          '该档案已被明确选为 M5 简历补采试运行；巡检会沿正式路径执行一次',
          () => api.selectM5Trial(selectedAccount.platform, selectedAccount.accountRef, selectedConversation.conversationRef),
        )}
        onSendChanged={() => {
          conversations.refresh()
          messages.refresh()
          audits.refresh()
        }}
      />
    )
  } else if (activePage === 'jobPublish') {
    page = <JobPublishPage account={selectedAccount} />
  } else if (activePage === 'modelConfig') {
    page = <ModelConfigPage />
  } else if (activePage === 'devTools') {
    page = <DevToolsPage />
  } else {
    page = (
      <OverviewPage
        hands={hands.data?.hands ?? []}
        busy={busy}
        canProcessCurrent={Boolean(target) && Boolean(selectedAccount?.handOnline)}
        onProcessCurrent={() => target && runMutation(
          'process-current',
          '已处理浏览器当前打开会话一次',
          () => api.processCurrentConversationOnce(target),
        )}
      />
    )
  }

  return (
    <div className="dc-shell">
      <ConsoleSidebar activePage={activePage} onNavigate={setActivePage} />
      <div className="dc-main">
        <header className="dc-topbar">
          <ServiceSeal health={health.data} error={health.error} />
          <HandsBar hands={hands.data?.hands ?? []} onRefresh={hands.refresh} />
        </header>

        {notice && (
          <div className={`notice ${notice.kind}`} role="status" aria-live="polite">
            <strong>{notice.text}</strong>
            <button className="icon-button" onClick={() => setNotice(null)} aria-label="关闭提示">×</button>
          </div>
        )}

        {/* suspect 队列不属于任何一页：它挡住的是后续所有发送，藏起来就等于
            让人在别的页面上看不见自己被卡住了。 */}
        <Suspects />

        {page}
      </div>
    </div>
  )
}
