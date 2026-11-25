import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";

export function testClient(base) {
  async function api(path, token, input, method = input === undefined ? "GET" : "POST") {
    const headers = new Headers();
    if (token) headers.set("Authorization", `Bearer ${token}`);
    let body;
    if (input instanceof FormData) body = input;
    else if (input !== undefined) {
      headers.set("Content-Type", "application/json");
      body = JSON.stringify(input);
    }
    const response = await fetch(new URL(`/api/v1${path}`, base), {
      method, headers, body, signal: AbortSignal.timeout(15_000),
    });
    const result = await response.json();
    assert.equal(result.code, 0, `${path}: ${result.msg}`);
    return result.data;
  }

  async function account() {
    const username = "load_" + randomUUID().replaceAll("-", "").slice(0, 16);
    const credentials = { username, password: randomUUID() };
    await api("/auth/register", undefined, { ...credentials, nickname: username });
    const { user_id: id, access_token: token } = await api("/auth/login", undefined, credentials);
    return { id, token, username };
  }

  async function befriend(sender, receiver) {
    const { id } = await api("/friend-requests", sender.token, { peer_username: receiver.username });
    await api(`/friend-requests/${id}`, receiver.token, { status: "accepted" }, "PUT");
  }

  async function connect(receiver, onMessage) {
    const { ticket } = await api("/ws/tickets", receiver.token, {});
    const url = new URL("/ws", base);
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    url.searchParams.set("ticket", ticket);
    const socket = new WebSocket(url);
    try {
      await new Promise((resolve, reject) => {
        const timeout = setTimeout(() => reject(new Error("WebSocket handshake timed out")), 5000);
        socket.addEventListener("open", () => { clearTimeout(timeout); resolve(); }, { once: true });
        socket.addEventListener("error", () => { clearTimeout(timeout); reject(new Error("WebSocket handshake failed")); }, { once: true });
      });
      socket.addEventListener("message", ({ data }) => {
        const event = JSON.parse(data);
        onMessage(event);
      });
      return socket;
    } catch (error) {
      socket.close();
      throw error;
    }
  }

  return { api, account, befriend, connect };
}
