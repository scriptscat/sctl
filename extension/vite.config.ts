import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig, type Plugin } from "vite";

const src = resolve(import.meta.dirname, "src");

// Chrome 的 manifest version 只接受 1–4 段纯数字，预发布后缀（如 0.2.0-rc.1）只能放进 version_name。
function manifestVersion(release: string): { version: string; version_name?: string } {
  const match = /^(\d+\.\d+\.\d+)(-[0-9A-Za-z.-]+)?$/.exec(release);
  if (!match) {
    throw new Error(`SCTL_EXTENSION_VERSION must look like 1.2.3 or 1.2.3-rc.1, got ${release}`);
  }
  return match[2] ? { version: match[1], version_name: release } : { version: match[1] };
}

// 版本号与 sctl 发布版本一致：发布流程通过 SCTL_EXTENSION_VERSION 传入 tag 版本，本地构建用 package.json 的版本。
function manifest(): Plugin {
  return {
    name: "sctl-manifest",
    generateBundle() {
      const pkg = JSON.parse(readFileSync(resolve(import.meta.dirname, "package.json"), "utf8")) as {
        version: string;
      };
      const source = JSON.parse(readFileSync(resolve(src, "manifest.json"), "utf8")) as Record<string, unknown>;
      const release = process.env.SCTL_EXTENSION_VERSION || pkg.version;
      this.emitFile({
        type: "asset",
        fileName: "manifest.json",
        source: `${JSON.stringify({ ...source, ...manifestVersion(release) }, null, 2)}\n`,
      });
    },
  };
}

export default defineConfig({
  root: src,
  publicDir: resolve(import.meta.dirname, "public"),
  base: "/",
  plugins: [react(), tailwindcss(), manifest()],
  resolve: { alias: { "@": src } },
  build: {
    outDir: resolve(import.meta.dirname, "dist"),
    emptyOutDir: true,
    // service worker 没有 document，Vite 的 modulepreload polyfill 会在其中报错。
    modulePreload: { polyfill: false },
    rollupOptions: {
      input: {
        background: resolve(src, "background/index.ts"),
        popup: resolve(src, "popup/index.html"),
        offscreen: resolve(src, "offscreen/index.html"),
        // 路径与 shared/approvals.ts 的 APPROVAL_PAGE 一致：service worker 按它打开审批窗口。
        approval: resolve(src, "approval/index.html"),
      },
      output: {
        // manifest 按固定路径引用 service worker，其余入口保留内容哈希。
        entryFileNames: (chunk) => (chunk.name === "background" ? "background.js" : "assets/[name]-[hash].js"),
      },
    },
  },
});
