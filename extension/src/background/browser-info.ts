import { type BrandInfo, detectProduct } from "@/shared/identity";
import type { BrowserInfo } from "./controller";

// navigator.userAgentData 只在 Chromium 系浏览器里存在，TypeScript 的 DOM 库没有收录它。
interface UserAgentData {
  brands: BrandInfo[];
  getHighEntropyValues(hints: string[]): Promise<{ fullVersionList?: BrandInfo[] }>;
}

export async function readBrowserInfo(): Promise<BrowserInfo> {
  const data = (navigator as Navigator & { userAgentData?: UserAgentData }).userAgentData;
  const product = detectProduct(data?.brands);
  const full = data ? await data.getHighEntropyValues(["fullVersionList"]) : {};
  const version =
    full.fullVersionList?.find((b) => b.brand === product.brand)?.version ??
    data?.brands.find((b) => b.brand === product.brand)?.version ??
    "";
  return { product, version };
}
