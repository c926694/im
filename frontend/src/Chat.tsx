import { useEffect, useEffectEvent, useLayoutEffect, useRef, useState } from "react";
import type { Dispatch, FormEvent } from "react";
import { APIError, conversationTitle, errorCode, pageSize } from "./api";
import type { ChatAPI, Conversation, Draft, RedPacketStatus } from "./api";
import { timelineKey } from "./timeline";
import type { Timeline, TimelineAction } from "./timeline";
import { statusLabels } from "./useRealtime";
import { PacketDetails, SendPacket } from "./RedPackets";
import { ChatImage } from "./ChatImage";
import { Avatar, Icon } from "./visuals";

const messageTime = new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit" });
const messageDate = new Intl.DateTimeFormat("zh-CN", { year: "numeric", month: "long", day: "numeric" });

export function Chat({ api, conversation, timeline, userID, username, status, epoch, dispatch, onBack, onFriendshipChanged, onRead, onGroupDetails, ownAvatarURL, packetUpdate, packetStatuses }: {
  api: ChatAPI;
  conversation: Conversation;
  timeline: Timeline;
  userID: string;
  username: string;
  status: "connecting" | "online" | "offline";
  epoch: number;
  dispatch: Dispatch<TimelineAction>;
  onBack: () => void;
  onFriendshipChanged: () => void;
  onRead: (id: string, seq: string) => void;
  ownAvatarURL?: string;
  onGroupDetails: () => void;
  packetUpdate?: { id: string };
  packetStatuses: Record<string, RedPacketStatus>;
}) {
  const { id } = conversation;
  const key = timelineKey(conversation);
  const joinedSeq = BigInt(conversation.joined_seq);
  const title = conversationTitle(conversation);
  const [historyError, setHistoryError] = useState("");
  const [historyAttempt, setHistoryAttempt] = useState(0);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [syncError, setSyncError] = useState("");
  const [syncAttempt, setSyncAttempt] = useState(0);
  const [checkedLastSeq, setCheckedLastSeq] = useState(0n);
  const [syncedEpoch, setSyncedEpoch] = useState(0);
  const [content, setContent] = useState("");
  const [packetDialog, setPacketDialog] = useState<{ id: string } | "send" | null>(null);
  const [imageDraft, setImageDraft] = useState<{ file: File; preview: string }>();
  const imageAttempt = useRef<{ file: File; content: string; clientID: string } | null>(null);
  const imageInput = useRef<HTMLInputElement>(null);
  const [sending, setSending] = useState(false);
  const [sendError, setSendError] = useState("");
  const [atBottom, setAtBottom] = useState(true);
  const [pageVisible, setPageVisible] = useState(!document.hidden);
  const [readError, setReadError] = useState("");
  const [readAttempt, setReadAttempt] = useState(0);
  const [packetArrivalID, setPacketArrivalID] = useState<string>();
  const draft = useRef<Draft | null>(null);
  const packetArrivalTimer = useRef<number | undefined>(undefined);
  const scroll = useRef<HTMLDivElement>(null);
  const stickToBottom = useRef(true);
  const prependAnchor = useRef<{ element: HTMLElement; top: number } | null>(null);
  const initialized = timeline.oldestSeq !== null;
  const needsSync = initialized && ((status === "online" && epoch !== syncedEpoch) ||
    BigInt(conversation.last_seq) > timeline.cursor || checkedLastSeq > timeline.cursor);
  const refreshMembership = useEffectEvent(onFriendshipChanged);
  const readCursor = useEffectEvent(() => timeline.cursor);

  useEffect(() => () => {
    if (packetArrivalTimer.current !== undefined) window.clearTimeout(packetArrivalTimer.current);
  }, []);

  useEffect(() => {
    if (initialized) return;
    const controller = new AbortController();
    setHistoryError("");
    api.messages(id, null, controller.signal).then((messages) => {
      dispatch({ key, joinedSeq, source: "history", messages });
    }).catch((error: Error) => {
      if (!controller.signal.aborted) setHistoryError(error.message);
    });
    return () => controller.abort();
  }, [api, id, key, joinedSeq, initialized, historyAttempt, dispatch]);

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
        if (messages.length > 0) dispatch({ key, joinedSeq, source: "sync", messages });
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
  }, [api, id, key, joinedSeq, epoch, needsSync, syncAttempt, dispatch]);

  useEffect(() => {
    if (!initialized || !pageVisible) return;
    const controller = new AbortController();
    let timer: number;
    function schedule() {
      timer = window.setTimeout(() => void check(), 25_000 + Math.random() * 10_000);
    }
    async function check() {
      try {
        // 即使最后一条推送遗漏，也能按连续游标发现并补齐。
        const { last_seq } = await api.syncState(id, controller.signal);
        if (!controller.signal.aborted) setCheckedLastSeq(BigInt(last_seq));
      } catch (error) {
        if (!controller.signal.aborted && error instanceof APIError && error.code === errorCode.notMember) {
          refreshMembership();
        }
      } finally {
        if (!controller.signal.aborted) schedule();
      }
    }
    schedule();
    return () => {
      controller.abort();
      window.clearTimeout(timer);
    };
  }, [api, id, initialized, pageVisible]);

  useEffect(() => {
    const onVisibilityChange = () => setPageVisible(!document.hidden);
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => document.removeEventListener("visibilitychange", onVisibilityChange);
  }, []);

  useEffect(() => {
    // 只上报当前页面已显示的连续消息，保留后台和查看历史时的未读进度。
    if (!initialized || !pageVisible || !atBottom || timeline.cursor <= BigInt(conversation.last_read_seq)) return;
    const controller = new AbortController();
    api.markRead(id, timeline.cursor.toString(), controller.signal).then(() => {
      if (controller.signal.aborted) return;
      setReadError("");
      onRead(id, timeline.cursor.toString());
    }).catch((error: Error) => {
      if (!controller.signal.aborted) setReadError(error.message);
    });
    return () => controller.abort();
  }, [api, id, initialized, timeline.cursor, atBottom, pageVisible, conversation.last_read_seq, readAttempt, onRead]);

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
      dispatch({ key, joinedSeq, source: "history", messages });
    } catch (error) {
      setHistoryError((error as Error).message);
    } finally {
      setLoadingOlder(false);
    }
  }

  useEffect(() => () => {
    if (imageDraft) URL.revokeObjectURL(imageDraft.preview);
  }, [imageDraft]);

  function selectImage(file: File) {
    if (sending) return;
    if (file.size > 5 * 1024 * 1024) {
      setSendError("图片不能超过 5 MiB。");
      return;
    }
    setSendError("");
    setImageDraft({ file, preview: URL.createObjectURL(file) });
  }

  async function send(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (sending || (!imageDraft && !content.trim())) return;
    // HTTP 请求失败时保留客户端 UUID，重试可以取回已保存的消息。
    if (!imageDraft && draft.current?.content !== content) {
      draft.current = { client_msg_id: crypto.randomUUID(), content };
    }
    setSending(true);
    setSendError("");
    try {
      if (imageDraft && (imageAttempt.current?.file !== imageDraft.file || imageAttempt.current.content !== content)) {
        imageAttempt.current = { file: imageDraft.file, content, clientID: crypto.randomUUID() };
      }
      const message = imageDraft ? await api.sendImage(id, imageDraft.file, imageAttempt.current!.clientID, content) : await api.send(id, draft.current!);
      stickToBottom.current = true;
      dispatch({ key, joinedSeq, source: "live", messages: [message] });
      setImageDraft(undefined);
      imageAttempt.current = null;
      draft.current = null;
      setContent("");
    } catch (error) {
      setSendError((error as Error).message);
      if (error instanceof APIError && (error.code === errorCode.notFriends || error.code === errorCode.notMember)) onFriendshipChanged();
    } finally {
      setSending(false);
    }
  }

  const visible = initialized ? timeline.messages.filter((message) =>
    BigInt(message.seq) >= BigInt(timeline.oldestSeq!) && BigInt(message.seq) <= timeline.cursor) : [];

  return (
    <section className="chat" aria-labelledby="chat-title">
      <header className="chat-header">
        <button type="button" className="icon-button mobile-back" aria-label="返回会话" onClick={onBack}><Icon name="back" /></button>
        <h2 id="chat-title">{title}</h2>
        {conversation.kind === "direct" && conversation.peer_online !== undefined &&
          <span className="presence" data-online={conversation.peer_online}>{conversation.peer_online ? "在线" : "离线"}</span>}
        <span className="connection chat-connection" data-status={status}>{statusLabels[status]}</span>
        {conversation.kind === "group" && <button type="button" className="icon-button" aria-label="群详情" title="群详情"
          onClick={onGroupDetails}><Icon name="group" /></button>}
      </header>
      <div className="chat-scroll" ref={scroll} role="region" aria-label="聊天记录" tabIndex={0} onScroll={(event) => {
        const viewport = event.currentTarget;
        const atBottom = viewport.scrollHeight - viewport.clientHeight - viewport.scrollTop <= 2;
        stickToBottom.current = atBottom;
        setAtBottom(atBottom);
        if (viewport.scrollTop < 48 && !atBottom && !historyError) void loadOlder();
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
            const sender = message.sender_nickname || (own ? username : title);
            const time = new Date(message.created_at);
            const previous = visible[index - 1];
            const previousTime = previous ? new Date(previous.created_at) : null;
            const dayChanged = !previousTime || time.toDateString() !== previousTime.toDateString();
            const showTimestamp = dayChanged || time.getTime() - previousTime.getTime() >= 5 * 60_000;
            return <div key={message.id}>
              {showTimestamp && <time className="day-label" dateTime={message.created_at}>
                {dayChanged && `${messageDate.format(time)} `}{messageTime.format(time)}
              </time>}
              {message.kind === "red_packet_claim" ? <p className="message-system" role="note">
                {own ? "你领取了红包" : conversation.kind === "direct" ? `${sender} 领取了你的红包` : `${sender} 领取了红包`}
              </p> : <article className="message" data-own={own} data-packet-arrival={packetArrivalID === message.id}>
                <Avatar name={sender} src={own ? ownAvatarURL : conversation.kind === "direct" ? conversation.peer_avatar_url : message.sender_avatar_url} />
                <div className="message-body">
                  <span className={own ? "sr-only" : "message-sender"}>{own ? "我" : sender}</span>
                  {message.kind === "image" ? <div className="chat-image-message">
                    <ChatImage api={api} id={message.id} />
                    {message.content !== "[图片]" && <p>{message.content}</p>}
                  </div> : message.kind === "red_packet" || message.red_packet_id || message.content === "[红包]" ?
                    message.red_packet_id ? <button type="button" className={`packet-card${(packetStatuses[message.red_packet_id] ?? message.red_packet_status) === "claimed" ? " is-claimed" : ""}`}
                      data-status={packetStatuses[message.red_packet_id] ?? message.red_packet_status ?? "open"}
                      aria-label={own ? "查看红包领取情况" : "打开并领取积分红包"}
                      onClick={() => setPacketDialog({ id: message.red_packet_id! })}>
                      <span className="packet-card-main">
                        <span className="packet-card-icon"><Icon name="envelope" /></span>
                        <span className="packet-card-copy"><strong>积分红包</strong><span>{(packetStatuses[message.red_packet_id] ?? message.red_packet_status) === "claimed"
                          ? "红包已领完" : own ? "查看领取情况" : "点击领取积分"}</span></span>
                      </span>
                      <span className="packet-card-footer"><span>回声积分</span><span className="packet-card-arrow" aria-hidden="true">›</span></span>
                    </button> : <div className="packet-card packet-card-unavailable" role="status">
                      <span className="packet-card-main">
                        <span className="packet-card-icon"><Icon name="envelope" /></span>
                      <span className="packet-card-copy"><strong>历史红包消息</strong><span>这条消息没有可打开的红包记录</span></span>
                      </span>
                      <span className="packet-card-footer"><span>无法领取</span></span>
                    </div> : <p title={messageTime.format(time)}>{message.content}</p>}
                </div>
              </article>}
            </div>;
          })}
        </div>
      </div>
      <div>
        {syncError && <div className="feedback sync-feedback" role="alert">
          <p>消息同步失败。{syncError}</p><button type="button" disabled={status !== "online"} onClick={() => setSyncAttempt((value) => value + 1)}>重试同步</button>
        </div>}
        {readError && <div className="feedback sync-feedback" role="alert">
          <p>已读状态更新失败。{readError}</p><button type="button" onClick={() => setReadAttempt((value) => value + 1)}>重试更新</button>
        </div>}
      </div>
      {conversation.kind === "group" || conversation.is_friend ? <form className="composer" onSubmit={send}>
        <label className="sr-only" htmlFor="message-content">消息</label>
        <textarea id="message-content" rows={3} value={content} readOnly={sending} aria-describedby="composer-shortcuts"
          onPaste={(event) => {
            const picture = Array.from(event.clipboardData.files).find((file) => file.type.startsWith("image/"));
            if (!picture) return;
            event.preventDefault();
            selectImage(picture);
          }} placeholder="输入消息" onChange={(event) => setContent(event.target.value)} onKeyDown={(event) => {
            // 输入法确认候选词时，Enter 交给输入法处理。
            if (event.key !== "Enter" || event.shiftKey || event.nativeEvent.isComposing || event.keyCode === 229) return;
            event.preventDefault();
            if (!event.repeat) event.currentTarget.form?.requestSubmit();
          }} />
        {imageDraft && <div className="composer-image">
          <img src={imageDraft.preview} alt="待发送图片" />
          <button type="button" disabled={sending} onClick={() => setImageDraft(undefined)}>移除图片</button>
        </div>}
        <input type="file" ref={imageInput} hidden accept="image/jpeg,image/png,image/webp" onChange={(event) => {
          const file = event.currentTarget.files?.[0];
          event.currentTarget.value = "";
          if (file) selectImage(file);
        }} />
        <div className="composer-toolbar">
          <p id="composer-shortcuts" className="hint"><Icon name="keyboard" /><span>Enter 发送 · Shift + Enter 换行</span></p>
          <button type="button" disabled={sending} onClick={() => imageInput.current?.click()}>{imageDraft ? "替换图片" : "图片"}</button>
          <button type="button" className="packet-send" onClick={() => setPacketDialog("send")}>发红包</button>
          <button type="submit" className="primary" disabled={sending || (!imageDraft && !content.trim())}>{sending ? "正在发送…" : "发送"}</button>
        </div>
        {sendError && <p className="error" role="alert">{sendError}</p>}
      </form> : <p className="composer composer-locked" role="status">聊天记录仍可查看。成为好友后可以发送新消息。</p>}
      {packetDialog === "send" && <SendPacket api={api} conversation={conversation} onClose={() => setPacketDialog(null)} onSent={(message) => {
        stickToBottom.current = true;
        dispatch({ key, joinedSeq, source: "live", messages: [message] });
        setPacketArrivalID(message.id);
        if (packetArrivalTimer.current !== undefined) window.clearTimeout(packetArrivalTimer.current);
        packetArrivalTimer.current = window.setTimeout(() => setPacketArrivalID(undefined), 760);
        setPacketDialog(null);
      }} />}
      {packetDialog && packetDialog !== "send" && <PacketDetails api={api} id={packetDialog.id} epoch={epoch}
        update={packetUpdate?.id === packetDialog.id ? packetUpdate : undefined} onClose={() => setPacketDialog(null)} />}
    </section>
  );
}
