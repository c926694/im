import { useCallback, useEffect, useMemo, useReducer, useState } from "react";
import type { FormEvent } from "react";
import { chatAPI } from "./api";
import type { Conversation, Session } from "./api";
import { Chat } from "./Chat";
import { emptyTimeline, timelinesReducer } from "./timeline";
import { statusLabels, useRealtime } from "./useRealtime";

const activityTime = new Intl.DateTimeFormat("zh-CN", {
  month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit",
});

export function Workspace({ session, onLogout, onExpired }: {
  session: Session;
  onLogout: () => void;
  onExpired: () => void;
}) {
  const api = useMemo(() => chatAPI(session, onExpired), [session, onExpired]);
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [loadingList, setLoadingList] = useState(true);
  const [listError, setListError] = useState("");
  const [creating, setCreating] = useState(false);
  const [createError, setCreateError] = useState("");
  const [selected, setSelected] = useState<string | null>(null);
  const [timelines, dispatch] = useReducer(timelinesReducer, {});

  const loadConversations = useCallback(async (signal?: AbortSignal) => {
    try {
      const incoming = await api.conversations(signal);
      // 刷新列表期间也会收到推送，保留序号更新的会话活动时间。
      setConversations((current) => {
        const byID = new Map(current.map((conversation) => [conversation.id, conversation]));
        for (const conversation of incoming) {
          const existing = byID.get(conversation.id);
          if (!existing || BigInt(conversation.last_seq) >= BigInt(existing.last_seq)) {
            byID.set(conversation.id, conversation);
          }
        }
        return [...byID.values()];
      });
      setListError("");
    } catch (error) {
      if (!signal?.aborted) setListError((error as Error).message);
    } finally {
      if (!signal?.aborted) setLoadingList(false);
    }
  }, [api]);

  const { status, epoch } = useRealtime(api, (message) => {
    dispatch({ id: message.conversation_id, source: "live", messages: [message] });
    if (!conversations.some((conversation) => conversation.id === message.conversation_id)) {
      // 收到列表之外的会话消息时，刷新列表以取得对方昵称。
      void loadConversations();
    }
  });

  useEffect(() => {
    const controller = new AbortController();
    // 重连后还需要发现断线期间由对方新建的会话。
    void loadConversations(controller.signal);
    return () => controller.abort();
  }, [epoch, loadConversations]);

  async function openDirect(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const username = String(new FormData(form).get("peer_username"));
    setCreating(true);
    setCreateError("");
    try {
      const result = await api.direct(username);
      // 列表接口提供会话的昵称和活动时间，刷新后打开目标会话。
      await loadConversations();
      setSelected(result.id);
      form.reset();
    } catch (error) {
      setCreateError((error as Error).message);
    } finally {
      setCreating(false);
    }
  }

  const ordered = conversations.map((conversation) => {
    const latest = timelines[conversation.id]?.messages.at(-1);
    if (!latest || BigInt(latest.seq) <= BigInt(conversation.last_seq)) return conversation;
    return { ...conversation, last_seq: latest.seq, last_activity_at: latest.created_at };
  }).sort((a, b) => Date.parse(b.last_activity_at) - Date.parse(a.last_activity_at));
  const active = ordered.find((conversation) => conversation.id === selected);

  return (
    <main className="workspace" data-thread-open={Boolean(active)}>
      <aside className="sidebar" aria-label="会话列表">
        <header className="sidebar-header">
          <div className="wordmark"><span className="brand-mark" aria-hidden="true" /><h1>回声</h1></div>
          <span className="connection" data-status={status} role="status">{statusLabels[status]}</span>
        </header>
        <form className="direct-form" onSubmit={openDirect}>
          <label htmlFor="peer-username">对方的用户名</label>
          <div className="direct-fields">
            <input id="peer-username" name="peer_username" autoCapitalize="none" spellCheck={false} required disabled={creating} />
            <button type="submit" disabled={creating}>{creating ? "正在打开…" : "发起单聊"}</button>
          </div>
          {createError && <p className="error" role="alert">{createError}</p>}
        </form>
        <section className="conversations" aria-labelledby="conversations-title">
          <h2 id="conversations-title">会话</h2>
          {listError && <div className="feedback" role="alert">
            <p>{listError}</p><button type="button" onClick={() => void loadConversations()}>刷新会话</button>
          </div>}
          {loadingList && <p className="list-note" role="status">正在加载会话…</p>}
          {!loadingList && !listError && ordered.length === 0 && <p className="list-note">暂无会话</p>}
          <ul>
            {ordered.map((conversation) => (
              <li key={conversation.id}>
                <button type="button" className="conversation" aria-current={selected === conversation.id ? "true" : undefined}
                  onClick={() => setSelected(conversation.id)}>
                  <span className="avatar" aria-hidden="true">{Array.from(conversation.peer_nickname)[0]}</span>
                  <span className="conversation-label">
                    <strong>{conversation.peer_nickname}</strong>
                    <time dateTime={conversation.last_activity_at}>{activityTime.format(new Date(conversation.last_activity_at))}</time>
                  </span>
                </button>
              </li>
            ))}
          </ul>
        </section>
        <footer className="account">
          <span><span>我的用户名</span><strong>{session.username}</strong></span>
          <button type="button" onClick={onLogout}>退出</button>
        </footer>
      </aside>
      {active ? <Chat key={active.id} api={api} conversation={active} timeline={timelines[active.id] ?? emptyTimeline}
        userID={session.userID} status={status} epoch={epoch} dispatch={dispatch} onBack={() => setSelected(null)} />
        : <section className="empty-workspace">
          <span className="brand-mark" aria-hidden="true" />
          <h2>选择会话</h2>
          <p>也可以输入对方的用户名，发起单聊。</p>
        </section>}
    </main>
  );
}
