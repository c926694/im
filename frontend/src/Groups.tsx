import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import type { ChatAPI, Group, Profile } from "./api";
import { Avatar } from "./visuals";
import { Dialog } from "./Dialog";

const roleLabels = { owner: "群主", admin: "管理员", member: "成员" } as const;

function FriendPicker({ api, excluded, selected, onChange, limit, disabled }: {
  api: ChatAPI;
  excluded?: Set<string>;
  selected: string[];
  onChange: (ids: string[]) => void;
  limit: number;
  disabled: boolean;
}) {
  const [friends, setFriends] = useState<Profile[] | null>(null);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setError("");
    api.friends(controller.signal).then(setFriends).catch((error: Error) => {
      if (!controller.signal.aborted) setError(error.message);
    });
    return () => controller.abort();
  }, [api, attempt]);

  if (error) return <div className="feedback" role="alert"><p>{error}</p>
    <button type="button" onClick={() => setAttempt((value) => value + 1)}>重新加载好友</button>
  </div>;
  if (!friends) return <p className="hint" role="status">正在加载好友…</p>;
  const choices = friends.filter((friend) => !excluded?.has(friend.id));
  if (choices.length === 0) return <p className="hint">暂无可邀请的好友</p>;
  return <fieldset className="friend-picker" disabled={disabled}>
    <legend>选择好友（{selected.length}/{limit}）</legend>
    <div className="friend-picker-list">
      {choices.map((friend) => {
        const checked = selected.includes(friend.id);
        return <label key={friend.id}>
          <input type="checkbox" checked={checked} disabled={!checked && selected.length >= limit} onChange={() => {
            onChange(checked ? selected.filter((id) => id !== friend.id) : [...selected, friend.id]);
          }} />
          <Avatar name={friend.nickname} /><span>{friend.nickname} <small>@{friend.username}</small></span>
        </label>;
      })}
    </div>
  </fieldset>;
}

export function GroupCreate({ api, onClose, onCreated }: { api: ChatAPI; onClose: () => void; onCreated: (id: string) => Promise<void> }) {
  const [name, setName] = useState("");
  const [selected, setSelected] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      const { id } = await api.createGroup(name, selected);
      await onCreated(id);
    } catch (error) {
      setError((error as Error).message);
      setBusy(false);
    }
  }

  return <Dialog title="创建群聊" busy={busy} onClose={onClose}>
    <form className="group-form" onSubmit={submit}>
      <label>群名<input value={name} onChange={(event) => setName(event.target.value)} required maxLength={64} disabled={busy} /></label>
      <FriendPicker api={api} selected={selected} onChange={setSelected} limit={199} disabled={busy} />
      {error && <p className="error" role="alert">{error}</p>}
      <div className="group-actions"><button type="button" disabled={busy} onClick={onClose}>取消</button>
        <button type="submit" className="primary" disabled={busy || !name.trim()}>创建</button></div>
    </form>
  </Dialog>;
}

export function GroupPanel({ api, id, userID, revision, epoch, onClose, onChanged, onLeft }: {
  api: ChatAPI;
  id: string;
  userID: string;
  revision: number;
  epoch: number;
  onClose: () => void;
  onChanged: () => void;
  onLeft: () => void;
}) {
  const [group, setGroup] = useState<Group | null>(null);
  const [loadError, setLoadError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [name, setName] = useState("");
  const [inviting, setInviting] = useState(false);
  const [selected, setSelected] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    const controller = new AbortController();
    setLoadError("");
    api.groupDetails(id, controller.signal).then((data) => {
      setGroup(data);
      setName(data.name);
    }).catch((error: Error) => {
      if (!controller.signal.aborted) setLoadError(error.message);
    });
    return () => controller.abort();
  }, [api, id, revision, epoch, attempt]);

  async function change(operation: () => Promise<unknown>, done = onChanged) {
    setError("");
    setBusy(true);
    try {
      await operation();
      done();
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const role = group?.members.find((member) => member.id === userID)?.role;
  const ownerMustTransfer = role === "owner" && group!.members.length > 1;
  return <Dialog title="群详情" busy={busy} onClose={onClose}>
    {loadError ? <div className="feedback" role="alert"><p>{loadError}</p>
      <button type="button" onClick={() => setAttempt((value) => value + 1)}>重新加载群详情</button>
    </div> : !group ? <p className="hint" role="status">正在加载群详情…</p> : <div className="group-form">
      {role === "owner" ? <form className="group-rename" onSubmit={(event) => {
        event.preventDefault();
        void change(() => api.renameGroup(id, name));
      }}>
        <label>群名<input value={name} onChange={(event) => setName(event.target.value)} disabled={busy} required maxLength={64} /></label>
        <button type="submit" disabled={busy || !name.trim() || name.trim() === group.name}>保存群名</button>
      </form> : <p className="group-name">{group.name}</p>}
      <div className="group-members-heading"><h3>群成员（{group.members.length}/200）</h3>
        {(role === "owner" || role === "admin") && <button type="button" disabled={busy || (!inviting && group.members.length >= 200)} onClick={() => {
          setSelected([]);
          setInviting(!inviting);
        }}>{inviting ? "取消邀请" : "邀请成员"}</button>}
      </div>
      {inviting && (role === "owner" || role === "admin") && <form className="group-form" onSubmit={(event) => {
        event.preventDefault();
        void change(() => api.inviteGroupMembers(id, selected), () => {
          setSelected([]);
          setInviting(false);
          onChanged();
        });
      }}>
        <FriendPicker api={api} excluded={new Set(group.members.map((member) => member.id))} selected={selected}
          onChange={setSelected} limit={200 - group.members.length} disabled={busy} />
        <button type="submit" className="primary" disabled={busy || selected.length === 0}>确认邀请</button>
      </form>}
      <ul className="group-members">
        {group.members.map((member) => <li key={member.id}>
          <Avatar name={member.nickname} />
          <div className="group-member-name"><strong>{member.nickname}</strong><small>@{member.username} · {roleLabels[member.role]}</small></div>
          {member.id !== userID && <div className="group-actions">
            {role === "owner" && <>
              <button type="button" disabled={busy} onClick={() => void change(() => api.setGroupRole(id, member.id, member.role === "admin" ? "member" : "admin"))}>
                {member.role === "admin" ? "取消管理员" : "设为管理员"}
              </button>
              <button type="button" disabled={busy} onClick={() => {
                if (window.confirm(`将群主身份转让给 ${member.nickname}？`)) void change(() => api.transferGroup(id, member.id));
              }}>转让群主</button>
            </>}
            {(role === "owner" || (role === "admin" && member.role === "member")) && <button type="button" disabled={busy} onClick={() => {
              if (window.confirm(`将 ${member.nickname} 移出群聊？`)) void change(() => api.removeGroupMember(id, member.id));
            }}>移除</button>}
          </div>}
        </li>)}
      </ul>
      {error && <p className="error" role="alert">{error}</p>}
      <div className="group-actions group-exit">
        {ownerMustTransfer && <p className="hint">转让群主身份后可以退出。</p>}
        <button type="button" disabled={busy || ownerMustTransfer} onClick={() => {
          const prompt = role === "owner" ? "退出并关闭群聊？" : "退出群聊？";
          if (window.confirm(prompt)) void change(() => api.leaveGroup(id), onLeft);
        }}>退出群聊</button>
      </div>
    </div>}
  </Dialog>;
}
