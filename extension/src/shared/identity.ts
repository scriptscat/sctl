// 实例 ID 与名称的格式由 daemon 在握手边界校验（docs/protocol.md §2.1、§2.2），这里与之保持一致。
const NAME_PATTERN = /^[a-z0-9-]{1,32}$/;
const INSTANCE_ID_BYTES = 16;

export interface BrandInfo {
  brand: string;
  version: string;
}

// brand 是 userAgentData 里的品牌名（用于取版本号），slug 用于默认名称，product 作为自报的产品名发给 daemon。
export interface ProductInfo {
  brand: string;
  slug: string;
  product: string;
}

// 按优先级匹配：Edge、Opera 等的品牌列表里同时带有 Chromium，必须先认出具体品牌。
const KNOWN_PRODUCTS: readonly ProductInfo[] = [
  { brand: "Microsoft Edge", slug: "edge", product: "Edge" },
  { brand: "Opera", slug: "opera", product: "Opera" },
  { brand: "Brave", slug: "brave", product: "Brave" },
  { brand: "Google Chrome", slug: "chrome", product: "Chrome" },
];

const CHROMIUM: ProductInfo = { brand: "Chromium", slug: "chromium", product: "Chromium" };

export function newInstanceId(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(INSTANCE_ID_BYTES));
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

export function detectProduct(brands: readonly BrandInfo[] | undefined): ProductInfo {
  return KNOWN_PRODUCTS.find((known) => brands?.some((b) => b.brand === known.brand)) ?? CHROMIUM;
}

export function defaultName(product: ProductInfo, instanceId: string): string {
  return `${product.slug}-${instanceId.slice(0, 4)}`;
}

export function isValidName(name: string): boolean {
  return NAME_PATTERN.test(name);
}
