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
  // Stylesheets are left out of tests, except those read for layout contract tests.
  test: {
    testTimeout: 15000,
    setupFiles: ["./src/testSetup.ts"],
    css: { include: [/styles\/(request|chat|base|tokens|board)\.css/] },
  },
});
