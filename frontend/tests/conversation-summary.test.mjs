import assert from "node:assert/strict";
import test from "node:test";
import { projectSummary } from "../src/conversationSummary.ts";

const base = {
  id: "1", kind: "group", group_name: "群聊",
  joined_seq: "0", last_seq: "10", last_read_seq: "7", unread_count: 2,
  last_message: "第十条", last_activity_at: "2026-11-23T10:00:00Z",
};
const message = (seq, sender = "2") => ({ id: String(seq), seq: String(seq), sender_id: sender,
  content: `消息 ${seq}`, created_at: "2026-11-23T10:01:00Z" });

test("a sequence gap does not advance the summary; ordered backfill counts a message once", () => {
  const gap = projectSummary(base, [message(12)], "1");
  assert.equal(gap.dirty, true);
  assert.equal(gap.conversation.last_seq, "10");
  const filled = projectSummary(base, [message(11), message(12), message(12), message(13, "1")], "1");
  assert.equal(filled.dirty, false);
  assert.equal(filled.conversation.last_seq, "13");
  assert.equal(filled.conversation.last_message, "消息 13");
  assert.equal(filled.conversation.unread_count, 4);
});

test("read progress clears known messages and keeps later unread messages", () => {
  const result = projectSummary(base, [message(11), message(12)], "1", "11");
  assert.equal(result.conversation.last_read_seq, "11");
  assert.equal(result.conversation.unread_count, 1);
  const old = projectSummary(base, [], "1", "6");
  assert.equal(old.conversation.last_read_seq, "7");
  assert.equal(old.conversation.unread_count, 2);
});

test("reading part of the baseline requires reconciliation unless the remaining range is known", () => {
  assert.equal(projectSummary(base, [], "1", "8").dirty, true);
  const result = projectSummary(base, [message(9, "1"), message(10), message(11)], "1", "8");
  assert.equal(result.dirty, false);
  assert.equal(result.conversation.unread_count, 2);
  const refreshed = projectSummary({ ...base, last_seq: "11", last_read_seq: "8", unread_count: 2 }, [message(11)], "1", "8");
  assert.equal(refreshed.conversation.unread_count, 2);
});
