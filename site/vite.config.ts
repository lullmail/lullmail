import { fileURLToPath } from "node:url";
import preact from "@preact/preset-vite";
import { defineConfig, type Plugin } from "vite";

function serveBrowserEntry(): Plugin {
  return {
    name: "lullmail:browser-entry",
    configureServer(server) {
      server.middlewares.use((request, _response, next) => {
        if (request.url?.split("?", 1)[0] === "/assets/site.js") {
          request.url = "/src/scripts/site.ts";
        }
        next();
      });
    },
  };
}

export default defineConfig(({ mode }) => ({
  plugins: [preact(), serveBrowserEntry()],
  ...(mode === "browser"
    ? {
        build: {
          outDir: "dist",
          emptyOutDir: false,
          copyPublicDir: false,
          rollupOptions: {
            input: fileURLToPath(new URL("./src/scripts/site.ts", import.meta.url)),
            output: {
              entryFileNames: "assets/site.js",
              chunkFileNames: "assets/[name]-[hash].js",
              assetFileNames: "assets/[name]-[hash][extname]",
            },
          },
        },
      }
    : {}),
}));
