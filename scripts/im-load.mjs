import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { readFile } from "node:fs/promises";
import { setTimeout as sleep } from "node:timers/promises";
import { testClient } from "./im-test-client.mjs";

const base = process.env.IM_LOAD_URL;
assert.ok(base, "IM_LOAD_URL must point to a dedicated test deployment");
const mode = process.env.IM_LOAD_MODE ?? "direct";
const senders = Number(process.env.IM_LOAD_SENDERS ?? 8);
const perSender = Number(process.env.IM_LOAD_MESSAGES ?? 30);
const idle = Number(process.env.IM_LOAD_IDLE ?? 0);
const image = process.env.IM_LOAD_IMAGE ? await readFile(process.env.IM_LOAD_IMAGE) : null;
assert.ok(["group", "direct"].includes(mode));
assert.ok(Number.isSafeInteger(senders) && senders >= 1 && senders <= 199);
assert.ok(Number.isSafeInteger(perSender) && perSender >= 1);
assert.ok(Number.isSafeInteger(idle) && idle >= 0);
assert.ok(!image || image.length <= 5 * 1024 * 1024);

const { api, account, befriend, connect } = testClient(base);

function p95(values) {
  if (values.length === 0) return null;
  const sorted = [...values].sort((a, b) => a - b);
  return Number(sorted[Math.ceil(sorted.length * 0.95) - 1].toFixed(1));
}

const sockets = [];
try {
  const lanes = [];
  const groupOwner = mode === "group" ? await account() : null;
  for (let index = 0; index < senders; index++) {
    const sender = await account();
    const receiver = groupOwner ?? await account();
    await befriend(receiver, sender);
    const id = groupOwner ? undefined : (await api("/conversations/direct", sender.token, { peer_username: receiver.username })).id;
    lanes.push({ sender, receiver, id });
  }
  const groupID = groupOwner ? (await api("/groups", groupOwner.token, {
    name: "并发测试群", member_ids: lanes.map(({ sender }) => sender.id),
  })).id : undefined;
  const observed = new Map();
  const started = new Map();
  const record = (receiver, message) => {
    const key = `${receiver.id}:${message.client_msg_id}`;
    if (started.has(key) && !observed.has(key)) observed.set(key, performance.now() - started.get(key));
  };
  const watch = groupOwner ? [groupOwner] : lanes.map(({ receiver }) => receiver);
  for (const receiver of watch) {
    sockets.push(await connect(receiver, ({ type, data }) => {
      if (type === "message.created") record(receiver, data);
    }));
  }
  const idleAccount = idle > 0 ? await account() : null;
  for (let index = 0; index < idle; index++) sockets.push(await connect(idleAccount, () => {}));
  const receiverChecks = new Map();
  for (const lane of lanes) receiverChecks.set(groupID ?? lane.id, { receiver: lane.receiver, ids: new Set() });

  const latencies = [];
  let failures = 0;
  let imageErrors = 0;
  let firstError;
  const startedAt = performance.now();
  await Promise.all(lanes.map(async ({ sender, receiver, id }) => {
    const conversationID = groupID ?? id;
    for (let index = 0; index < perSender; index++) {
      const client_msg_id = randomUUID();
      const key = `${receiver.id}:${client_msg_id}`;
      const useImage = image && index % 10 === 0;
      const content = "并发测试";
      let input = { client_msg_id, content };
      if (useImage) {
        input = new FormData();
        input.set("client_msg_id", client_msg_id);
        input.set("content", content);
        input.set("image", new Blob([image]), "load-image");
      }
      const start = performance.now();
      started.set(key, start);
      try {
        const message = await api(`/conversations/${conversationID}/messages`, sender.token, input);
        receiverChecks.get(conversationID).ids.add(message.id);
        latencies.push(performance.now() - start);
      } catch (error) {
        failures++;
        if (useImage) imageErrors++;
        firstError ??= error.message;
        started.delete(key);
        observed.delete(key);
      }
    }
  }));
  const duration = (performance.now() - startedAt) / 1000;
  const deadline = performance.now() + 8000;
  while (observed.size < latencies.length && performance.now() < deadline) await sleep(100);
  const onlineSockets = sockets.filter(({ readyState }) => readyState === WebSocket.OPEN).length;
  let missingFromSync = 0;
  for (const [id, { receiver, ids }] of receiverChecks) {
    let cursor = "0";
    while (true) {
      const page = await api(`/conversations/${id}/messages?after_seq=${cursor}&limit=100`, receiver.token);
      for (const message of page) ids.delete(message.id);
      if (page.length < 100) break;
      cursor = page.at(-1).seq;
    }
    missingFromSync += ids.size;
  }
  console.log(JSON.stringify({
    mode, senders, perSender, imageMixed: Boolean(image), idle, onlineSockets, acknowledged: latencies.length,
    failures, imageErrors, firstError, sendSeconds: Number(duration.toFixed(2)),
    errorRate: failures / (senders * perSender), acknowledgedQPS: Number((latencies.length / duration).toFixed(1)),
    httpP95Ms: p95(latencies), pushP95Ms: p95([...observed.values()]),
    pushesMissingAtDeadline: latencies.length - observed.size, missingFromSync,
  }, null, 2));
  if (failures || missingFromSync || observed.size !== latencies.length || onlineSockets !== sockets.length) process.exitCode = 1;
} finally {
  for (const socket of sockets) socket.close();
}
