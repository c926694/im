export type Session = {
  userID: string;
  username: string;
  accessToken: string;
  expiresAt: number;
};

type ConversationBase = {
  id: string;
  joined_seq: string;
  last_seq: string;
  last_read_seq: string;
  last_message: string;
  unread_count: number;
  last_activity_at: string;
};

export type Conversation = ConversationBase & (
  { kind: "direct"; peer_nickname: string; is_friend: boolean } |
  { kind: "group"; group_name: string }
);

export function conversationTitle(conversation: Conversation) {
  return conversation.kind === "group" ? conversation.group_name : conversation.peer_nickname;
}

export type Profile = { id: string; username: string; nickname: string };
export type FriendRequest = { id: string; peer: Profile; direction: "incoming" | "outgoing" };
export type GroupRole = "owner" | "admin" | "member";
export type GroupMember = Profile & { role: GroupRole };
export type Group = { id: string; name: string; members: GroupMember[] };

export type Message = {
  kind: "text" | "red_packet" | "red_packet_claim";
  red_packet_id?: string;
  red_packet_status?: RedPacketStatus;
  id: string;
  conversation_id: string;
  sender_id: string;
  sender_nickname: string;
  client_msg_id: string;
  seq: string;
  content: string;
  created_at: string;
};

export type PacketDraft = {
  client_msg_id: string;
  points_per_share: string;
  share_count: number;
};
export type PacketClaim = { points: string; created_at: string };
export type RedPacketStatus = "open" | "claimed" | "expired" | "refunded";
export type RedPacket = {
  id: string;
  sender_id: string;
  points_per_share: string;
  share_count: number;
  remaining: number;
  status: RedPacketStatus;
  expires_at: string;
  can_claim: boolean;
  my_claim: PacketClaim | null;
};
export type PointEntry = {
  id: string;
  kind: "grant" | "send" | "claim" | "refund";
  ref_id: string;
  delta: string;
  balance_after: string;
  created_at: string;
};

export type Draft = { client_msg_id: string; content: string };
type Credentials = { username: string; password: string };

type APIResponse<T> = {
  code: number;
  msg: string;
  data: T;
};

export const errorCode = {
  invalidToken: 40003,
  notFriends: 40006,
  notMember: 40007,
} as const;

export class APIError extends Error {
  constructor(readonly code: number, message: string) {
    super(message);
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
  if (!response.headers.get("content-type")?.includes("application/json")) {
    throw new Error("服务返回了无法识别的响应。");
  }
  const body = await response.json() as APIResponse<T>;
  if (body.code !== 0) throw new APIError(body.code, body.msg);
  return body.data;
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
      if (error instanceof APIError && error.code === errorCode.invalidToken) onExpired();
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
    createGroup: (name: string, memberIDs: string[]) => authorized<{ id: string }>("/groups", {
      method: "POST", body: JSON.stringify({ name, member_ids: memberIDs }),
    }),
    groupDetails: (id: string, signal: AbortSignal) => authorized<Group>(`/groups/${id}`, { signal }),
    renameGroup: (id: string, name: string) => authorized<{ id: string }>(`/groups/${id}`, {
      method: "PATCH", body: JSON.stringify({ name }),
    }),
    inviteGroupMembers: (id: string, memberIDs: string[]) => authorized<{ id: string }>(`/groups/${id}/members`, {
      method: "POST", body: JSON.stringify({ member_ids: memberIDs }),
    }),
    removeGroupMember: (id: string, userID: string) => authorized<{ id: string }>(`/groups/${id}/members/${userID}`, { method: "DELETE" }),
    setGroupRole: (id: string, userID: string, role: "admin" | "member") => authorized<{ id: string }>(`/groups/${id}/members/${userID}`, {
      method: "PATCH", body: JSON.stringify({ role }),
    }),
    transferGroup: (id: string, userID: string) => authorized<{ id: string }>(`/groups/${id}/transfer`, {
      method: "POST", body: JSON.stringify({ user_id: userID }),
    }),
    leaveGroup: (id: string) => authorized<{ id: string }>(`/groups/${id}/leave`, { method: "POST" }),
    ticket: (signal: AbortSignal) => authorized<{ ticket: string }>("/ws/tickets", { method: "POST", signal }),
    messages: (id: string, cursor: { before_seq: string } | { after_seq: string } | null, signal?: AbortSignal) => {
      const params = new URLSearchParams({ limit: String(pageSize), ...cursor });
      return authorized<Message[]>(`/conversations/${id}/messages?${params}`, { signal });
    },
    send: (id: string, draft: Draft) => authorized<Message>(`/conversations/${id}/messages`, {
      method: "POST",
      body: JSON.stringify(draft),
    }),
    pointBalance: (signal?: AbortSignal) => authorized<{ balance: string }>("/points/balance", { signal }),
    pointLedger: (beforeID?: string, signal?: AbortSignal) => {
      const query = beforeID ? `?before_id=${beforeID}` : "";
      return authorized<PointEntry[]>(`/points/ledger${query}`, { signal });
    },
    sendPacket: (id: string, draft: PacketDraft) => authorized<{ red_packet_id: string; message: Message }>(`/conversations/${id}/red-packets`, {
      method: "POST", body: JSON.stringify(draft),
    }),
    packetDetails: (id: string, signal?: AbortSignal) => authorized<RedPacket>(`/red-packets/${id}`, { signal }),
    claimPacket: (id: string) => authorized<PacketClaim>(`/red-packets/${id}/claim`, { method: "POST" }),
    markRead: (id: string, seq: string, signal?: AbortSignal) => authorized<null>(`/conversations/${id}/read`, {
      method: "POST",
      body: JSON.stringify({ seq }),
      signal,
    }),
  };
}

export type ChatAPI = ReturnType<typeof chatAPI>;
