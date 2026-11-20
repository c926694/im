import { pageSize } from "./api";
import type { Message } from "./api";

export type Timeline = {
  messages: Message[];
  oldestSeq: string | null;
  cursor: bigint;
  hasOlder: boolean;
};

export const emptyTimeline: Timeline = {
  messages: [], oldestSeq: null, cursor: 0n, hasOlder: false,
};

export type TimelineAction = {
  id: string;
  source: "history" | "sync" | "live";
  messages: Message[];
};

export function timelinesReducer(timelines: Record<string, Timeline>, action: TimelineAction) {
  const timeline = timelines[action.id] ?? emptyTimeline;
  const byID = new Map(timeline.messages.map((message) => [message.id, message]));
  for (const message of action.messages) byID.set(message.id, message);
  const messages = [...byID.values()].sort((a, b) => BigInt(a.seq) < BigInt(b.seq) ? -1 : 1);

  let { oldestSeq, cursor, hasOlder } = timeline;
  if (action.source === "history") {
    const oldest = action.messages.at(-1)?.seq;
    // 首次打开会话时，以最近一页的起点为准，更早的消息由历史查询加载。
    if (oldestSeq === null) cursor = BigInt(oldest ?? "1") - 1n;
    oldestSeq = oldest ?? oldestSeq ?? "1";
    hasOlder = action.messages.length === pageSize && oldestSeq !== "1";
  }
  if (oldestSeq !== null) {
    // 已读进度与本页的同步进度独立，WebSocket 序号出现缺口时保留原进度。
    for (const message of messages) {
      const seq = BigInt(message.seq);
      if (seq <= cursor) continue;
      if (seq !== cursor + 1n) break;
      cursor = seq;
    }
  }
  return { ...timelines, [action.id]: { messages, oldestSeq, cursor, hasOlder } };
}
