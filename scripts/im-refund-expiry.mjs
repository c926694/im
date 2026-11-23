import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { randomUUID } from "node:crypto";
import { promisify } from "node:util";
import { setTimeout as sleep } from "node:timers/promises";
import { testClient } from "./im-test-client.mjs";

const exec = promisify(execFile);
async function compose(...args) {
  const { stdout } = await exec("docker", ["compose", "-p", "im-expiry-test", "-f", "docker/compose.yaml", ...args], {
    timeout: 60_000,
  });
  return stdout.trim();
}

async function sql(query) {
  return compose("exec", "-T", "mysql", "sh", "-c",
    'MYSQL_PWD="$MYSQL_PASSWORD" mysql -u "$MYSQL_USER" "$MYSQL_DATABASE" -N -e "$1"',
    "query", query);
}

async function waitFor(description, check) {
  const deadline = performance.now() + 45_000;
  while (performance.now() < deadline) {
    if (await check()) return;
    await sleep(250);
  }
  throw new Error(description);
}

assert.equal(await compose("port", "backend", "8080"), "127.0.0.1:18080", "expected dedicated expiry deployment");
const { api, account, befriend } = testClient("http://127.0.0.1:18080");
const sender = await account();
const peer = await account();
await befriend(sender, peer);
const { id } = await api("/conversations/direct", sender.token, { peer_username: peer.username });

async function sendPacket() {
  const result = await api(`/conversations/${id}/red-packets`, sender.token, {
    client_msg_id: randomUUID(), points_per_share: "10", share_count: 1,
  });
  assert.match(result.red_packet_id, /^\d+$/);
  return result.red_packet_id;
}

const standard = await sendPacket();
const scheduledWhere = (packetID) => `kind = 'packet_expiry' AND red_packet_id = ${packetID}`;
const published = async (packetID) =>
  (await sql(`SELECT COUNT(*) FROM outbox_events WHERE ${scheduledWhere(packetID)} AND published_at IS NOT NULL`)) === "1";

await waitFor("Broker did not accept the 24-hour task", () => published(standard));
const [standardDeadline, seconds] = (await sql(
  `SELECT deliver_at, TIMESTAMPDIFF(SECOND, UTC_TIMESTAMP(6), deliver_at) FROM outbox_events WHERE ${scheduledWhere(standard)}`,
)).split("\t");
assert.ok(Number(seconds) > 86_300 && Number(seconds) <= 86_400);
assert.equal(await sql(`SELECT expires_at FROM red_packets WHERE id = ${standard}`), standardDeadline);
assert.equal((await api(`/red-packets/${standard}`, peer.token)).status, "open");

const quick = await sendPacket();
// 只在隔离环境缩短该测试红包的有效期，使用应用的真实延迟发布与退款链路。
await sql(`START TRANSACTION; SET @deadline = UTC_TIMESTAMP(6) + INTERVAL 12 SECOND;
  UPDATE red_packets SET expires_at = @deadline WHERE id = ${quick};
  UPDATE outbox_events SET deliver_at = @deadline, published_at = NULL, next_attempt_at = UTC_TIMESTAMP(6)
  WHERE ${scheduledWhere(quick)}; COMMIT;`);
await waitFor("Broker did not accept the short delay", () => published(quick));
assert.equal((await api(`/red-packets/${quick}`, peer.token)).status, "open");
const lockTest = process.env.IM_REFUND_TEST_LOCK === "1";
if (lockTest) {
  // 独立数据库连接持有实际红包行锁，让第一次退款等待超过事务期限。
  const lockName = `im-refund-test-${quick}`;
  const lock = sql(`START TRANSACTION;
    SELECT id FROM red_packets WHERE id = ${quick} FOR UPDATE;
    SELECT GET_LOCK('${lockName}', 0); DO SLEEP(25); COMMIT; SELECT RELEASE_LOCK('${lockName}');`);
  try {
    await waitFor("test did not acquire the packet row lock", async () =>
      (await sql(`SELECT IS_USED_LOCK('${lockName}') IS NOT NULL`)) === "1");
    await waitFor("refund handler did not report an unacknowledged attempt", async () => {
      const logs = await compose("logs", "--tail", "40", "backend");
      return logs.split("\n").some((line) =>
        line.includes("RocketMQ task remains unacknowledged") && line.includes(`refund packet ${quick}:`));
    });
    assert.equal((await api("/points/balance", sender.token)).balance, "980");
  } finally {
    await lock;
  }
}
await waitFor("expiry was not refunded", async () => (await api(`/red-packets/${quick}`, peer.token)).status === "refunded");
assert.equal((await api("/points/balance", sender.token)).balance, "990");
const credit = (await api("/points/ledger", sender.token)).filter((row) => row.kind === "refund" && row.ref_id === quick);
assert.equal(credit.length, 1);

await sql(`UPDATE outbox_events SET published_at = NULL, next_attempt_at = UTC_TIMESTAMP(6) WHERE ${scheduledWhere(quick)}`);
await waitFor("redrive did not publish", () => published(quick));
await sleep(2500);
assert.equal((await api("/points/balance", sender.token)).balance, "990");
assert.equal((await api("/points/ledger", sender.token)).filter((row) => row.kind === "refund" && row.ref_id === quick).length, 1);
console.log(JSON.stringify({ accepted24HourTask: true, refundedShortTask: true, duplicateCredit: false, retriedAfterLock: lockTest }));
