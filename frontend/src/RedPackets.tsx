import { useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { conversationTitle } from "./api";
import type { ChatAPI, Conversation, Message, PacketDraft, PointEntry, RedPacket } from "./api";
import { Dialog } from "./Dialog";

const dateTime = new Intl.DateTimeFormat("zh-CN", { dateStyle: "short", timeStyle: "short" });
const pointFormat = new Intl.NumberFormat("zh-CN");
const maxPoints = (1n << 63n) - 1n;
const statusLabel = { open: "可领取", claimed: "已领完", expired: "已过期，等待退款", refunded: "已退款" };
const entryLabel = { grant: "初始发放", send: "发送红包", claim: "领取红包", refund: "红包退款" };

function formatPoints(value: string | bigint) {
  return pointFormat.format(BigInt(value));
}

export function SendPacket({ api, conversation, onClose, onSent }: {
  api: ChatAPI;
  conversation: Conversation;
  onClose: () => void;
  onSent: (message: Message) => void;
}) {
  const group = conversation.kind === "group";
  const [points, setPoints] = useState("");
  const [shares, setShares] = useState("1");
  const [busy, setBusy] = useState(false);
  const [phase, setPhase] = useState<"ready" | "sending" | "sent">("ready");
  const [error, setError] = useState("");
  const [balance, setBalance] = useState<string>();
  const draft = useRef<PacketDraft | null>(null);
  const pointsPerShare = /^\d{1,19}$/.test(points) ? BigInt(points) : 0n;
  const shareCount = group && /^\d{1,3}$/.test(shares) ? Number(shares) : group ? 0 : 1;
  const total = pointsPerShare * BigInt(shareCount);
  const valid = pointsPerShare > 0n && shareCount >= 1 && shareCount <= 199 && total <= maxPoints;

  useEffect(() => {
    const controller = new AbortController();
    api.pointBalance(controller.signal).then(({ balance }) => setBalance(balance)).catch((error: Error) => {
      if (!controller.signal.aborted) setError(error.message);
    });
    return () => controller.abort();
  }, [api]);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!valid || busy) return;
    const amount = pointsPerShare.toString();
    if (draft.current?.points_per_share !== amount || draft.current.share_count !== shareCount) {
      // 相同参数重试时沿用 UUID，避免重复扣减。
      draft.current = { client_msg_id: crypto.randomUUID(), points_per_share: amount, share_count: shareCount };
    }
    setError("");
    setBusy(true);
    setPhase("sending");
    try {
      const { message } = await api.sendPacket(conversation.id, draft.current);
      setPhase("sent");
      if (!window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
        await new Promise((resolve) => window.setTimeout(resolve, 540));
      }
      onSent(message);
    } catch (error) {
      setPhase("ready");
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return <Dialog title="发红包" busy={busy} onClose={onClose} className="packet-dialog">
    <form className="group-form" onSubmit={submit}>
      <div className="packet-send-visual" data-phase={phase}>
        <div className="packet-envelope" aria-hidden="true">
          <span className="packet-envelope-flap" />
          <span className="packet-envelope-seal">福</span>
          <span className="packet-envelope-caption">积分红包</span>
          <strong key={total} className="packet-envelope-amount">{total > 0n ? formatPoints(total) : "填写积分"}</strong>
          {total > 0n && <span className="packet-envelope-unit">积分</span>}
        </div>
        <div className="packet-send-copy" role="status" aria-live="polite">
          <strong>{phase === "sent" ? "红包已发出" : phase === "sending" ? "正在传递心意" : "送出一份心意"}</strong>
          <span>{phase === "sent" ? "即将出现在聊天里" : `发送给 ${conversationTitle(conversation)}`}</span>
        </div>
      </div>
      {balance !== undefined && <p className="hint">可用余额 {formatPoints(balance)} 积分</p>}
      <label>{group ? "每份积分" : "红包积分"}<input inputMode="numeric" pattern="[0-9]+" value={points}
        maxLength={19} required disabled={busy} onChange={(event) => setPoints(event.target.value)} /></label>
      {group && <label>份数<input inputMode="numeric" pattern="[0-9]+" value={shares} maxLength={3}
        required disabled={busy} onChange={(event) => setShares(event.target.value)} /></label>}
      <p className="packet-total">合计 <strong>{formatPoints(total)}</strong> 积分</p>
      {error && <p className="error" role="alert">{error}</p>}
      <button type="submit" className="primary" disabled={!valid || busy}>{busy ? "正在发送…" : "确认发送"}</button>
    </form>
  </Dialog>;
}

export function PacketDetails({ api, id, epoch, update, onClose }: {
  api: ChatAPI;
  id: string;
  epoch: number;
  update?: { id: string };
  onClose: () => void;
}) {
  const [packet, setPacket] = useState<RedPacket>();
  const [loadError, setLoadError] = useState("");
  const [claimError, setClaimError] = useState("");
  const [busy, setBusy] = useState(false);
  const [refresh, setRefresh] = useState(0);
  const [claimedPoints, setClaimedPoints] = useState<string>();
  const [opening, setOpening] = useState(false);

  useEffect(() => {
    const controller = new AbortController();
    api.packetDetails(id, controller.signal).then((details) => {
      setLoadError("");
      setPacket(details);
    }).catch((error: Error) => {
      if (!controller.signal.aborted) {
        setPacket(undefined);
        setLoadError(error.message);
      }
    });
    return () => controller.abort();
  }, [api, id, epoch, update, refresh]);

  async function claim() {
    setBusy(true);
    setOpening(true);
    setClaimError("");
    try {
      const result = await api.claimPacket(id);
      setClaimedPoints(result.points);
    } catch (error) {
      setOpening(false);
      setClaimError((error as Error).message);
    } finally {
      setRefresh((value) => value + 1);
      setBusy(false);
    }
  }

  const received = claimedPoints ?? packet?.my_claim?.points;
  return <Dialog title="红包详情" busy={busy} onClose={onClose} className="packet-dialog">
    {!packet && !loadError && <p role="status">正在查询红包…</p>}
    {packet && <div className="group-form packet-details">
      <div className={`packet-open-visual${opening || received ? " is-open" : ""}`}>
        <div className="packet-open-envelope" aria-hidden="true">
          <span className="packet-open-flap" />
          <span className="packet-open-seal">福</span>
          {received ? <strong className="packet-reveal-amount">{formatPoints(received)}</strong>
            : <strong className="packet-open-title">积分红包</strong>}
          <span className="packet-open-unit">{received ? "积分已入账" : packet.can_claim ? "恭喜发财，大吉大利" : statusLabel[packet.status]}</span>
        </div>
      </div>
      <p className="packet-total"><strong>{formatPoints(packet.points_per_share)}</strong> 积分 / 份</p>
      <p role="status">{statusLabel[packet.status]} · 剩余 {packet.remaining} / {packet.share_count} 份</p>
      <p className="hint">有效期至 {dateTime.format(new Date(packet.expires_at))}</p>
      {received && <p className="packet-received" role="status">已领取 {formatPoints(received)} 积分</p>}
      {!received && packet.can_claim && <button type="button" className="primary" disabled={busy}
        onClick={() => void claim()}>{busy ? "正在打开…" : "开红包"}</button>}
    </div>}
    {claimError && <p className="error" role="alert">{claimError}</p>}
    {loadError && <div className="feedback" role="alert"><p>{loadError}</p>
      <button type="button" onClick={() => {
        setLoadError("");
        setRefresh((value) => value + 1);
      }}>重新查询</button>
    </div>}
  </Dialog>;
}

export function Points({ api, epoch, update }: { api: ChatAPI; epoch: number; update?: { id: string } }) {
  const [open, setOpen] = useState(false);
  const [snapshot, setSnapshot] = useState<{ balance: string; entries: PointEntry[] }>();
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const request = useRef(0);

  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    ++request.current;
    setLoading(true);
    setError("");
    Promise.all([api.pointBalance(controller.signal), api.pointLedger(undefined, controller.signal)])
      .then(([{ balance }, entries]) => {
        setSnapshot({ balance, entries });
        setHasMore(entries.length === 50);
      }).catch((error: Error) => {
        if (!controller.signal.aborted) setError(error.message);
      }).finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [api, epoch, open, attempt, update]);

  async function loadMore() {
    if (!snapshot) return;
    const current = ++request.current;
    setLoading(true);
    setError("");
    try {
      const entries = await api.pointLedger(snapshot.entries.at(-1)?.id);
      if (request.current !== current) return;
      setHasMore(entries.length === 50);
      setSnapshot({ ...snapshot, entries: [...snapshot.entries, ...entries] });
    } catch (error) {
      if (request.current === current) setError((error as Error).message);
    } finally {
      if (request.current === current) setLoading(false);
    }
  }

  return <div className="points-entry">
    <button type="button" onClick={() => setOpen(true)}>积分</button>
    {open && <Dialog title="我的积分" busy={false} onClose={() => { ++request.current; setOpen(false); }}>
      {!snapshot && loading && <p role="status">正在查询积分…</p>}
      {snapshot && <div className="group-form">
        <p className="packet-total">可用余额 <strong>{formatPoints(snapshot.balance)}</strong> 积分</p>
        <h3 className="points-ledger-title">变动记录</h3>
        <ul className="points-ledger">
          {snapshot.entries.map((entry) => <li key={entry.id}>
            <span>{entryLabel[entry.kind]}<small>{dateTime.format(new Date(entry.created_at))}</small></span>
            <span><strong>{BigInt(entry.delta) > 0n ? "+" : ""}{formatPoints(entry.delta)}</strong>
              <small>余额 {formatPoints(entry.balance_after)}</small></span>
          </li>)}
        </ul>
        {hasMore && <button type="button" disabled={loading} onClick={() => void loadMore()}>加载更多记录</button>}
      </div>}
      {error && <div className="feedback" role="alert"><p>{error}</p>
        <button type="button" onClick={() => setAttempt((value) => value + 1)}>重新查询</button>
      </div>}
    </Dialog>}
  </div>;
}
