import { render } from "preact";
import App from "../src/app/App";
import { SHORTCUTS } from "../src/app/lib/keys";
import { list } from "../src/app/lib/store";

// The real application shell. The harness answers every /api request with
// synthetic data, so the router, views, reader, compose, palette and the key
// layer all run exactly as shipped.
history.replaceState(null, "", new URLSearchParams(location.search).get("path") || "/");
// The key layer reads the list from the store, which views publish to in an
// effect after first paint; the harness waits on that, not on the DOM alone.
Object.assign(window, {
  keybindsFixture: {
    shortcuts: SHORTCUTS,
    published: () => (list.value.kind === "rows" ? list.value.rows.length : list.value.kind === "senders" ? list.value.senders.length : -1),
  },
});
render(<App />, document.getElementById("app")!);
