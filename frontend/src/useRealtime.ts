import { useEffect, useEffectEvent, useState } from "react";
import type { ChatAPI, Message } from "./api";

export const statusLabels = { connecting: "正在连接", online: "已连接", offline: "连接已断开" };

export function useRealtime(api: ChatAPI, onMessage: (message: Message) => void) {
  const [status, setStatus] = useState<"connecting" | "online" | "offline">("connecting");
  const [epoch, setEpoch] = useState(0);
  const receive = useEffectEvent(onMessage);

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
        timer = window.setTimeout(() => void connect(), retryDelay);
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
          const frame = JSON.parse(event.data) as { type: string; data: Message };
          if (frame.type === "message.created") receive(frame.data);
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
