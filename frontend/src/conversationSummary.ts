import type { Conversation, Message } from "./api";

export function projectSummary(base: Conversation, messages: readonly Message[], userID: string, read?: string) {
  const baseSeq = BigInt(base.last_seq);
  const baseRead = BigInt(base.last_read_seq);
  const observedRead = read === undefined ? baseRead : BigInt(read);
  const readSeq = observedRead > baseRead ? observedRead : baseRead;
  let unread = readSeq >= baseSeq ? 0 : base.unread_count;
  let seq = baseSeq;
  let latest: Message | undefined;
  // 已读推进到摘要范围中间时，只有保留了完整区间才可在本地重新统计。
  let partialRead = readSeq > baseRead && readSeq < baseSeq ? readSeq : baseSeq;
  if (partialRead < baseSeq) unread = 0;
  let gap = false;
  let start = 0;
  let end = messages.length;
  while (start < end) {
    const middle = Math.floor((start + end) / 2);
    if (BigInt(messages[middle].seq) <= partialRead) start = middle + 1;
    else end = middle;
  }
  for (let index = start; index < messages.length; index++) {
    const message = messages[index];
    const next = BigInt(message.seq);
    if (next <= baseSeq) {
      if (partialRead < baseSeq && next === partialRead + 1n) {
        partialRead = next;
        if (message.sender_id !== userID) unread++;
      }
      continue;
    }
    if (next <= seq) continue;
    if (next !== seq + 1n) {
      gap = true;
      break;
    }
    if (next > readSeq && message.sender_id !== userID) unread++;
    seq = next;
    latest = message;
  }
  const incomplete = partialRead < baseSeq;
  return {
    dirty: gap || incomplete,
    conversation: {
      ...base,
      last_seq: seq.toString(),
      last_read_seq: readSeq.toString(),
      unread_count: incomplete ? base.unread_count : unread,
      ...(latest && { last_message: latest.content, last_activity_at: latest.created_at }),
    },
  };
}
