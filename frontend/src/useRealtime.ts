import { useEffect, useEffectEvent, useState } from "react";
import type { ChatAPI, Message, RedPacketStatus } from "./api";

export const statusLabels = { connecting: "正在连接", online: "已连接", offline: "连接已断开" };

type Frame = { type: "presence.updated" } | { type: "message.created"; data: Message } |
  { type: "conversation.read"; data: { conversation_id: string; seq: string } } |
  { type: "conversation.updated"; data: { conversation_id: string } } |
  { type: "red_packet.updated"; data: { red_packet_id: string; status: RedPacketStatus } };

export function useRealtime(api: ChatAPI, onMessage: (message: Message) => void, onConversationUpdated: (id: string) => void,
  onPacketUpdated: (id: string, status: RedPacketStatus) => void, onPresence: () => void, onRead: (id: string, seq: string) => void) {
  const [status, setStatus] = useState<"connecting" | "online" | "offline">("connecting");
  const [epoch, setEpoch] = useState(0);
  const receive = useEffectEvent(onMessage);
  const refresh = useEffectEvent(onConversationUpdated);
  const receiveRead = useEffectEvent(onRead);
  const refreshPresence = useEffectEvent(onPresence);
  const refreshPacket = useEffectEvent(onPacketUpdated);

  useEffect(() => {
    const controller = new AbortController();
    let socket: WebSocket | undefined;
    let timer: number | undefined;
    let retryDelay = 1000;
    let requestingTicket = false;

    function reconnect() {
      if (controller.signal.aborted) return;
      setStatus("offline");
      if (navigator.onLine) {
        timer = window.setTimeout(() => void connect(), retryDelay / 2 + Math.random() * retryDelay / 2);
        retryDelay = Math.min(retryDelay * 2, 10000);
      }
    }

    async function connect() {
      window.clearTimeout(timer);
      if (socket || requestingTicket || !navigator.onLine || controller.signal.aborted) return;
      requestingTicket = true;
      setStatus("connecting");
      try {
        // 每次握手都领取新票据，关闭当前页面时取消尚未完成的请求。
        const { ticket } = await api.ticket(controller.signal);
        if (controller.signal.aborted || !navigator.onLine) return;
        const url = new URL("/ws", window.location.href);
        url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
        url.searchParams.set("ticket", ticket);
        socket = new WebSocket(url);
        socket.onopen = () => {
          retryDelay = 1000;
          setStatus("online");
          setEpoch((value) => value + 1);
        };
        socket.onmessage = (event: MessageEvent<string>) => {
          const frame = JSON.parse(event.data) as Frame;
          if (frame.type === "presence.updated") refreshPresence();
          if (frame.type === "message.created") receive(frame.data);
          if (frame.type === "red_packet.updated") refreshPacket(frame.data.red_packet_id, frame.data.status);
          if (frame.type === "conversation.read") receiveRead(frame.data.conversation_id, frame.data.seq);
          if (frame.type === "conversation.updated") refresh(frame.data.conversation_id);
        };
        socket.onclose = () => {
          socket = undefined;
          reconnect();
        };
      } catch {
        reconnect();
      } finally {
        requestingTicket = false;
      }
    }

    function offline() {
      window.clearTimeout(timer);
      socket?.close();
      setStatus("offline");
    }

    if (navigator.onLine) void connect();
    else setStatus("offline");
    const online = () => void connect();
    window.addEventListener("online", online);
    window.addEventListener("offline", offline);
    return () => {
      controller.abort();
      window.clearTimeout(timer);
      window.removeEventListener("online", online);
      window.removeEventListener("offline", offline);
      if (socket) {
        socket.onopen = socket.onmessage = socket.onclose = null;
        socket.close();
      }
    };
  }, [api]);

  return { status, epoch };
}
