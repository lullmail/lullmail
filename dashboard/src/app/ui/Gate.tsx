import { useState } from "preact/hooks";
import { authApi, authStatus, refreshAuth, type AuthStatus } from "../lib/api";
import { createPasskey, getPasskey } from "../lib/passkeys";

type Mode = "passkey" | "recovery" | "totp";

function GateFan() {
  return (
    <div class="gate-fan" aria-hidden="true">
      <span class="gate-fan-card gate-fan-mail">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">
          <rect x="3" y="5" width="18" height="14" rx="2" />
          <path d="m4 7 8 6 8-6" />
        </svg>
      </span>
      <span class="gate-fan-card gate-fan-calendar">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">
          <rect x="3" y="4" width="18" height="17" rx="2" />
          <path d="M7 2v4M17 2v4M3 9h18" />
          <path d="M8 13h2M14 13h2M8 17h2M14 17h2" />
        </svg>
      </span>
      <span class="gate-fan-card gate-fan-board">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">
          <rect x="3" y="4" width="18" height="16" rx="2" />
          <path d="M9 4v16M15 4v16M5.5 8h1M11.5 8h1M17.5 8h1M5.5 12h1M11.5 12h1" />
        </svg>
      </span>
    </div>
  );
}

export function Gate() {
  const status = authStatus.value;
  const [mode, setMode] = useState<Mode>("passkey");
  const [email, setEmail] = useState("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

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

  const fallback = async (ev: Event) => {
    ev.preventDefault(); setBusy(true); setError("");
    try {
      await authApi(mode === "totp" ? "/auth/totp" : "/auth/recovery", { body: { email, code } });
      await refreshAuth();
    } catch (e) { setError(e instanceof Error ? e.message : "Sign-in failed"); }
    finally { setBusy(false); }
  };

  if (status && !status.configured) return <SetupWizard status={status} />;

  return (
    <div class="gate-wrap">
      <section class="gate" aria-labelledby="gate-title">
        <GateFan />
        <div class="gate-brand">email-soft</div>
        <h1 id="gate-title">Welcome back</h1>
        <p class="gate-sub">Use a passkey to open your mailbox.</p>
        {mode === "passkey" ? (
          <>
            <button class="btn btn-accent gate-primary" type="button" disabled={busy} onClick={signIn}>
              {busy ? "Waiting for your device…" : "Continue with a passkey"}
            </button>
            <button class="gate-link" type="button" onClick={() => setMode("recovery")}>Can't use your passkey?</button>
          </>
        ) : (
          <form onSubmit={fallback}>
            <label class="sr-only" for="fallback-email">Account email</label>
            <input id="fallback-email" type="email" placeholder="Email (optional)" autocomplete="email" value={email}
              onInput={(e) => setEmail((e.target as HTMLInputElement).value)} />
            <label class="sr-only" for="fallback-code">{mode === "totp" ? "Authenticator code" : "Recovery code"}</label>
            <input id="fallback-code" inputMode={mode === "totp" ? "numeric" : "text"}
              placeholder={mode === "totp" ? "6-digit authenticator code" : "Recovery code"}
              autocomplete="one-time-code" value={code} onInput={(e) => setCode((e.target as HTMLInputElement).value)} />
            <button class="btn btn-accent" type="submit" disabled={busy || !code.trim()}>{busy ? "Checking…" : "Sign in"}</button>
            <div class="gate-switch">
              <button type="button" onClick={() => setMode(mode === "totp" ? "recovery" : "totp")}>
                Use {mode === "totp" ? "a recovery code" : "an authenticator code"}
              </button>
              <button type="button" onClick={() => setMode("passkey")}>Use a passkey instead</button>
            </div>
          </form>
        )}
        {error && <div class="gate-error" role="alert">{error}</div>}
        <p class="gate-trust">No password to remember. Recovery codes work once.</p>
      </section>
    </div>
  );
}

function SetupWizard({ status }: { status: AuthStatus }) {
  const [step, setStep] = useState(0);
  const [token, setToken] = useState("");
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [copied, setCopied] = useState(false);
  const [recoveryCodes, setRecoveryCodes] = useState<string[]>([]);

  const goTo = (next: number) => { setError(""); setStep(next); };

  const enterToken = (ev: Event) => {
    ev.preventDefault();
    if (!token.trim()) { setError("Enter the setup token to continue."); return; }
    goTo(2);
  };

  const setUp = async (ev: Event) => {
    ev.preventDefault();
    if (!name.trim()) { setError("Enter your name to continue."); return; }
    setBusy(true); setError("");
    try {
      const options = await authApi<Record<string, unknown>>("/auth/bootstrap/begin", { body: { name: name.trim() } }, token.trim());
      const credential = await createPasskey(options);
      const result = await authApi<{ recovery_codes: string[] }>("/auth/bootstrap/finish?name=" + encodeURIComponent("Primary passkey"), { body: credential }, token.trim());
      setRecoveryCodes(result.recovery_codes);
      setError("");
      setStep(3);
    } catch (e) { setError(e instanceof Error ? e.message : "Setup failed"); }
    finally { setBusy(false); }
  };

  const downloadCodes = () => {
    const today = new Date().toISOString().slice(0, 10);
    const text = ["email-soft recovery codes", "", "Owner: " + (name.trim() || "you"), "Generated: " + today, "", ...recoveryCodes, ""].join("\n");
    const url = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = "email-soft-recovery-codes.txt";
    document.body.appendChild(anchor);
    anchor.click();
    anchor.remove();
    setTimeout(() => URL.revokeObjectURL(url), 5000);
  };

  const copyAll = async () => {
    const text = recoveryCodes.join("\n");
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      const area = document.createElement("textarea");
      area.value = text;
      area.style.position = "fixed";
      area.style.opacity = "0";
      document.body.appendChild(area);
      area.select();
      document.execCommand("copy");
      area.remove();
    }
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  return (
    <div class="gate-wrap">
      <section class={"gate" + (step === 3 ? " gate-wide" : "")} aria-labelledby="setup-title">
        <GateFan />
        <div class="gate-brand">email-soft</div>
        <div class="gate-progress" role="progressbar" aria-label={`Setup, step ${step + 1} of 4`} aria-valuemin={1} aria-valuemax={4} aria-valuenow={step + 1}>
          <span>Step {step + 1} of 4</span>
          <i><b style={{ width: `${(step + 1) * 25}%` }} /></i>
        </div>
        <div class="gate-step" key={step}>
          {step === 0 && (
            <>
              <h1 id="setup-title">Set up your mailbox</h1>
              <p class="gate-sub">Create your account and passkey. It takes about a minute.</p>
              {status.detected_origin && (
                <p class="gate-origin">You'll sign in at <b>{status.detected_origin}</b>.<br />Passkeys created here only work at this address.</p>
              )}
              <button class="btn btn-accent gate-primary" type="button" onClick={() => goTo(1)}>Get started</button>
            </>
          )}
          {step === 1 && (
            <form onSubmit={enterToken}>
              <h1 id="setup-title">Enter your setup code</h1>
              <p class="gate-sub">email-soft added a one-time code to your container logs when it started. Paste it here to confirm this is your server.</p>
              {!status.bootstrap_available && <div class="gate-error">This setup code has expired. Restart the container to create a new one.</div>}
              <label class="sr-only" for="setup-token">Setup code</label>
              <input id="setup-token" type="password" placeholder="Setup code" autocomplete="off" autofocus value={token}
                onInput={(e) => setToken((e.target as HTMLInputElement).value)} />
              <p class="gate-hint">Open your platform's container logs to find it. With Docker Compose, run <code>docker compose logs app</code>. The code expires after 24 hours.</p>
              <div class="gate-nav">
                <button class="btn btn-outline" type="button" onClick={() => goTo(0)}>Back</button>
                <button class="btn btn-accent" type="submit" disabled={!status.bootstrap_available}>Continue</button>
              </div>
              {error && <div class="gate-error" role="alert">{error}</div>}
            </form>
          )}
          {step === 2 && (
            <form onSubmit={setUp}>
              <h1 id="setup-title">Who's this mailbox for?</h1>
              <p class="gate-sub">It's just you. Enter your name, then create your passkey. Your actual mailboxes (Gmail, Outlook, or IMAP) get connected inside the app right after.</p>
              <label class="sr-only" for="setup-name">Your name</label>
              <input id="setup-name" type="text" placeholder="Your name" autocomplete="name" value={name}
                onInput={(e) => setName((e.target as HTMLInputElement).value)} />
              <p class="gate-hint">You'll use this passkey whenever you sign in. Recovery codes are available if you lose access to it.</p>
              <div class="gate-nav">
                <button class="btn btn-outline" type="button" disabled={busy} onClick={() => goTo(1)}>Back</button>
                <button class="btn btn-accent" type="submit" disabled={busy || !status.bootstrap_available}>
                  {busy ? "Creating your passkey…" : "Create my passkey"}
                </button>
              </div>
              {error && <div class="gate-error" role="alert">{error}</div>}
            </form>
          )}
          {step === 3 && (
            <>
              <h1 id="setup-title">Save your recovery codes</h1>
              <p class="gate-sub">Keep these somewhere safe. Each code can sign you in once if your passkey isn't available.</p>
              <div class="recovery-grid">{recoveryCodes.map((item) => <code key={item}>{item}</code>)}</div>
              <div class="gate-code-actions">
                <button class="btn btn-outline" type="button" onClick={downloadCodes}>Download</button>
                <button class="btn btn-outline" type="button" onClick={copyAll}>{copied ? "Copied" : "Copy"}</button>
              </div>
              <button class="btn btn-accent" type="button" onClick={() => refreshAuth()}>I've saved them</button>
            </>
          )}
        </div>
        <p class="gate-trust">No password to remember. Recovery codes work once.</p>
      </section>
    </div>
  );
}
