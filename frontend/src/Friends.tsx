import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import type { ChatAPI, FriendRequest, Profile } from "./api";

export function Friends({ api, userID, view, onOpen, onChanged }: {
  api: ChatAPI;
  userID: string;
  view: "friends" | "requests";
  onOpen: (username: string) => Promise<void>;
  onChanged: () => void;
}) {
  const [friends, setFriends] = useState<Profile[]>([]);
  const [requests, setRequests] = useState<FriendRequest[]>([]);
  const [profile, setProfile] = useState<Profile | null>(null);
  const [revision, setRevision] = useState(0);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [removeID, setRemoveID] = useState<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError("");
    const load = view === "friends"
      ? api.friends(controller.signal).then(setFriends)
      : api.friendRequests(controller.signal).then(setRequests);
    void load.catch((error: Error) => {
      if (!controller.signal.aborted) setError(error.message);
    }).finally(() => {
      if (!controller.signal.aborted) setLoading(false);
    });
    return () => controller.abort();
  }, [api, view, revision]);

  async function run(action: () => Promise<void>) {
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await action();
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  function lookup(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const username = String(new FormData(event.currentTarget).get("username"));
    setProfile(null);
    void run(async () => setProfile(await api.lookupUser(username)));
  }

  function resolve(request: FriendRequest, status: "accepted" | "rejected") {
    void run(async () => {
      await api.resolveFriend(request.id, status);
      setRequests((current) => current.filter((item) => item.id !== request.id));
      setNotice(status === "accepted" ? "已添加好友。" : "已拒绝申请。");
      if (status === "accepted") onChanged();
    });
  }

  const incoming = requests.filter((request) => request.direction === "incoming");
  const outgoing = requests.filter((request) => request.direction === "outgoing");

  return (
    <section className="conversations friends" aria-labelledby="friends-title" aria-busy={loading || busy}>
      <div className="friends-heading">
        <h2 id="friends-title">{view === "friends" ? "好友" : "好友申请"}</h2>
        <button type="button" disabled={loading || busy} onClick={() => setRevision((value) => value + 1)}>刷新</button>
      </div>
      {view === "friends" && <form className="friend-lookup" onSubmit={lookup}>
        <label htmlFor="friend-username">完整用户名</label>
        <div className="direct-fields">
          <input id="friend-username" name="username" autoCapitalize="none" spellCheck={false} required
            disabled={busy} onChange={() => setProfile(null)} />
          <button type="submit" disabled={busy}>查找</button>
        </div>
      </form>}
      {error && <p className="list-note error" role="alert">{error}</p>}
      {notice && <p className="list-note" role="status">{notice}</p>}
      {loading && <p className="list-note" role="status">正在加载…</p>}
      {view === "friends" && <>
        {profile && <div className="friend-row friend-result">
          <FriendName profile={profile} />
          {profile.id === userID ? <span>你的账号</span> : <button type="button" disabled={busy} onClick={() => void run(async () => {
            await api.requestFriend(profile.username);
            setProfile(null);
            setNotice("好友申请已发送。");
          })}>发送申请</button>}
        </div>}
        {!loading && !error && friends.length === 0 && <p className="list-note">暂无好友。</p>}
        <ul>{friends.map((friend) => <li key={friend.id} className="friend-row">
          <FriendName profile={friend} />
          <div className="friend-actions">
            {removeID === friend.id ? <>
              <button type="button" disabled={busy} onClick={() => setRemoveID(null)}>取消</button>
              <button type="button" disabled={busy} onClick={() => void run(async () => {
                await api.removeFriend(friend.id);
                setFriends((current) => current.filter((item) => item.id !== friend.id));
                setRemoveID(null);
                setNotice("已解除好友，聊天记录仍可查看。");
                onChanged();
              })}>确认解除</button>
            </> : <>
              <button type="button" disabled={busy} onClick={() => void run(() => onOpen(friend.username))}>发消息</button>
              <button type="button" disabled={busy} onClick={() => setRemoveID(friend.id)}>解除好友</button>
            </>}
          </div>
        </li>)}</ul>
      </>}
      {view === "requests" && !loading && <>
        <h3>收到的申请</h3>
        {incoming.length === 0 && <p className="list-note">暂无收到的申请。</p>}
        <ul>{incoming.map((request) => <li key={request.id} className="friend-row">
          <FriendName profile={request.peer} />
          <div className="friend-actions">
            <button type="button" disabled={busy} onClick={() => resolve(request, "accepted")}>同意</button>
            <button type="button" disabled={busy} onClick={() => resolve(request, "rejected")}>拒绝</button>
          </div>
        </li>)}</ul>
        <h3>发出的申请</h3>
        {outgoing.length === 0 && <p className="list-note">暂无待处理的申请。</p>}
        <ul>{outgoing.map((request) => <li key={request.id} className="friend-row">
          <FriendName profile={request.peer} /><span>等待同意</span>
        </li>)}</ul>
      </>}
    </section>
  );
}

function FriendName({ profile }: { profile: Profile }) {
  return <span className="conversation-label"><strong>{profile.nickname}</strong><span>@{profile.username}</span></span>;
}
