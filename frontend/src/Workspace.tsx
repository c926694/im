import { useCallback, useEffect, useMemo, useReducer, useState, useRef } from "react";
import { chatAPI } from "./api";
import type { Conversation, Session } from "./api";
import { Chat } from "./Chat";
import { Friends } from "./Friends";
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
  const [view, setView] = useState<"chats" | "friends" | "requests">("chats");
  const [selected, setSelected] = useState<string | null>(null);
  const [timelines, dispatch] = useReducer(timelinesReducer, {});
  const conversationRefreshTimer = useRef<number | undefined>(undefined);
  const listRequest = useRef(0);
  const appliedListRequest = useRef(0);

  const loadConversations = useCallback(async (signal?: AbortSignal) => {
    const request = ++listRequest.current;
    try {
      const incoming = await api.conversations(signal);
      // 较早的列表响应可能包含相同消息序号和更早的已读进度。
      if (signal?.aborted || request < appliedListRequest.current) return;
      appliedListRequest.current = request;
      setConversations(incoming);
      setListError("");
    } catch (error) {
      if (!signal?.aborted && request >= appliedListRequest.current) setListError((error as Error).message);
    } finally {
      if (!signal?.aborted) setLoadingList(false);
    }
  }, [api]);
  const refreshConversationsAfterRead = useCallback(() => {
    void loadConversations();
  }, [loadConversations]);
  const scheduleConversationRefresh = useCallback(() => {
    if (conversationRefreshTimer.current !== undefined) return;
    conversationRefreshTimer.current = window.setTimeout(() => {
      conversationRefreshTimer.current = undefined;
      void loadConversations();
    }, 150);
  }, [loadConversations]);

  useEffect(() => () => window.clearTimeout(conversationRefreshTimer.current), []);

  const { status, epoch } = useRealtime(api, (message) => {
    dispatch({ id: message.conversation_id, source: "live", messages: [message] });
    scheduleConversationRefresh();
  });

  useEffect(() => {
    const controller = new AbortController();
    // 重连后还需要发现断线期间由对方新建的会话。
    void loadConversations(controller.signal);
    return () => controller.abort();
  }, [epoch, loadConversations]);

  async function openDirect(username: string) {
    const result = await api.direct(username);
    // 从会话列表取得昵称和发送权限后，再打开目标聊天。
    await loadConversations();
    setSelected(result.id);
    setView("chats");
  }

  const ordered = conversations.map((conversation) => {
    const latest = timelines[conversation.id]?.messages.at(-1);
    if (!latest || BigInt(latest.seq) <= BigInt(conversation.last_seq)) return conversation;
    return { ...conversation, last_seq: latest.seq, last_activity_at: latest.created_at };
  }).sort((a, b) => Date.parse(b.last_activity_at) - Date.parse(a.last_activity_at));
  const active = view === "chats" ? ordered.find((conversation) => conversation.id === selected) : undefined;

  return (
    <main className="workspace" data-thread-open={Boolean(active)}>
      <aside className="sidebar" aria-label="聊天导航">
        <header className="sidebar-header">
          <div className="wordmark"><span className="brand-mark" aria-hidden="true" /><h1>回声</h1></div>
          <span className="connection" data-status={status} role="status">{statusLabels[status]}</span>
        </header>
        <nav className="sidebar-nav" aria-label="聊天功能">
          <button type="button" aria-pressed={view === "chats"} onClick={() => {
            setView("chats");
            void loadConversations();
          }}>会话</button>
          <button type="button" aria-pressed={view === "friends"} onClick={() => setView("friends")}>好友</button>
          <button type="button" aria-pressed={view === "requests"} onClick={() => setView("requests")}>好友申请</button>
        </nav>
        {view !== "chats" ? <Friends key={view} api={api} userID={session.userID} view={view}
          onOpen={openDirect} onChanged={() => void loadConversations()} /> : <section className="conversations" aria-labelledby="conversations-title">
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
                  onClick={() => {
                    setSelected(conversation.id);
                    void loadConversations();
                  }}>
                  <span className="avatar" aria-hidden="true">{Array.from(conversation.peer_nickname)[0]}</span>
                  <span className="conversation-label">
                    <strong>{conversation.peer_nickname}</strong>
                    <time dateTime={conversation.last_activity_at}>{activityTime.format(new Date(conversation.last_activity_at))}</time>
                  </span>
                  {conversation.unread_count > 0 && <span className="unread-badge">
                    <span className="sr-only">未读消息：</span>{conversation.unread_count}
                  </span>}
                </button>
              </li>
            ))}
          </ul>
        </section>}
        <footer className="account">
          <span><span>我的用户名</span><strong>{session.username}</strong></span>
          <button type="button" onClick={onLogout}>退出</button>
        </footer>
      </aside>
      {active ? <Chat key={active.id} api={api} conversation={active} timeline={timelines[active.id] ?? emptyTimeline}
        userID={session.userID} status={status} epoch={epoch} dispatch={dispatch} onBack={() => setSelected(null)}
        onFriendshipChanged={() => void loadConversations()} onRead={refreshConversationsAfterRead} />
        : <section className="empty-workspace">
          <span className="brand-mark" aria-hidden="true" />
          <h2>选择会话</h2>
          <p>可以从好友列表发起单聊。</p>
        </section>}
    </main>
  );
}
