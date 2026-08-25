import { useState } from "preact/hooks";
import { authApi, authStatus, refreshAuth } from "../lib/api";
import { createPasskey, getPasskey } from "../lib/passkeys";

type Mode = "passkey" | "recovery" | "totp";

export function Gate() {
  const status = authStatus.value;
  const [mode, setMode] = useState<Mode>("passkey");
  const [token, setToken] = useState("");
  const [email, setEmail] = useState("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [recoveryCodes, setRecoveryCodes] = useState<string[] | null>(null);

  const signIn = async () => {
    setBusy(true); setError("");
    try {
      const options = await authApi<Record<string, unknown>>("/auth/login/begin", { method: "POST" });
      const credential = await getPasskey(options);
      await authApi("/auth/login/finish", { body: credential });
      await refreshAuth();
    } catch (e) { setError(e instanceof Error ? e.message : "Sign-in failed"); }
    finally { setBusy(false); }
  };

  const setUp = async (ev: Event) => {
    ev.preventDefault(); setBusy(true); setError("");
    try {
      const options = await authApi<Record<string, unknown>>("/auth/bootstrap/begin", { body: { email } }, token.trim());
      const credential = await createPasskey(options);
      const result = await authApi<{ recovery_codes: string[] }>("/auth/bootstrap/finish?name=" + encodeURIComponent("Primary passkey"), { body: credential }, token.trim());
      setRecoveryCodes(result.recovery_codes);
    } catch (e) { setError(e instanceof Error ? e.message : "Setup failed"); }
    finally { setBusy(false); }
  };

  const fallback = async (ev: Event) => {
    ev.preventDefault(); setBusy(true); setError("");
    try {
      await authApi(mode === "totp" ? "/auth/totp" : "/auth/recovery", { body: { email, code } });
      await refreshAuth();
    } catch (e) { setError(e instanceof Error ? e.message : "Sign-in failed"); }
    finally { setBusy(false); }
  };

  if (recoveryCodes) {
    return (
      <div class="gate-wrap"><section class="gate gate-wide" aria-labelledby="recovery-title">
        <div class="gate-brand">email-soft</div>
        <h1 id="recovery-title">Save your recovery codes</h1>
        <p class="gate-sub">These are shown once. Print or save them somewhere outside this device.</p>
        <div class="recovery-grid">{recoveryCodes.map((item) => <code key={item}>{item}</code>)}</div>
        <button class="btn btn-accent" type="button" onClick={() => refreshAuth()}>I saved them — continue</button>
      </section></div>
    );
  }

  const setup = status && !status.configured;
  return (
    <div class="gate-wrap">
      <section class="gate" aria-labelledby="gate-title">
        <div class="gate-mark" aria-hidden="true">✦</div>
        <div class="gate-brand">email-soft</div>
        <h1 id="gate-title">{setup ? "Make this mailbox yours" : "Welcome back"}</h1>
        <p class="gate-sub">
          {setup ? "Create a passkey. Nothing reusable is stored in your browser." : "Your mail stays behind a device-bound passkey."}
        </p>

        {setup ? (
          <form onSubmit={setUp}>
            {!status?.bootstrap_available && <div class="gate-error">Set EMAILSOFT_TOKEN on the server before first-run setup.</div>}
            <label class="sr-only" for="setup-email">Owner email</label>
            <input id="setup-email" type="email" placeholder="Owner email" autocomplete="email" value={email}
              onInput={(e) => setEmail((e.target as HTMLInputElement).value)} />
            <label class="sr-only" for="setup-token">One-time setup token</label>
            <input id="setup-token" type="password" placeholder="One-time setup token" autocomplete="off" value={token}
              onInput={(e) => setToken((e.target as HTMLInputElement).value)} />
            <button class="btn btn-accent" type="submit" disabled={busy || !token.trim() || !status?.bootstrap_available}>
              {busy ? "Creating passkey…" : "Create passkey"}
            </button>
          </form>
        ) : mode === "passkey" ? (
          <>
            <button class="btn btn-accent gate-primary" type="button" disabled={busy} onClick={signIn}>
              {busy ? "Waiting for your device…" : "Sign in with a passkey"}
            </button>
            <button class="gate-link" type="button" onClick={() => setMode("recovery")}>Use a recovery method</button>
          </>
        ) : (
          <form onSubmit={fallback}>
            <label class="sr-only" for="fallback-email">Account email</label>
            <input id="fallback-email" type="email" placeholder="Account email (optional)" autocomplete="email" value={email}
              onInput={(e) => setEmail((e.target as HTMLInputElement).value)} />
            <label class="sr-only" for="fallback-code">{mode === "totp" ? "Authenticator code" : "Recovery code"}</label>
            <input id="fallback-code" inputMode={mode === "totp" ? "numeric" : "text"}
              placeholder={mode === "totp" ? "6-digit authenticator code" : "Recovery code"}
              autocomplete="one-time-code" value={code} onInput={(e) => setCode((e.target as HTMLInputElement).value)} />
            <button class="btn btn-accent" type="submit" disabled={busy || !code.trim()}>{busy ? "Checking…" : "Continue"}</button>
            <div class="gate-switch">
              <button type="button" onClick={() => setMode(mode === "totp" ? "recovery" : "totp")}>
                Use {mode === "totp" ? "a recovery code" : "an authenticator code"}
              </button>
              <button type="button" onClick={() => setMode("passkey")}>Back to passkey</button>
            </div>
          </form>
        )}
        {error && <div class="gate-error" role="alert">{error}</div>}
        <p class="gate-trust">HttpOnly sessions · user verification required · one-use recovery</p>
      </section>
    </div>
  );
}
