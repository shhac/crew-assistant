import { useState, type FormEvent } from "react";
import { ErrorNotice, Icon, Mark } from "./ui";
import { api, errorText } from "./api";

export function Login({
  onSuccess,
  initialError = "",
}: {
  onSuccess: () => Promise<void>;
  initialError?: string;
}) {
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(initialError);
  async function login(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api("/api/session", {
        method: "POST",
        body: JSON.stringify({ token: token.trim() }),
      });
      setToken("");
      await onSuccess();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="login-screen">
      <div className="login-art" aria-hidden="true">
        <div className="orbit one" />
        <div className="orbit two" />
        <div className="orbit three" />
        <Mark />
      </div>
      <main className="login-card">
        <p className="eyebrow">YOUR PRIVATE WORKSPACE</p>
        <h1>A little less to carry.</h1>
        <p>
          Connect to your assistant to see the work clearly and keep it moving.
        </p>
        <form onSubmit={login}>
          <label htmlFor="login-token">
            Dashboard access code
            <input
              id="login-token"
              type="password"
              autoComplete="off"
              value={token}
              onChange={(e) => setToken(e.target.value)}
              autoFocus
              required
            />
          </label>
          <div className="field-hint login-help">
            <p>
              Run this on the computer hosting your assistant to get a code:
            </p>
            <pre>
              <code>crew-assistant dashboard open --print</code>
            </pre>
            <p>
              The code expires after five minutes and works once. It is issued
              on demand, not stored for you to look up.
            </p>
          </div>
          <ErrorNotice error={error} />
          <button
            type="submit"
            className="button primary"
            disabled={busy || !token.trim()}
          >
            {busy ? "Connecting…" : "Open workspace"}
            <Icon name="Arrow" size={16} />
          </button>
        </form>
        <span className="login-private">
          <Icon name="Lock" size={13} /> Crew Assistant · Owner access
        </span>
      </main>
    </div>
  );
}
