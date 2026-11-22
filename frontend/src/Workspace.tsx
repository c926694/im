import { useCallback, useEffect, useMemo, useReducer, useState, useRef } from "react";
import { chatAPI, conversationTitle } from "./api";
import type { Conversation, RedPacketStatus, Session } from "./api";
import { Chat } from "./Chat";
import { Points } from "./RedPackets";
import { Friends } from "./Friends";
import { GroupCreate, GroupPanel } from "./Groups";
import { emptyTimeline, timelineKey, timelinesReducer } from "./timeline";
import { statusLabels, useRealtime } from "./useRealtime";
import { Avatar, Icon } from "./visuals";

const activityTime = new Intl.DateTimeFormat("zh-CN", {
  month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit",
});

const navigation = [
  { view: "chats", icon: "chat", label: "会话" },
  { view: "contacts", icon: "contacts", label: "通讯录" },
  { view: "account", icon: "account", label: "我的" },
] as const;

export function Workspace({ session, onLogout, onExpired }: {
  session: Session;
  onLogout: () => void;
  onExpired: () => void;
}) {
  const api = useMemo(() => chatAPI(session, onExpired), [session, onExpired]);
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [loadingList, setLoadingList] = useState(true);
  const [listError, setListError] = useState("");
  const [view, setView] = useState<"chats" | "contacts" | "account">("chats");
  const [contactsView, setContactsView] = useState<"friends" | "requests">("friends");
  const [search, setSearch] = useState("");
  const [showCreateMenu, setShowCreateMenu] = useState(false);
  const [groupDialog, setGroupDialog] = useState<string | null>(null);
  const [groupVersions, setGroupVersions] = useState<Record<string, number>>({});
  const [packetUpdate, setPacketUpdate] = useState<{ id: string }>();
  const [packetStatuses, setPacketStatuses] = useState<Record<string, RedPacketStatus>>({});
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
      const keys = incoming.map(timelineKey);
      dispatch({ source: "keep", keys });
      setGroupDialog((current) => current === "create" || (current && keys.includes(current)) ? current : null);
      setSelected((current) => incoming.some((item) => item.id === current) ? current : null);
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

  function refreshGroup(id: string) {
    setGroupVersions((versions) => ({ ...versions, [id]: (versions[id] ?? 0) + 1 }));
    void loadConversations();
  }

  const { status, epoch } = useRealtime(api, (message) => {
    const conversation = conversations.find((item) => item.id === message.conversation_id);
    if (conversation) {
      dispatch({ key: timelineKey(conversation), joinedSeq: BigInt(conversation.joined_seq), source: "live", messages: [message] });
    }
    scheduleConversationRefresh();
  }, refreshGroup, (id, status) => {
    setPacketUpdate({ id });
    setPacketStatuses((current) => ({ ...current, [id]: status }));
  });

  useEffect(() => {
    if (epoch <= 1) return;
    const controller = new AbortController();
    // 首次连接保留初始列表请求，后续重连时刷新断线期间新建的会话。
    void loadConversations(controller.signal);
    return () => controller.abort();
  }, [epoch, loadConversations]);

  useEffect(() => {
    const controller = new AbortController();
    void loadConversations(controller.signal);
    return () => controller.abort();
  }, [loadConversations]);

  async function openDirect(username: string) {
    const result = await api.direct(username);
    // 从会话列表取得昵称和发送权限后，再打开目标聊天。
    await openConversation(result.id);
  }

  async function openConversation(id: string) {
    await loadConversations();
    setSearch("");
    setSelected(id);
    setView("chats");
  }

  const ordered = conversations.map((conversation) => {
    const latest = timelines[timelineKey(conversation)]?.messages.at(-1);
    if (!latest || BigInt(latest.seq) <= BigInt(conversation.last_seq)) return conversation;
    return { ...conversation, last_seq: latest.seq, last_activity_at: latest.created_at, last_message: latest.content };
  }).sort((a, b) => Date.parse(b.last_activity_at) - Date.parse(a.last_activity_at));
  const active = view === "chats" ? ordered.find((conversation) => conversation.id === selected) : undefined;
  const query = search.trim().toLowerCase();
  const matches = ordered.filter((conversation) =>
    conversationTitle(conversation).toLowerCase().includes(query) || conversation.last_message.toLowerCase().includes(query));
  const unreadTotal = conversations.reduce((sum, conversation) => sum + conversation.unread_count, 0);

  return (
    <main className="workspace" data-thread-open={Boolean(active)}>
      <aside className="sidebar" aria-label="聊天导航">
        <nav className="workspace-rail" aria-label="聊天功能">
          <div className="rail-profile"><Avatar name={session.username} /></div>
          <div className="rail-items">
            {navigation.map(({ view: target, icon, label }) => <button key={target} type="button" className="rail-button"
              aria-label={label} title={label} aria-pressed={view === target} onClick={() => {
                setView(target);
                setShowCreateMenu(false);
                if (target === "chats") void loadConversations();
              }}>
              <Icon name={icon} /><span className="rail-label">{label}</span>
              {target === "chats" && unreadTotal > 0 && <span className="rail-badge" aria-hidden="true">{unreadTotal > 99 ? "99+" : unreadTotal}</span>}
            </button>)}
          </div>
          <span className="connection rail-connection" data-status={status} role="status" title={statusLabels[status]}>
            <span className="sr-only">{statusLabels[status]}</span>
          </span>
        </nav>
        <div className="sidebar-pane">
          <header className="pane-header">
            {view === "chats" ? <>
              <h1 id="conversations-title" className="sr-only">会话</h1>
              <label className="conversation-search">
                <Icon name="search" /><span className="sr-only">搜索会话</span>
                <input type="search" value={search} onChange={(event) => setSearch(event.target.value)} placeholder="搜索" />
              </label>
              <div className="create-menu" onKeyDown={(event) => {
                if (event.key === "Escape") {
                  setShowCreateMenu(false);
                  event.currentTarget.querySelector("button")?.focus();
                }
              }} onBlur={(event) => {
                if (!event.currentTarget.contains(event.relatedTarget)) setShowCreateMenu(false);
              }}>
                <button className="icon-button" type="button" aria-label="新建" title="新建" aria-expanded={showCreateMenu}
                  onClick={() => setShowCreateMenu(!showCreateMenu)}><Icon name="add" /></button>
                {showCreateMenu && <div className="create-options">
                  <button type="button" onClick={() => {
                    setShowCreateMenu(false);
                    setGroupDialog("create");
                  }}><Icon name="group" />创建群聊</button>
                  <button type="button" onClick={() => {
                    setShowCreateMenu(false);
                    setContactsView("friends");
                    setView("contacts");
                  }}><Icon name="contacts" />查找好友</button>
                </div>}
              </div>
            </> : <h1>{view === "contacts" ? "通讯录" : "我的"}</h1>}
            <span className="connection pane-connection" data-status={status} role="status">{statusLabels[status]}</span>
          </header>
          {view === "contacts" && <section className="contacts" aria-label="通讯录">
            <nav className="contacts-nav" aria-label="通讯录功能">
              <button type="button" aria-pressed={contactsView === "friends"} onClick={() => setContactsView("friends")}>好友</button>
              <button type="button" aria-pressed={contactsView === "requests"} onClick={() => setContactsView("requests")}>好友申请</button>
            </nav>
            <Friends key={contactsView} api={api} userID={session.userID} view={contactsView}
              onOpen={openDirect} onChanged={() => void loadConversations()} />
          </section>}
          {view === "account" && <section className="conversations" aria-labelledby="account-title">
            <div className="account">
              <Avatar name={session.username} />
              <div className="account-name"><h2 id="account-title">我的账号</h2><strong>{session.username}</strong></div>
              <button type="button" onClick={onLogout}>退出登录</button>
            </div>
            <Points api={api} epoch={epoch} update={packetUpdate} />
          </section>}
          {view === "chats" && <section className="conversations conversation-list" aria-labelledby="conversations-title">
            {listError && <div className="feedback" role="alert">
              <p>{listError}</p><button type="button" onClick={() => void loadConversations()}>刷新会话</button>
            </div>}
            {loadingList && <p className="list-note" role="status">正在加载会话…</p>}
            {!loadingList && !listError && ordered.length === 0 && <p className="list-note">暂无会话</p>}
            {!loadingList && !listError && ordered.length > 0 && matches.length === 0 && <p className="list-note">没有匹配的会话</p>}
            <ul>
              {matches.map((conversation) => (
                <li key={conversation.id}>
                  <button type="button" className="conversation" aria-current={selected === conversation.id ? "true" : undefined}
                    onClick={() => {
                      setSelected(conversation.id);
                      void loadConversations();
                    }}>
                    {conversation.kind === "group" ? <span className="avatar group-avatar"><Icon name="group" /></span>
                      : <Avatar name={conversation.peer_nickname} />}
                    <span className="conversation-details">
                      <strong>{conversationTitle(conversation)}</strong>
                      <time dateTime={conversation.last_activity_at}>{activityTime.format(new Date(conversation.last_activity_at))}</time>
                      <span className="conversation-preview">{conversation.last_message === "[红包]"
                        ? <span className="packet-preview"><Icon name="envelope" />积分红包</span>
                        : conversation.last_message || "暂无消息"}</span>
                      {conversation.unread_count > 0 && <span className="unread-badge">
                        <span className="sr-only">未读消息：</span>{conversation.unread_count}
                      </span>}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          </section>}
        </div>
      </aside>
      {active ? <Chat key={timelineKey(active)} api={api} conversation={active} timeline={timelines[timelineKey(active)] ?? emptyTimeline}
        userID={session.userID} username={session.username} status={status} epoch={epoch} dispatch={dispatch} onBack={() => setSelected(null)}
        onFriendshipChanged={() => void loadConversations()} onRead={refreshConversationsAfterRead}
        onGroupDetails={() => setGroupDialog(timelineKey(active))} packetUpdate={packetUpdate} packetStatuses={packetStatuses} />
        : <section className="empty-workspace">
          <span className="empty-icon"><Icon name={view === "chats" ? "chat" : view} /></span>
          {view === "chats" ? <><h2>选择会话</h2><p>可以从通讯录发起单聊，或创建群聊。</p></>
            : view === "contacts" ? <><h2>通讯录</h2><p>查找好友、处理申请，或从好友列表发起单聊。</p></>
              : <><h2>我的账号</h2><p>在左侧查看当前登录账号。</p></>}
        </section>}
      {groupDialog === "create" && <GroupCreate api={api} onClose={() => setGroupDialog(null)} onCreated={async (id) => {
        await openConversation(id);
        setGroupDialog(null);
      }} />}
      {active?.kind === "group" && groupDialog === timelineKey(active) && <GroupPanel api={api} id={active.id} userID={session.userID}
        revision={groupVersions[active.id] ?? 0} epoch={epoch} onClose={() => setGroupDialog(null)}
        onChanged={() => refreshGroup(active.id)} onLeft={() => {
          setSelected(null);
          setGroupDialog(null);
          void loadConversations();
        }} />}
    </main>
  );
}
