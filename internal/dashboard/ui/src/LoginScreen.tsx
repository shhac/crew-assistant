import { useState, type FormEvent } from "react";
import { ErrorNotice } from "./ui";
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
    <div className="pairing">
      <main className="pairing-card card">
        <span className="brand-mark" aria-hidden="true">
          C
        </span>
        <h1>Pair this browser</h1>
        <p className="soft">On the computer running crew-assistant, run:</p>
        <pre className="pairing-command">
          <code>crew-assistant dashboard open --print</code>
        </pre>
        <form className="form" onSubmit={login}>
          <label htmlFor="login-token">
            Pairing code
            <input
              id="login-token"
              className="field"
              type="password"
              autoComplete="one-time-code"
              value={token}
              onChange={(e) => setToken(e.target.value)}
              autoFocus
              required
            />
            <span className="hint">
              A code works once and expires after five minutes.
            </span>
          </label>
          <ErrorNotice error={error} />
          <button
            type="submit"
            className="btn btn-primary"
            disabled={busy || !token.trim()}
          >
            Pair browser
          </button>
        </form>
      </main>
    </div>
  );
}
