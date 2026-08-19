import { defineConfig } from "vite";
import preact from "@preact/preset-vite";

// NEUTRON_BUGS N7: with `--preset static` and islands present, the CLI takes the
// "build a client bundle" branch (appRouteCount > 0 || hasIslands) but nothing
// supplies a Rollup entry — neutronPlugin only provides one for app routes — so
// Vite falls back to index.html and the build dies with UNRESOLVED_ENTRY.
// Naming the islands entry ourselves is the entry that pass should have had.
// The CLI's own islands pass sets the same input, so this is not a second build
// of anything new.
//
// The CLI injects its own fully-configured neutronPlugin for both passes; adding
// a second unconfigured instance here is not needed.
export default defineConfig({
  plugins: [preact()],
  build: {
    rollupOptions: {
      input: { "neutron-islands": "@neutron-build/core/client/islands-entry" },
    },
  },
});
