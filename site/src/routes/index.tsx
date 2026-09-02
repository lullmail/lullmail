const title = "Lull Mail — Less time on email";
const description = "A focused email client that screens new senders, shows what needs you today, and brings unfinished mail back at the right time.";

export const config = { mode: "static" };

export function head() {
  return {
    title,
    description,
    openGraph: {
      title,
      description,
      type: "website",
    },
  };
}

export default function Home() {
  return (
    <>
      <a class="skip" href="#main">Skip to content</a>

      <header class="site-head" data-header>
        <a class="brand" href="#top" aria-label="Lull Mail home">
          <span class="brand-mark" aria-hidden="true"><i></i></span>
          <span>Lull Mail</span>
        </a>
        <nav aria-label="Main navigation">
          <a href="#product">Product</a>
          <a href="#trust">Trust</a>
        </nav>
        <a class="preview-link" href="#availability"><i aria-hidden="true"></i>Private preview</a>
      </header>

      <main id="main">
        <section class="hero-film" id="top" data-hero-film aria-labelledby="hero-title">
          <div class="hero-stage">
            <div class="hero-poster" aria-hidden="true">
              <span class="dusk-sun"></span>
              <span class="dusk-horizon"></span>
              <span class="poster-mail mail-a"><i></i></span>
              <span class="poster-mail mail-b"><i></i></span>
              <span class="poster-mail mail-c"><i></i></span>
            </div>
            <canvas class="cinema-canvas" data-cinema aria-hidden="true"></canvas>
            <div class="hero-grain" aria-hidden="true"></div>

            <div class="hero-copy">
              <p class="overline">A focused email client for the mailbox you already use.</p>
              <h1 id="hero-title"><span>Less time</span><em>on email.</em></h1>
              <p class="hero-lede">Choose who gets through. See what actually needs you. Set the rest aside until the right day.</p>
              <a class="arrow-link light" href="#product">See how it works <span aria-hidden="true">↓</span></a>
            </div>

            <div class="hero-meter" aria-hidden="true">
              <span>Incoming</span>
              <i><b></b></i>
              <span>Handled</span>
            </div>
          </div>
        </section>

        <section class="product" id="product" aria-labelledby="product-title">
          <header class="section-lead">
            <p class="overline dark">How it works</p>
            <h2 id="product-title">A short list, not an endless inbox.</h2>
            <p>Lull Mail shows you what needs a decision today. Handle it, set it aside, or leave it for later without losing track of anything.</p>
          </header>

          <div class="proof-list">
            <article class="proof">
              <div class="proof-copy">
                <span class="proof-index">01</span>
                <h3>Choose who gets through.</h3>
                <p>New senders wait for your approval. Send them to Inbox, Reading, or Receipts, or block them. Future messages follow the same choice until you change it.</p>
                <small>No more sorting the same sender again and again.</small>
              </div>

              <div class="app-frame" role="img" aria-label="The Screener showing a new sender waiting for a routing decision">
                <div class="app-topline">
                  <b>Lull Mail</b>
                  <span>Today&nbsp;&nbsp; Inbox&nbsp;&nbsp; Screener</span>
                  <i>Compose</i>
                </div>
                <div class="app-page screener-page">
                  <div class="app-heading">
                    <div><small>The Screener</small><h4>New senders wait here.</h4></div>
                    <span>2 waiting</span>
                  </div>
                  <div class="sender-row primary-sender">
                    <span class="avatar">S</span>
                    <div class="sender-copy"><b>Sarah Chen</b><span>Contract redlines before Friday</span></div>
                    <small>1 message</small>
                  </div>
                  <div class="route-row"><span>Inbox</span><span>Reading</span><span>Receipts</span><span>Block</span></div>
                  <div class="sender-row quiet-sender">
                    <span class="avatar quiet">N</span>
                    <div class="sender-copy"><b>Nucleon Weekly</b><span>Issue 42: the patient machine</span></div>
                    <small>2 messages</small>
                  </div>
                </div>
              </div>
            </article>

            <article class="proof reverse">
              <div class="proof-copy">
                <span class="proof-index">02</span>
                <h3>Know what needs you.</h3>
                <p>Today brings together the messages that need a reply, the people you are waiting on, and anything else worth seeing. When you are done, it is done.</p>
                <small>No hidden queue asking for one more refresh.</small>
              </div>

              <div class="app-frame today-frame" role="img" aria-label="The Today briefing showing two messages that need attention and one person being waited on">
                <div class="app-topline">
                  <b>Lull Mail</b>
                  <span>Today&nbsp;&nbsp; Inbox&nbsp;&nbsp; Screener</span>
                  <i>Compose</i>
                </div>
                <div class="app-page today-page">
                  <div class="today-heading">
                    <small>Today</small>
                    <h4>Tuesday, August 25</h4>
                    <p>2 things need you. That's everything for today.</p>
                  </div>
                  <div class="surface-nav"><span>Board</span><span>Calendar</span><span>Notes</span></div>
                  <div class="brief-title"><b>Needs you</b><span>2</span></div>
                  <div class="brief-row unread"><i></i><div><b>Sarah Chen</b><strong>Contract redlines before Friday</strong><p>I left two comments on the final section...</p></div><time>9:12</time></div>
                  <div class="brief-row"><i></i><div><b>Micah Evans</b><strong>Are we still on for the coast?</strong></div><time>Yesterday</time></div>
                  <div class="brief-title waiting"><b>You're waiting</b><span>1</span></div>
                  <div class="waiting-row"><b>Nadia Flores</b><span>Draft review</span><em>4 days</em></div>
                </div>
              </div>
            </article>

            <article class="proof">
              <div class="proof-copy">
                <span class="proof-index">03</span>
                <h3>Come back to it later.</h3>
                <p>Set a thread aside until tomorrow, next week, or a date you choose. It leaves your Inbox and comes back when you are ready for it.</p>
                <small>Out of sight without being forgotten.</small>
              </div>

              <div class="return-frame" role="img" aria-label="A mail return timeline showing a final draft scheduled to return Friday at 9 AM">
                <div class="return-head"><span>Calendar</span><b>August</b></div>
                <div class="return-days"><span>WED<br /><b>26</b></span><span>THU<br /><b>27</b></span><span class="return-day">FRI<br /><b>28</b></span><span>SAT<br /><b>29</b></span><span>SUN<br /><b>30</b></span></div>
                <div class="return-line"><i></i><b></b></div>
                <div class="return-card">
                  <small>Back Friday at 9:00</small>
                  <strong>Final draft review</strong>
                  <span>From Nadia Flores</span>
                </div>
                <p>Until then, there is nothing to maintain.</p>
              </div>
            </article>
          </div>
        </section>

        <section class="trust" id="trust" aria-labelledby="trust-title">
          <header class="trust-intro">
            <p class="overline dark">Privacy without fine print</p>
            <h2 id="trust-title">Your mail stays yours.</h2>
            <p>Lull Mail reads your mail only to sync and organize it for you. Your messages are never used for ads, tracking, or model training.</p>
          </header>

          <div class="trust-list">
            <article><span>01</span><h3>Tracking stays out.</h3><p>Remote images are blocked by default. Common tracking links and campaign parameters are removed.</p></article>
            <article><span>02</span><h3>Your secrets stay encrypted.</h3><p>Account credentials are encrypted at rest. Passkeys, recovery codes, authenticator codes, and session revocation are built in.</p></article>
            <article><span>03</span><h3>Your mail comes with you.</h3><p>Download individual messages as EML or export a complete account in the standard mboxrd format. Delete your local data whenever you want.</p></article>
            <article><span>04</span><h3>Run it on your own server.</h3><p>The complete core is one Go binary and Postgres, MIT licensed, with no telemetry and no required cloud service.</p></article>
          </div>
        </section>

        <section class="availability" id="availability" aria-labelledby="availability-title">
          <div class="availability-copy">
            <p class="overline">Private preview</p>
            <h2 id="availability-title">Private for now.</h2>
            <p>The core product works. We are testing it with real accounts and finishing the public provider integrations before opening access more widely.</p>
          </div>
          <div class="availability-meta">
            <span>IMAP + JMAP</span><span>One owner</span><span>MIT licensed</span><span>Self-hostable</span>
          </div>
          <footer class="site-foot">
            <a class="brand" href="#top"><span class="brand-mark" aria-hidden="true"><i></i></span><span>Lull Mail</span></a>
            <p>lullmail.com · 2026</p>
            <div><a href="#product">Product</a><a href="#trust">Trust</a><a href="#top">Back to top ↑</a></div>
          </footer>
        </section>
      </main>
    </>
  );
}
