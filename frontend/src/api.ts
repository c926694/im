export type Session = {
  userID: string;
  username: string;
  accessToken: string;
  expiresAt: number;
};

export type Conversation = {
  id: string;
  is_friend: boolean;
  peer_nickname: string;
  last_seq: string;
  last_activity_at: string;
};

export type Profile = { id: string; username: string; nickname: string };
export type FriendRequest = { id: string; peer: Profile; direction: "incoming" | "outgoing" };

export type Message = {
  id: string;
  conversation_id: string;
  sender_id: string;
  client_msg_id: string;
  seq: string;
  content: string;
  created_at: string;
};

export type Draft = { client_msg_id: string; content: string };
type Credentials = { username: string; password: string };

const errorMessages: Record<string, string> = {
  invalid_registration: "请检查用户名和昵称。",
  invalid_login: "请检查用户名。",
  invalid_credentials: "用户名或密码不正确。",
  username_taken: "该用户名已被使用。",
  invalid_token: "登录已失效，请重新登录。",
  invalid_peer_username: "用户名需使用 1 至 32 个字母、数字或下划线。",
  self_conversation: "无法和自己发起单聊。",
  user_not_found: "没有找到这个用户名，请核对后重试。",
  self_friend_request: "无法向自己发送好友申请。",
  incoming_friend_request: "对方已向你发送申请，请在收到的申请中处理。",
  already_friends: "你们已经是好友。",
  friend_request_unavailable: "这条申请已失效，请刷新申请列表。",
  not_friends: "成为好友后才能发送消息。",
  not_conversation_member: "你没有权限访问这个会话。",
  invalid_content: "消息需要包含正文，且最多 2000 个字符。",
  internal_error: "服务暂时无法完成请求，请重试。",
};

export class APIError extends Error {
  constructor(readonly code: string) {
    super(errorMessages[code] ?? "请求失败，请重试。");
  }
}

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const headers = new Headers(options.headers);
  if (options.body) headers.set("Content-Type", "application/json");

  let response: Response;
  try {
    response = await fetch(`/api/v1${path}`, { ...options, headers });
  } catch (error) {
    if (options.signal?.aborted) throw error;
    throw new Error("无法连接服务，请检查网络后重试。");
  }
  if (!response.ok) {
    const body = response.headers.get("content-type")?.includes("application/json")
      ? await response.json() as { error: string }
      : { error: "internal_error" };
    throw new APIError(body.error);
  }
  return response.json() as Promise<T>;
}

export function register(form: Credentials & { nickname: string }) {
  return request<{ user_id: string }>("/auth/register", {
    method: "POST",
    body: JSON.stringify(form),
  });
}

export async function login(form: Credentials): Promise<Session> {
  const result = await request<{ user_id: string; access_token: string; expires_in: number }>(
    "/auth/login", { method: "POST", body: JSON.stringify(form) },
  );
  return {
    userID: result.user_id,
    username: form.username.trim().toLowerCase(),
    accessToken: result.access_token,
    expiresAt: Date.now() + result.expires_in * 1000,
  };
}

export const pageSize = 50;

export function chatAPI(session: Session, onExpired: () => void) {
  async function authorized<T>(path: string, options?: RequestInit) {
    try {
      return await request<T>(path, {
        ...options,
        headers: { Authorization: `Bearer ${session.accessToken}` },
      });
    } catch (error) {
      if (error instanceof APIError && error.code === "invalid_token") onExpired();
      throw error;
    }
  }

  return {
    lookupUser: (username: string) => authorized<Profile>(`/users/by-username?${new URLSearchParams({ username })}`),
    friends: (signal: AbortSignal) => authorized<Profile[]>("/friends", { signal }),
    friendRequests: (signal: AbortSignal) => authorized<FriendRequest[]>("/friend-requests", { signal }),
    requestFriend: (username: string) => authorized<FriendRequest>("/friend-requests", {
      method: "POST", body: JSON.stringify({ peer_username: username }),
    }),
    resolveFriend: (id: string, status: "accepted" | "rejected") => authorized<{ status: string }>(`/friend-requests/${id}`, {
      method: "PATCH", body: JSON.stringify({ status }),
    }),
    removeFriend: (id: string) => authorized<{ status: string }>(`/friends/${id}`, { method: "DELETE" }),
    conversations: (signal?: AbortSignal) => authorized<Conversation[]>("/conversations", { signal }),
    direct: (peerUsername: string) => authorized<{ id: string }>("/conversations/direct", {
      method: "POST",
      body: JSON.stringify({ peer_username: peerUsername }),
    }),
    ticket: (signal: AbortSignal) => authorized<{ ticket: string }>("/ws/tickets", { method: "POST", signal }),
    messages: (id: string, cursor: { before_seq: string } | { after_seq: string } | null, signal?: AbortSignal) => {
      const params = new URLSearchParams({ limit: String(pageSize), ...cursor });
      return authorized<Message[]>(`/conversations/${id}/messages?${params}`, { signal });
    },
    send: (id: string, draft: Draft) => authorized<Message>(`/conversations/${id}/messages`, {
      method: "POST",
      body: JSON.stringify(draft),
    }),
  };
}

export type ChatAPI = ReturnType<typeof chatAPI>;
