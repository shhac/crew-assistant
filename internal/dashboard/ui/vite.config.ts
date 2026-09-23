/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
export default defineConfig({
  plugins: [react()],
  base: "/",
  build: {
    outDir: "../assets",
    emptyOutDir: true,
    assetsDir: "generated",
    sourcemap: false,
  },
  server: { proxy: { "/api": "http://127.0.0.1:8340" } },
  // jsdom rendering on a busy machine can outlast the default five seconds;
  // a slow run is not a failing one.
  test: { testTimeout: 15000 },
});
