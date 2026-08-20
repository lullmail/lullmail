import { navigate } from "../lib/router";

/** With no mailbox connected, every bucket is empty and the app reads as
    "nothing here" rather than "nothing set up yet". This is the first thing a
    new user should ever see. */
export function Welcome() {
  return (
    <div class="welcome">
      <div class="page-kicker">Welcome</div>
      <h1 class="page-title">Let's get your mail in here.</h1>
      <p class="welcome-lede">
        Connect a mailbox and email-soft mirrors it — your mail stays with your provider,
        this is a better way to read it.
      </p>

      <button class="btn btn-accent welcome-cta" type="button" onClick={() => navigate("/settings/accounts")}>
        Connect a mailbox
      </button>

      <div class="welcome-how">
        <div class="welcome-step">
          <div class="welcome-num">1</div>
          <div>
            <strong>Everyone new lands in the Screener.</strong>
            <p>
              Nobody reaches you until you say so. You decide once per sender — Inbox for people,
              Reading for newsletters, Receipts for confirmations — and every message they ever
              send follows that rule. Change your mind whenever; nothing is permanent.
            </p>
          </div>
        </div>
        <div class="welcome-step">
          <div class="welcome-num">2</div>
          <div>
            <strong>The Inbox is only what you said yes to.</strong>
            <p>
              Newsletters and confirmations never touch it. They wait in Reading and Receipts
              for whenever you feel like looking.
            </p>
          </div>
        </div>
        <div class="welcome-step">
          <div class="welcome-num">3</div>
          <div>
            <strong>Today is the whole picture.</strong>
            <p>
              One page: what needs an answer, who you're waiting on, and who's new. When it's
              empty, you're actually done.
            </p>
          </div>
        </div>
      </div>
    </div>
  );
}
