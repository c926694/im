import { useCallback, useEffect, useState } from "react";
import type { FormEvent } from "react";
import { createRoot } from "react-dom/client";
import { login, register } from "./api";
import type { Session } from "./api";
import { Workspace } from "./Workspace";
import "./styles.css";

const sessionKey = "im.session";

function AuthForm({ onLogin, notice }: { onLogin: (session: Session) => void; notice: string }) {
  const [creating, setCreating] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    const credentials = { username: String(data.get("username")), password: String(data.get("password")) };
    setBusy(true);
    setError("");
    try {
      if (creating) await register({ ...credentials, nickname: String(data.get("nickname")) });
      onLogin(await login(credentials));
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="auth-page">
      <div className="auth-layout">
        <div className="auth-identity">
          <span className="brand-mark" aria-hidden="true" />
          <h1>回声</h1>
          <p>即时通讯</p>
        </div>
        <section className="auth-panel" aria-labelledby="auth-title">
          <h2 id="auth-title">{creating ? "创建账号" : "登录"}</h2>
          <form onSubmit={submit}>
            <fieldset disabled={busy}>
              <label>用户名<input name="username" autoComplete="username" required maxLength={32} /></label>
              {creating && <label>昵称<input name="nickname" required maxLength={64} /></label>}
              <label>密码<input name="password" type="password" autoComplete={creating ? "new-password" : "current-password"} /></label>
              {creating && <p className="hint">用户名使用字母、数字或下划线。</p>}
              {notice && <p className="notice" role="status">{notice}</p>}
              {error && <p className="error" role="alert">{error}</p>}
              <button className="primary" type="submit">{busy ? "正在提交…" : creating ? "注册并登录" : "登录"}</button>
            </fieldset>
          </form>
          <button className="auth-switch" disabled={busy} type="button" onClick={() => { setCreating(!creating); setError(""); }}>
            {creating ? "已有账号，返回登录" : "没有账号，去注册"}
          </button>
        </section>
      </div>
    </main>
  );
}

function App() {
  const [session, setSession] = useState<Session | null>(() => {
    const saved = sessionStorage.getItem(sessionKey);
    if (!saved) return null;
    const value = JSON.parse(saved) as Session;
    return value.expiresAt > Date.now() ? value : null;
  });
  const [notice, setNotice] = useState("");

  const logout = useCallback(() => {
    setSession(null);
    setNotice("");
  }, []);

  const expire = useCallback(() => {
    setSession(null);
    setNotice("登录已失效，请重新登录。");
  }, []);

  useEffect(() => {
    if (!session) {
      sessionStorage.removeItem(sessionKey);
      return;
    }
    // 登录凭证仅保存在当前浏览器标签页的会话存储中。
    sessionStorage.setItem(sessionKey, JSON.stringify(session));
    const timer = window.setTimeout(expire, Math.max(0, session.expiresAt - Date.now()));
    return () => window.clearTimeout(timer);
  }, [session, expire]);

  return session
    ? <Workspace session={session} onLogout={logout} onExpired={expire} />
    : <AuthForm onLogin={setSession} notice={notice} />;
}

createRoot(document.getElementById("root")!).render(<App />);
