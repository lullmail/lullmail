import { useState } from "preact/hooks";
import { authApi, authStatus, refreshAuth, type AuthStatus } from "../lib/api";
import { createPasskey, getPasskey } from "../lib/passkeys";

type Mode = "passkey" | "recovery" | "totp";

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
        <div class="gate-mark" aria-hidden="true">✦</div>
        <div class="gate-brand">email-soft</div>
        <h1 id="gate-title">Welcome back</h1>
        <p class="gate-sub">Your mail stays behind a device-bound passkey.</p>
        {mode === "passkey" ? (
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

function SetupWizard({ status }: { status: AuthStatus }) {
  const [step, setStep] = useState(0);
  const [token, setToken] = useState("");
  const [email, setEmail] = useState("");
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
    if (!email.trim().includes("@")) { setError("Enter the email that will own this mailbox."); return; }
    setBusy(true); setError("");
    try {
      const options = await authApi<Record<string, unknown>>("/auth/bootstrap/begin", { body: { email: email.trim() } }, token.trim());
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
    const text = ["email-soft recovery codes", "", "Account: " + email.trim(), "Generated: " + today, "", ...recoveryCodes, ""].join("\n");
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
        <div class="gate-mark" aria-hidden="true">✦</div>
        <div class="gate-brand">email-soft</div>
        <div class="gate-steps" aria-hidden="true">
          <span class={step === 0 ? "cur" : undefined}>01</span><i />
          <span class={step === 1 ? "cur" : undefined}>02</span><i />
          <span class={step === 2 ? "cur" : undefined}>03</span><i />
          <span class={step === 3 ? "cur" : undefined}>04</span>
        </div>
        <div class="gate-step" key={step}>
          {step === 0 && (
            <>
              <h1 id="setup-title">Make this mailbox yours</h1>
              <p class="gate-sub">Create a passkey. Nothing reusable is stored in your browser.</p>
              {status.detected_origin && (
                <p class="gate-origin">Setting up at <b>{status.detected_origin}</b><br />Your passkey will only work at this address.</p>
              )}
              <button class="btn btn-accent gate-primary" type="button" onClick={() => goTo(1)}>Continue</button>
            </>
          )}
          {step === 1 && (
            <form onSubmit={enterToken}>
              <h1 id="setup-title">The one-time token</h1>
              <p class="gate-sub">A token was printed when this server first started. Enter it to prove you can reach the container.</p>
              {!status.bootstrap_available && <div class="gate-error">Setup is unavailable — the one-time token may have expired. Restart the container to generate a fresh one.</div>}
              <label class="sr-only" for="setup-token">One-time setup token</label>
              <input id="setup-token" type="password" placeholder="One-time setup token" autocomplete="off" autofocus value={token}
                onInput={(e) => setToken((e.target as HTMLInputElement).value)} />
              <p class="gate-hint">See it again with <code>docker compose logs app</code>. It expires 24 hours after startup.</p>
              <div class="gate-nav">
                <button class="btn btn-outline" type="button" onClick={() => goTo(0)}>Back</button>
                <button class="btn btn-accent" type="submit" disabled={!status.bootstrap_available}>Continue</button>
              </div>
              {error && <div class="gate-error" role="alert">{error}</div>}
            </form>
          )}
          {step === 2 && (
            <form onSubmit={setUp}>
              <h1 id="setup-title">Owner and passkey</h1>
              <p class="gate-sub">The email names the owner. The passkey becomes the only way in.</p>
              <label class="sr-only" for="setup-email">Owner email</label>
              <input id="setup-email" type="email" placeholder="Owner email" autocomplete="email" value={email}
                onInput={(e) => setEmail((e.target as HTMLInputElement).value)} />
              <p class="gate-hint">You'll confirm with your device — fingerprint, face, or PIN.</p>
              <div class="gate-nav">
                <button class="btn btn-outline" type="button" disabled={busy} onClick={() => goTo(1)}>Back</button>
                <button class="btn btn-accent" type="submit" disabled={busy || !status.bootstrap_available}>
                  {busy ? "Creating passkey…" : "Create passkey"}
                </button>
              </div>
              {error && <div class="gate-error" role="alert">{error}</div>}
            </form>
          )}
          {step === 3 && (
            <>
              <h1 id="setup-title">Save your recovery codes</h1>
              <p class="gate-sub">These are shown once. Print or save them somewhere outside this device.</p>
              <div class="recovery-grid">{recoveryCodes.map((item) => <code key={item}>{item}</code>)}</div>
              <div class="gate-code-actions">
                <button class="btn btn-outline" type="button" onClick={downloadCodes}>Download codes</button>
                <button class="btn btn-outline" type="button" onClick={copyAll}>{copied ? "Copied" : "Copy all"}</button>
              </div>
              <button class="btn btn-accent" type="button" onClick={() => refreshAuth()}>I saved them — continue</button>
            </>
          )}
        </div>
        <p class="gate-trust">HttpOnly sessions · user verification required · one-use recovery</p>
      </section>
    </div>
  );
}
