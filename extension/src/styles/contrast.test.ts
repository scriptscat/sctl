/// <reference types="node" />
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

// 解析 globals.css 的 :root 与 .dark 变量块，而不是维护一份手抄的颜色表——颜色表改了
// 这里立刻能感知，不用两处同步。直接用 node:fs 读取源文件：vitest 默认把任何 .css
// 请求（包括 ?raw）的内容置空（vitest:css-disable transform），import 这个文件拿不到
// 真实内容；tsconfig 的 types 不含 "node"，所以用上面的三斜线引用只为这一个文件拉入
// Node 类型，不改 tsconfig。
const CSS_PATH = fileURLToPath(new URL("./globals.css", import.meta.url));
const HEX_RE = /^#[0-9a-fA-F]{6}$/;

function extractBlock(css: string, selector: string): string {
  const match = new RegExp(`${selector}\\s*\\{([^}]*)\\}`).exec(css);
  if (!match) {
    throw new Error(`未在 globals.css 中找到 ${selector} 块`);
  }
  return match[1];
}

function extractVars(block: string): Map<string, string> {
  const vars = new Map<string, string>();
  for (const m of block.matchAll(/--([a-z-]+):\s*(#[0-9a-fA-F]{6})\s*;/g)) {
    vars.set(m[1], m[2].toLowerCase());
  }
  return vars;
}

function srgbToLinear(channel: number): number {
  const c = channel / 255;
  return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
}

function relativeLuminance(hex: string): number {
  if (!HEX_RE.test(hex)) {
    throw new Error(`不是 6 位十六进制颜色: ${hex}`);
  }
  const r = srgbToLinear(parseInt(hex.slice(1, 3), 16));
  const g = srgbToLinear(parseInt(hex.slice(3, 5), 16));
  const b = srgbToLinear(parseInt(hex.slice(5, 7), 16));
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

// WCAG 2.x 对比度公式：(L1+0.05)/(L2+0.05)，L1 是较亮者的相对亮度。
function contrastRatio(a: string, b: string): number {
  const la = relativeLuminance(a);
  const lb = relativeLuminance(b);
  const lighter = Math.max(la, lb);
  const darker = Math.min(la, lb);
  return (lighter + 0.05) / (darker + 0.05);
}

function resolveVars(css: string, selector: string, base?: Map<string, string>): Map<string, string> {
  const own = extractVars(extractBlock(css, selector));
  // .dark 通过层叠覆盖 :root 的变量，未重新声明的（如 --radius 之外的十六进制变量目前没有这种情况，
  // 但为了忠实反映真实层叠语义仍在此合并）沿用 :root 的值。
  return base ? new Map([...base, ...own]) : own;
}

const css = readFileSync(CSS_PATH, "utf8");
const light = resolveVars(css, ":root");
const dark = resolveVars(css, "\\.dark", light);

function get(vars: Map<string, string>, name: string): string {
  const value = vars.get(name);
  if (!value) {
    throw new Error(`缺少 CSS 变量 --${name}`);
  }
  return value;
}

// 弹窗实际用到的「文字色/背景色」组合（docs/specs「视觉规范」+「无障碍」：
// 白色或带色的文字与背景的对比度不低于 4.5:1）。
const TEXT_ON_SURFACE_PAIRS: Array<{ name: string; text: string; surface: string }> = [
  { name: "primary text", text: "primary", surface: "background" },
  { name: "primary text", text: "primary", surface: "surface" },
  { name: "success text", text: "ok", surface: "background" },
  { name: "success text", text: "ok", surface: "surface" },
  { name: "warning text", text: "warn", surface: "background" },
  { name: "warning text", text: "warn", surface: "surface" },
  { name: "danger text", text: "bad", surface: "background" },
  { name: "danger text", text: "bad", surface: "surface" },
  { name: "body text", text: "foreground", surface: "background" },
  { name: "body text", text: "foreground", surface: "surface" },
  { name: "muted text", text: "muted-foreground", surface: "background" },
  { name: "muted text", text: "muted-foreground", surface: "surface" },
];

// 白字按钮底色（default 按钮用 --primary-foreground/--primary；忘记确认用 --bad-foreground/--bad）。
// 悬停时按钮文字仍在，所以悬停底色同样受 4.5:1 约束。
const FILL_PAIRS: Array<{ name: string; foreground: string; fill: string }> = [
  { name: "primary button fill", foreground: "primary-foreground", fill: "primary" },
  { name: "primary button hover fill", foreground: "primary-foreground", fill: "primary-hover" },
  { name: "danger button fill", foreground: "bad-foreground", fill: "bad" },
  { name: "danger button hover fill", foreground: "bad-foreground", fill: "bad-hover" },
];

// 提示框（Note、弹窗的待审批入口）的底色是 color-mix(in oklch, <色调>, transparent 91%)，即 9% 不透明的色调
// 叠在页面背景上；审批窗口的撤销提示、防诱导提示和失败说明都是正文色或次要文字色写在这种底色上。
const TINT_ALPHA = 0.09;

function blend(tone: string, alpha: number, base: string): string {
  const channel = (hex: string, i: number) => parseInt(hex.slice(1 + i * 2, 3 + i * 2), 16);
  const mixed = [0, 1, 2].map((i) => Math.round(channel(tone, i) * alpha + channel(base, i) * (1 - alpha)));
  return `#${mixed.map((v) => v.toString(16).padStart(2, "0")).join("")}`;
}

const TEXT_ON_TINT_PAIRS: Array<{ name: string; text: string; tint: string }> = [
  { name: "body text", text: "foreground", tint: "warn" },
  { name: "muted text", text: "muted-foreground", tint: "warn" },
  { name: "body text", text: "foreground", tint: "bad" },
  { name: "muted text", text: "muted-foreground", tint: "bad" },
];

describe.each([
  ["light", light],
  ["dark", dark],
])("popup colour contrast (%s)", (_mode, vars) => {
  it.each(TEXT_ON_TINT_PAIRS)("$name on the $tint notice tint is >= 4.5:1", ({ text, tint }) => {
    const surface = blend(get(vars, tint), TINT_ALPHA, get(vars, "background"));
    expect(contrastRatio(get(vars, text), surface)).toBeGreaterThanOrEqual(4.5);
  });

  it.each(TEXT_ON_SURFACE_PAIRS)("$name on $surface is >= 4.5:1", ({ text, surface }) => {
    const ratio = contrastRatio(get(vars, text), get(vars, surface));
    expect(ratio).toBeGreaterThanOrEqual(4.5);
  });

  it.each(FILL_PAIRS)("$name is >= 4.5:1", ({ foreground, fill }) => {
    const ratio = contrastRatio(get(vars, foreground), get(vars, fill));
    expect(ratio).toBeGreaterThanOrEqual(4.5);
  });

  // shadcn 组件的错误态（aria-invalid 边框）读 --destructive；spec 的色表只有一个危险色。
  it("uses the spec danger colour for invalid-field states", () => {
    expect(get(vars, "destructive")).toBe(get(vars, "bad"));
  });
});

describe("brand decorative token", () => {
  // 品牌天蓝只用于不承载文字的装饰元素，不受 4.5:1 约束，但数值必须钉在 spec 上
  // （docs/specs「视觉规范」表：品牌装饰色浅色 #1296DB / 深色 #3AA9E6）。
  it("keeps the spec value in light mode", () => {
    expect(get(light, "brand")).toBe("#1296db");
  });

  it("keeps the spec value in dark mode", () => {
    expect(get(dark, "brand")).toBe("#3aa9e6");
  });
});
