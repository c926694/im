import { useEffect, useEffectEvent, useLayoutEffect, useRef, useState } from "react";
import type { Dispatch, FormEvent } from "react";
import { APIError, pageSize } from "./api";
import type { ChatAPI, Conversation, Draft } from "./api";
import type { Timeline, TimelineAction } from "./timeline";
import { statusLabels } from "./useRealtime";

const messageTime = new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit" });
const messageDate = new Intl.DateTimeFormat("zh-CN", { year: "numeric", month: "long", day: "numeric" });

export function Chat({ api, conversation, timeline, userID, status, epoch, dispatch, onBack, onFriendshipChanged }: {
  api: ChatAPI;
  conversation: Conversation;
  timeline: Timeline;
  userID: string;
  status: "connecting" | "online" | "offline";
  epoch: number;
  dispatch: Dispatch<TimelineAction>;
  onBack: () => void;
  onFriendshipChanged: () => void;
}) {
  const { id, peer_nickname: peerNickname } = conversation;
  const [historyError, setHistoryError] = useState("");
  const [historyAttempt, setHistoryAttempt] = useState(0);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [syncError, setSyncError] = useState("");
  const [syncAttempt, setSyncAttempt] = useState(0);
  const [syncedEpoch, setSyncedEpoch] = useState(0);
  const [content, setContent] = useState("");
  const [sending, setSending] = useState(false);
  const [sendError, setSendError] = useState("");
  const draft = useRef<Draft | null>(null);
  const scroll = useRef<HTMLDivElement>(null);
  const stickToBottom = useRef(true);
  const prependAnchor = useRef<{ element: HTMLElement; top: number } | null>(null);
  const initialized = timeline.oldestSeq !== null;
  const needsSync = initialized && status === "online" &&
    (epoch !== syncedEpoch || BigInt(conversation.last_seq) > timeline.cursor);
  const readCursor = useEffectEvent(() => timeline.cursor);

  useEffect(() => {
    if (initialized) return;
    const controller = new AbortController();
    setHistoryError("");
    api.messages(id, null, controller.signal).then((messages) => {
      dispatch({ id, source: "history", messages });
    }).catch((error: Error) => {
      if (!controller.signal.aborted) setHistoryError(error.message);
    });
    return () => controller.abort();
  }, [api, id, initialized, historyAttempt, dispatch]);

  useEffect(() => {
    if (!needsSync) return;
    const controller = new AbortController();
    const cursor = readCursor();
    setSyncError("");

    async function sync(from: bigint) {
      // 使用本页面连续处理到的序号。补拉期间收到的推送也进入同一时间线。
      let after = from.toString();
      while (!controller.signal.aborted) {
        const messages = await api.messages(id, { after_seq: after }, controller.signal);
        if (controller.signal.aborted) return;
        if (messages.length > 0) dispatch({ id, source: "sync", messages });
        if (messages.length < pageSize) {
          setSyncedEpoch(epoch);
          return;
        }
        after = messages[messages.length - 1].seq;
      }
    }

    sync(cursor).catch((error: Error) => {
      if (!controller.signal.aborted) setSyncError(error.message);
    });
    return () => controller.abort();
  }, [api, id, epoch, needsSync, syncAttempt, dispatch]);

  useLayoutEffect(() => {
    const viewport = scroll.current;
    if (!viewport) return;
    const anchor = prependAnchor.current;
    if (anchor) {
      // 向上翻页后保持刚才可见的那条消息在原来位置。
      prependAnchor.current = null;
      viewport.scrollTop += anchor.element.getBoundingClientRect().top - anchor.top;
    } else if (stickToBottom.current) {
      viewport.scrollTop = viewport.scrollHeight;
    }
  }, [timeline]);

  async function loadOlder() {
    if (!timeline.oldestSeq || !timeline.hasOlder || loadingOlder) return;
    setLoadingOlder(true);
    setHistoryError("");
    try {
      const messages = await api.messages(id, { before_seq: timeline.oldestSeq });
      if (messages.length > 0 && scroll.current) {
        const viewportTop = scroll.current.getBoundingClientRect().top;
        const firstVisible = Array.from(scroll.current.querySelectorAll<HTMLElement>(".message"))
          .find((element) => element.getBoundingClientRect().bottom > viewportTop);
        if (firstVisible) prependAnchor.current = { element: firstVisible, top: firstVisible.getBoundingClientRect().top };
      }
      dispatch({ id, source: "history", messages });
    } catch (error) {
      setHistoryError((error as Error).message);
    } finally {
      setLoadingOlder(false);
    }
  }

  async function send(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (sending || !content.trim()) return;
    // HTTP 请求失败时保留客户端 UUID，重试可以取回已保存的消息。
    if (draft.current?.content !== content) {
      draft.current = { client_msg_id: crypto.randomUUID(), content };
    }
    setSending(true);
    setSendError("");
    try {
      const message = await api.send(id, draft.current);
      stickToBottom.current = true;
      dispatch({ id, source: "live", messages: [message] });
      draft.current = null;
      setContent("");
    } catch (error) {
      setSendError((error as Error).message);
      if (error instanceof APIError && error.code === "not_friends") onFriendshipChanged();
    } finally {
      setSending(false);
    }
  }

  const visible = initialized ? timeline.messages.filter((message) =>
    BigInt(message.seq) >= BigInt(timeline.oldestSeq!) && BigInt(message.seq) <= timeline.cursor) : [];

  return (
    <section className="chat" aria-labelledby="chat-title">
      <header className="chat-header">
        <button type="button" className="mobile-back" onClick={onBack}>返回会话</button>
        <span className="avatar" aria-hidden="true">{Array.from(peerNickname)[0]}</span>
        <div>
          <h2 id="chat-title">{peerNickname}</h2>
          <span className="connection mobile-connection" data-status={status} role="status">{statusLabels[status]}</span>
        </div>
      </header>
      <div className="chat-scroll" ref={scroll} role="region" aria-label="聊天记录" tabIndex={0} onScroll={(event) => {
        const viewport = event.currentTarget;
        stickToBottom.current = viewport.scrollHeight - viewport.clientHeight - viewport.scrollTop < 72;
        if (viewport.scrollTop < 48 && !stickToBottom.current && !historyError) void loadOlder();
      }}>
        {!initialized && !historyError && <p className="chat-note" role="status">正在加载消息…</p>}
        {historyError && <div className="feedback chat-note" role="alert">
          <p>{historyError}</p><button type="button" onClick={() => initialized ? void loadOlder() : setHistoryAttempt((value) => value + 1)}>重试加载历史</button>
        </div>}
        {timeline.hasOlder && !historyError && <div className="history-control">
          <button type="button" disabled={loadingOlder} onClick={() => void loadOlder()}>
            {loadingOlder ? "正在加载历史…" : "加载更早消息"}
          </button>
        </div>}
        {initialized && visible.length === 0 && <p className="chat-note">还没有消息。</p>}
        <div role="log" aria-label="消息列表" aria-live="polite">
          {visible.map((message, index) => {
            const own = message.sender_id === userID;
            const time = new Date(message.created_at);
            const day = messageDate.format(time);
            const previous = visible[index - 1];
            return <div key={message.id}>
              {(!previous || day !== messageDate.format(new Date(previous.created_at))) && <p className="day-label">{day}</p>}
              <article className="message" data-own={own}>
                <span className="sr-only">{own ? "我" : peerNickname}</span>
                <p>{message.content}</p>
                <time dateTime={message.created_at}>{messageTime.format(time)}</time>
              </article>
            </div>;
          })}
        </div>
      </div>
      {syncError && <div className="feedback sync-feedback" role="alert">
        <p>消息同步失败。{syncError}</p><button type="button" disabled={status !== "online"} onClick={() => setSyncAttempt((value) => value + 1)}>重试同步</button>
      </div>}
      {conversation.is_friend ? <form className="composer" onSubmit={send}>
        <label htmlFor="message-content">消息</label>
        <div className="composer-fields">
          <textarea id="message-content" rows={2} value={content} disabled={sending} onChange={(event) => setContent(event.target.value)} />
          <button type="submit" className="primary" disabled={sending || !content.trim()}>{sending ? "正在发送…" : "发送"}</button>
        </div>
        {sendError && <p className="error" role="alert">{sendError}</p>}
      </form> : <p className="composer" role="status">聊天记录仍可查看。成为好友后可以发送新消息。</p>}
    </section>
  );
}
