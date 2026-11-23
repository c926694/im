import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { randomUUID } from "node:crypto";
import { promisify } from "node:util";
import { setTimeout as sleep } from "node:timers/promises";
import { testClient } from "./im-test-client.mjs";

const exec = promisify(execFile);
// 故障演练固定使用独立 Compose 项目，避免停止正在使用的服务。
async function compose(...args) {
  const { stdout } = await exec("docker", ["compose", "-p", "im-resilience-test", "-f", "docker/compose.yaml", ...args], {
    timeout: 90_000,
  });
  return stdout.trim();
}

async function waitFor(description, check, timeout = 30_000) {
  const deadline = performance.now() + timeout;
  while (performance.now() < deadline) {
    if (await check()) return;
    await sleep(200);
  }
  throw new Error(description);
}

const address = await compose("port", "backend", "8080");
assert.equal(address, "127.0.0.1:18080", "expected isolated backend port");
const base = "http://" + address;
const { api, account, befriend, connect } = testClient(base);
const sender = await account();
const receiver = await account();
await befriend(sender, receiver);
const { id } = await api("/conversations/direct", sender.token, { peer_username: receiver.username });
const messagePath = `/conversations/${id}/messages`;
const draft = { client_msg_id: randomUUID(), content: "通知故障期间的消息" };
const downtimeStarted = performance.now();
let frontendSocket;
let brokerStarted = false;

try {
  await compose("stop", "rocketmq-broker", "rocketmq-namesrv");
  const restartStarted = performance.now();
  await compose("restart", "backend");
  await waitFor("backend did not recover while RocketMQ was unavailable", async () => {
    try {
      const response = await fetch(base + "/readyz", { signal: AbortSignal.timeout(1500) });
      return response.status === 204;
    } catch (error) {
      if (error instanceof TypeError || error.name === "TimeoutError") return false;
      throw error;
    }
  });
  const backendRecoveryMs = Math.round(performance.now() - restartStarted);
  const events = [];
  frontendSocket = await connect(receiver, (event) => events.push(event));

  const sent = await api(messagePath, sender.token, draft);
  assert.equal((await api(messagePath, sender.token, draft)).id, sent.id);
  const packet = await api(`/conversations/${id}/red-packets`, sender.token, {
    client_msg_id: randomUUID(), points_per_share: "10", share_count: 1,
  });
  const claimPath = `/red-packets/${packet.red_packet_id}/claim`;
  const claim = await api(claimPath, receiver.token, {});
  assert.deepEqual(await api(claimPath, receiver.token, {}), claim);
  assert.equal((await api("/points/balance", sender.token)).balance, "990");
  assert.equal((await api("/points/balance", receiver.token)).balance, "1010");
  const history = await api(messagePath + "?after_seq=0", receiver.token);
  assert.deepEqual(history.map((message) => message.kind), ["text", "red_packet", "red_packet_claim"]);
  assert.equal((await api(`/conversations/${id}/sync-state`, receiver.token)).last_seq, "3");
  assert.equal(events.length, 0);

  const brokerRecoveryStarted = performance.now();
  await compose("start", "rocketmq-namesrv", "rocketmq-broker");
  brokerStarted = true;
  await waitFor("pending notifications did not reach WebSocket after Broker recovery", () => {
    const seen = new Set(events.filter(({ type }) => type === "message.created").map(({ data }) => data.id));
    return history.every(({ id }) => seen.has(id)) &&
      events.some(({ type, data }) => type === "red_packet.updated" && data.red_packet_id === packet.red_packet_id && data.status === "claimed");
  }, 90_000);
  assert.equal((await api(messagePath + "?after_seq=0", receiver.token)).length, 3);
  assert.equal((await api("/points/balance", receiver.token)).balance, "1010");
  console.log(JSON.stringify({
    backendRecoveryMs,
    brokerRecoveryToPushMs: Math.round(performance.now() - brokerRecoveryStarted),
    drillDurationMs: Math.round(performance.now() - downtimeStarted),
    messagesRecovered: history.length,
    receiverBalance: "1010",
  }, null, 2));
} finally {
  frontendSocket?.close();
  if (!brokerStarted) await compose("start", "rocketmq-namesrv", "rocketmq-broker");
}
