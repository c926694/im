import { pageSize } from "./api";
import type { Conversation, Message } from "./api";

export type Timeline = {
  messages: Message[];
  oldestSeq: string | null;
  cursor: bigint;
  hasOlder: boolean;
};

export const emptyTimeline: Timeline = {
  messages: [], oldestSeq: null, cursor: 0n, hasOlder: false,
};

export function timelineKey(conversation: Conversation) {
  return `${conversation.id}:${conversation.joined_seq}`;
}

export type TimelineAction = { source: "keep"; keys: string[] } | {
  key: string;
  joinedSeq: bigint;
  source: "history" | "sync" | "live";
  messages: Message[];
};

export function timelinesReducer(timelines: Record<string, Timeline>, action: TimelineAction) {
  if (action.source === "keep") {
    const keys = new Set(action.keys);
    return Object.fromEntries(Object.entries(timelines).filter(([key]) => keys.has(key)));
  }
  const { key, joinedSeq } = action;
  const timeline = timelines[key] ?? emptyTimeline;
  const byID = new Map(timeline.messages.map((message) => [message.id, message]));
  for (const message of action.messages) {
    if (BigInt(message.seq) > joinedSeq) byID.set(message.id, message);
  }
  const messages = [...byID.values()].sort((a, b) => BigInt(a.seq) < BigInt(b.seq) ? -1 : 1);

  let { oldestSeq, cursor, hasOlder } = timeline;
  if (action.source === "history") {
    const oldest = action.messages.at(-1)?.seq;
    // 首次打开会话时，以最近一页的起点为准，更早的消息由历史查询加载。
    const firstAllowed = joinedSeq + 1n;
    if (oldestSeq === null) cursor = oldest ? BigInt(oldest) - 1n : joinedSeq;
    oldestSeq = oldest ?? oldestSeq ?? firstAllowed.toString();
    hasOlder = action.messages.length === pageSize && BigInt(oldestSeq) > firstAllowed;
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
  return { ...timelines, [key]: { messages, oldestSeq, cursor, hasOlder } };
}
