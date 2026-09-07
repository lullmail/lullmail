import { defineConfig } from "vite";
import preact from "@preact/preset-vite";

// NEUTRON_BUGS N7: with `--preset static` and islands present, published CLI
// releases up to 0.2.2 take the app-bundle branch without a Rollup entry and
// die with UNRESOLVED_ENTRY. Fixed upstream on 2026-09-06 (islands-only sites
// now take the CSS-extraction build); this entry is harmless there and can go
// once a CLI newer than 0.2.2 is published and picked up here.
export default defineConfig({
  plugins: [preact()],
  build: {
    rollupOptions: {
      input: { "neutron-islands": "@neutron-build/core/client/islands-entry" },
    },
  },
});
