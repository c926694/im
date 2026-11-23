import { useEffect, useState } from "react";
import type { ChatAPI } from "./api";
import { Dialog } from "./Dialog";

export function ChatImage({ api, id }: { api: ChatAPI; id: string }) {
  const [src, setSrc] = useState("");
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [open, setOpen] = useState(false);

  useEffect(() => {
    const controller = new AbortController();
    let url: string | undefined;
    void api.image(id, controller.signal).then((image) => {
      if (controller.signal.aborted) return;
      url = URL.createObjectURL(image);
      setSrc(url);
    }).catch((error: Error) => {
      if (!controller.signal.aborted) setError(error.message);
    });
    return () => {
      controller.abort();
      if (url) URL.revokeObjectURL(url);
    };
  }, [api, id, attempt]);

  if (error) return <button type="button" onClick={() => {
    setError("");
    setAttempt((value) => value + 1);
  }}>{error} · 重试</button>;
  if (!src) return <span role="status">正在加载图片…</span>;
  return <>
    <button type="button" className="chat-image" aria-label="查看原图" onClick={() => setOpen(true)}>
      <img src={src} alt="聊天图片" />
    </button>
    {open && <Dialog title="查看原图" busy={false} onClose={() => setOpen(false)} className="chat-image-dialog">
      <img src={src} alt="聊天原图" />
    </Dialog>}
  </>;
}
