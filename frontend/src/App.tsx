import { useEffect, useRef, useState } from 'react'
import { Events } from '@wailsio/runtime'
import { ChatService } from '../bindings/github.com/CycleZero/rym-desktop'

type User = { id: number; username: string; nickname: string }
type Message = {
  msg_id: string
  conversation_id: number
  seq: number
  sender_id: number
  body: string
  created_at: string
  client_msg_id?: string
  // 仅本地未确认消息：sending / failed；服务端消息为空。
  state?: string
  error?: string
}
type Conversation = {
  id: number
  type: number
  last_seq: number
  read_seq: number
  unread_count: number
  peer_read_seq: number
  peer_id: number
  peer?: User
  last_message?: Message
}
type SessionInfo = { user: User; server_url: string; ws_port: number }

const styles = `
  :root { color-scheme: dark; }
  * { box-sizing: border-box; }
  body { margin: 0; font-family: Inter, system-ui, -apple-system, "Segoe UI", sans-serif; background: #12141c; color: #e7eaf3; }
  .r-root { height: 100vh; display: flex; flex-direction: column; }
  /* 登录页必须可滚动：Windows 常见 150%/200% 缩放下 760px 高的窗口
     装不下整张卡片，之前按钮会被挤到可视区外且无法触达。 */
  .r-login-wrap { flex: 1; min-height: 0; overflow: auto; padding: 24px 16px; display: flex; flex-direction: column; justify-content: safe center; }
  .r-login { width: 360px; max-width: 100%; margin: 0 auto; flex: none; padding: 28px; background: #1b1e29; border: 1px solid #2b3040; border-radius: 14px; }
  .r-login h1 { margin: 0 0 6px; font-size: 22px; }
  .r-login p { margin: 0 0 20px; color: #8b93a7; font-size: 13px; }
  .r-field { margin-bottom: 12px; }
  .r-field label { display: block; font-size: 12px; color: #8b93a7; margin-bottom: 5px; }
  .r-field input { width: 100%; padding: 9px 10px; background: #12141c; border: 1px solid #2b3040; border-radius: 8px; color: #e7eaf3; outline: none; }
  .r-field input:focus { border-color: #5b8cff; }
  .r-btn { width: 100%; padding: 10px; border: 0; border-radius: 8px; background: #4f7cff; color: white; font-weight: 600; cursor: pointer; }
  .r-btn.secondary { background: #2b3040; }
  .r-btn:disabled { opacity: .5; cursor: default; }
  .r-switch { margin-top: 12px; text-align: center; font-size: 12px; color: #8b93a7; cursor: pointer; }
  .r-error { margin-top: 12px; color: #ff8b8b; font-size: 12px; white-space: pre-wrap; }
  .r-main { flex: 1; display: flex; min-height: 0; }
  .r-side { width: 320px; border-right: 1px solid #232838; display: flex; flex-direction: column; min-height: 0; }
  .r-side-head { padding: 12px 14px; border-bottom: 1px solid #232838; display: flex; align-items: center; gap: 8px; }
  .r-side-head strong { flex: 1; font-size: 14px; }
  .r-dot { width: 8px; height: 8px; border-radius: 50%; background: #ff6b6b; }
  .r-dot.on { background: #43d17a; }
  .r-search { padding: 10px 12px; border-bottom: 1px solid #232838; }
  .r-search input { width: 100%; padding: 8px 10px; background: #12141c; border: 1px solid #2b3040; border-radius: 8px; color: #e7eaf3; }
  .r-results { max-height: 220px; overflow: auto; border-bottom: 1px solid #232838; }
  .r-result { padding: 9px 12px; cursor: pointer; display: flex; gap: 8px; align-items: center; }
  .r-result:hover { background: #1b1e29; }
  .r-avatar { width: 30px; height: 30px; border-radius: 50%; background: #2b3040; display: grid; place-items: center; font-size: 12px; color: #b9c2d8; flex: none; }
  .r-convs { flex: 1; overflow: auto; }
  .r-conv { padding: 11px 14px; cursor: pointer; border-bottom: 1px solid #1b1f2c; }
  .r-conv:hover { background: #1b1e29; }
  .r-conv.active { background: #232a3d; }
  .r-conv-top { display: flex; justify-content: space-between; gap: 8px; font-size: 13px; }
  .r-conv-top span:first-child { font-weight: 600; }
  .r-conv-time { color: #6f7890; font-size: 11px; flex: none; }
  .r-conv-preview { margin-top: 4px; color: #8b93a7; font-size: 12px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .r-badge { min-width: 18px; height: 18px; padding: 0 5px; border-radius: 9px; background: #4f7cff; color: white; font-size: 11px; display: grid; place-items: center; }
  .r-empty { padding: 24px 16px; color: #6f7890; font-size: 13px; text-align: center; }
  .r-chat { flex: 1; display: flex; flex-direction: column; min-width: 0; }
  .r-chat-head { padding: 13px 18px; border-bottom: 1px solid #232838; display: flex; align-items: center; gap: 10px; }
  .r-chat-head strong { font-size: 14px; }
  .r-chat-head small { color: #6f7890; }
  .r-messages { flex: 1; overflow: auto; padding: 18px; display: flex; flex-direction: column; gap: 10px; }
  .r-msg { max-width: 68%; padding: 9px 12px; border-radius: 12px; background: #1b1e29; border: 1px solid #2b3040; font-size: 13px; white-space: pre-wrap; word-break: break-word; }
  .r-msg.mine { align-self: flex-end; background: #2e4f9e; border-color: #3d63bd; }
  .r-msg.pending { opacity: .65; }
  .r-msg.failed { background: #3a2027; border-color: #a3404f; }
  .r-msg-meta { font-size: 10px; color: #8b93a7; margin-top: 4px; }
  .r-compose { display: flex; gap: 8px; padding: 12px 16px; border-top: 1px solid #232838; }
  .r-compose input { flex: 1; padding: 10px 12px; background: #12141c; border: 1px solid #2b3040; border-radius: 9px; color: #e7eaf3; }
  .r-compose button { padding: 0 18px; border: 0; border-radius: 9px; background: #4f7cff; color: white; font-weight: 600; cursor: pointer; }
  .r-top-error { padding: 7px 14px; background: #4a1f27; color: #ffb4b4; font-size: 12px; }
`

function textOf(body: string) {
  try {
    const parsed = JSON.parse(body)
    return typeof parsed?.text === 'string' ? parsed.text : body
  } catch {
    return body
  }
}

function peerName(conv: Conversation) {
  return conv.peer?.nickname || conv.peer?.username || `用户 ${conv.peer_id}`
}

function timeOf(rfc: string | undefined) {
  if (!rfc) return ''
  const d = new Date(rfc)
  if (Number.isNaN(d.getTime())) return ''
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

export default function App() {
  const [session, setSession] = useState<SessionInfo | null>(null)
  const [conversations, setConversations] = useState<Conversation[]>([])
  const [selected, setSelected] = useState<Conversation | null>(null)
  const [messages, setMessages] = useState<Message[]>([])
  const [connected, setConnected] = useState(false)
  const [input, setInput] = useState('')
  const [search, setSearch] = useState('')
  const [results, setResults] = useState<User[]>([])
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const [serverURL, setServerURL] = useState('http://127.0.0.1:18080')
  const [wsPort, setWsPort] = useState(8001)
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [nickname, setNickname] = useState('')
  const [registerMode, setRegisterMode] = useState(false)

  const selectedRef = useRef<Conversation | null>(null)
  const registered = useRef(false)
  selectedRef.current = selected

  const loadMessages = async (conv: Conversation) => {
    try {
      const list = (await ChatService.Messages(conv.id, 0, 50)) as unknown as Message[]
      setMessages(list)
      // 未确认的本地消息 seq=0 且排在最后，不能用最后一条算已读。
      const maxSeq = list.reduce((acc, m) => (m.seq > acc ? m.seq : acc), 0)
      if (maxSeq > 0) {
        await ChatService.MarkRead(conv.id, maxSeq)
      }
    } catch (e: any) {
      setError(String(e?.message || e))
    }
  }

  const refreshConversations = async () => {
    try {
      const list = (await ChatService.Conversations()) as unknown as Conversation[]
      setConversations(list)
      const current = selectedRef.current
      if (current) {
        const next = list.find((c) => c.id === current.id)
        if (next) {
          setSelected(next)
          selectedRef.current = next
        }
      }
    } catch (e: any) {
      setError(String(e?.message || e))
    }
  }

  const selectConversation = async (conv: Conversation) => {
    setSelected(conv)
    selectedRef.current = conv
    setError('')
    await loadMessages(conv)
  }

  const applySession = async (sess: SessionInfo) => {
    setSession(sess)
    await refreshConversations()
  }

  useEffect(() => {
    if (registered.current) return
    registered.current = true

    ChatService.AutoLogin()
      .then(([sess, ok]) => {
        if (ok && sess) applySession(sess as unknown as SessionInfo)
      })
      .catch(() => undefined)

    Events.On('im:event', (event: any) => {
      const ev = event?.data ?? event
      const type = ev?.type
      const convID = ev?.data?.conversation_id
      // message.sent / message.failed 是本地发送队列的结果（服务端确认 / 4xx）。
      if (type === 'message.created' || type === 'message.sent' || type === 'message.failed') {
        refreshConversations()
        const current = selectedRef.current
        if (current && current.id === convID) {
          loadMessages(current)
        }
        if (type === 'message.failed' && ev?.data?.error) {
          setError(`消息发送失败：${ev.data.error}`)
        }
      } else if (type === 'read.updated') {
        refreshConversations()
      }
    })

    Events.On('im:connection', (event: any) => {
      const state = event?.data ?? event
      setConnected(!!state?.connected)
      if (state?.reason) setError(String(state.reason))
    })
  }, [])

  const doAuth = async () => {
    setBusy(true)
    setError('')
    try {
      if (registerMode) {
        await ChatService.Register(serverURL, username, password, nickname)
      }
      const sess = (await ChatService.Login(serverURL, Number(wsPort), username, password)) as unknown as SessionInfo
      await applySession(sess)
    } catch (e: any) {
      setError(String(e?.message || e))
    } finally {
      setBusy(false)
    }
  }

  const doLogout = async () => {
    await ChatService.Logout()
    setSession(null)
    setConversations([])
    setSelected(null)
    setMessages([])
    setConnected(false)
  }

  const doSearch = async () => {
    if (!search.trim()) {
      setResults([])
      return
    }
    try {
      setResults((await ChatService.SearchUsers(search.trim())) as unknown as User[])
    } catch (e: any) {
      setError(String(e?.message || e))
    }
  }

  const openUser = async (u: User) => {
    try {
      const conv = (await ChatService.OpenConversation(u.id)) as unknown as Conversation
      setSearch('')
      setResults([])
      await refreshConversations()
      await selectConversation(conv)
    } catch (e: any) {
      setError(String(e?.message || e))
    }
  }

  const send = async () => {
    if (!selected || !input.trim()) return
    const text = input.trim()
    setInput('')
    try {
      // 立即返回“发送中”的本地消息，确认结果由 message.sent 事件推进来。
      const msg = (await ChatService.SendMessage(selected.id, text)) as Message
      setMessages((prev) => {
        const same = prev.some(
          (m) =>
            (msg.msg_id && m.msg_id === msg.msg_id) ||
            (msg.client_msg_id && m.client_msg_id === msg.client_msg_id),
        )
        return same ? prev : [...prev, msg]
      })
      refreshConversations()
    } catch (e: any) {
      setError(String(e?.message || e))
      setInput(text)
    }
  }

  if (!session) {
    return (
      <div className="r-root">
        <style>{styles}</style>
        <div className="r-login-wrap">
        <div className="r-login">
          <h1>rym</h1>
          <p>桌面客户端 · 登录后开始聊天</p>
          <div className="r-field">
            <label>服务端地址</label>
            <input value={serverURL} onChange={(e) => setServerURL(e.target.value)} placeholder="http://127.0.0.1:18080" />
          </div>
          <div className="r-field">
            <label>WS 端口</label>
            <input value={wsPort} onChange={(e) => setWsPort(Number(e.target.value) || 8001)} />
          </div>
          <div className="r-field">
            <label>用户名</label>
            <input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" />
          </div>
          {registerMode && (
            <div className="r-field">
              <label>昵称</label>
              <input value={nickname} onChange={(e) => setNickname(e.target.value)} />
            </div>
          )}
          <div className="r-field">
            <label>密码</label>
            <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" onKeyDown={(e) => e.key === 'Enter' && doAuth()} />
          </div>
          <button className="r-btn" disabled={busy} onClick={doAuth}>{busy ? '请稍候…' : registerMode ? '注册并登录' : '登录'}</button>
          <div className="r-switch" onClick={() => setRegisterMode((v) => !v)}>
            {registerMode ? '已有账号？去登录' : '没有账号？注册一个'}
          </div>
          {error && <div className="r-error">{error}</div>}
        </div>
        </div>
      </div>
    )
  }

  return (
    <div className="r-root">
      <style>{styles}</style>
      {error && <div className="r-top-error">{error}</div>}
      <div className="r-main">
        <aside className="r-side">
          <div className="r-side-head">
            <span className={`r-dot ${connected ? 'on' : ''}`} />
            <strong>{session.user.nickname || session.user.username}</strong>
            <button className="r-btn secondary" style={{ width: 'auto', padding: '4px 8px', fontSize: 12 }} onClick={doLogout}>退出</button>
          </div>
          <div className="r-search">
            <input
              value={search}
              placeholder="搜索用户，回车"
              onChange={(e) => setSearch(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && doSearch()}
            />
          </div>
          {results.length > 0 && (
            <div className="r-results">
              {results.map((u) => (
                <div className="r-result" key={u.id} onClick={() => openUser(u)}>
                  <div className="r-avatar">{(u.nickname || u.username).slice(0, 1).toUpperCase()}</div>
                  <div>
                    <div>{u.nickname || u.username}</div>
                    <div style={{ color: '#6f7890', fontSize: 11 }}>@{u.username}</div>
                  </div>
                </div>
              ))}
            </div>
          )}
          <div className="r-convs">
            {conversations.length === 0 && <div className="r-empty">还没有会话<br />搜索用户开始聊天</div>}
            {conversations.map((c) => (
              <div key={c.id} className={`r-conv ${selected?.id === c.id ? 'active' : ''}`} onClick={() => selectConversation(c)}>
                <div className="r-conv-top">
                  <span>{peerName(c)}</span>
                  <span className="r-conv-time">{timeOf(c.last_message?.created_at)}</span>
                </div>
                <div className="r-conv-top" style={{ marginTop: 2 }}>
                  <span className="r-conv-preview">{c.last_message ? textOf(c.last_message.body) : '暂无消息'}</span>
                  {c.unread_count > 0 && <span className="r-badge">{c.unread_count}</span>}
                </div>
              </div>
            ))}
          </div>
        </aside>

        <main className="r-chat">
          {!selected ? (
            <div className="r-empty" style={{ margin: 'auto' }}>选择一个会话，或搜索用户</div>
          ) : (
            <>
              <div className="r-chat-head">
                <strong>{peerName(selected)}</strong>
                <small>{selected.peer_read_seq > 0 ? `对方已读到 ${selected.peer_read_seq}` : '单聊'}</small>
              </div>
              <div className="r-messages">
                {messages.map((m) => (
                  <div
                    key={m.msg_id || m.client_msg_id || `${m.conversation_id}-${m.seq}`}
                    className={`r-msg ${m.sender_id === session.user.id ? 'mine' : ''} ${m.state === 'sending' ? 'pending' : ''} ${m.state === 'failed' ? 'failed' : ''}`}
                  >
                    {textOf(m.body)}
                    <div className="r-msg-meta">
                      {timeOf(m.created_at)}
                      {m.state === 'sending' && ' · 发送中…'}
                      {m.state === 'failed' && ` · 发送失败${m.error ? `：${m.error}` : ''}`}
                    </div>
                  </div>
                ))}
              </div>
              <div className="r-compose">
                <input
                  value={input}
                  placeholder="输入消息…"
                  onChange={(e) => setInput(e.target.value)}
                  onKeyDown={(e) => e.key === 'Enter' && send()}
                />
                <button onClick={send}>发送</button>
              </div>
            </>
          )}
        </main>
      </div>
    </div>
  )
}
