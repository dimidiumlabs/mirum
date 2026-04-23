// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

import { readdirSync } from "node:fs";
import { resolve, parse } from "node:path";
import { defineConfig } from "vite";

import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

const locale = process.env.LOCALE ?? "en-GB";

const input = readdirSync(resolve(__dirname, "entries")).reduce(
  (acc, file) => {
    const { name } = parse(file);
    acc[name] = resolve(__dirname, "entries", file);
    return acc;
  },
  {} as Record<string, string>,
);

export default defineConfig({
  clearScreen: false,
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@/messages": resolve(__dirname, "messages", `${locale}.tsx`),
      "@": resolve(__dirname),
    },
  },
  server: {
    host: "127.0.0.1",
    origin: "http://localhost:5173",
    cors: { origin: "http://localhost:3000" },
  },
  build: {
    outDir: "../static",
    manifest: `.vite/manifest-${locale}.json`,
    emptyOutDir: false,
    rollupOptions: {
      input,
      output: {
        entryFileNames: "assets/[name].[hash].js",
        chunkFileNames: "assets/[name].[hash].js",
        assetFileNames: "assets/[name].[hash][extname]",
        manualChunks(id) {
          if (
            id.includes("node_modules/react") ||
            id.includes("node_modules/react-dom")
          ) {
            return "vendor";
          }
        },
      },
    },
  },
});
