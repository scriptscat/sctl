// sctl 写进它管理的扩展目录的标记文件（internal/pkg/browserext）：从 Release zip 手动加载的扩展没有它。
export const BUNDLE_MARKER = "sctl-bundle.json";

const DIGEST = /^[0-9a-f]{64}$/;
const SESSION_KEY = "bundle";

export interface SessionStore {
  get(keys: string[]): Promise<Record<string, unknown>>;
  set(items: Record<string, unknown>): Promise<void>;
}

// 读出标记文件记录的扩展内容摘要，在能力声明里上报，daemon 据此判断这份扩展是否落后于它内嵌的那份。
// 没有标记文件或内容不对时返回 null：这样的扩展不由 sctl 管理，daemon 不会去更新或重新加载它。
//
// 摘要必须对应正在运行的代码，而不是磁盘上的现状：目录可能在扩展运行期间被改写（sctl browsers install，或 daemon
// 写了目录但 reload 没送达），service worker 休眠后重启时读到的是新标记，运行的却仍是旧代码。所以只在扩展加载后
// 第一次读取磁盘，结果记在 storage.session 里——它在扩展重新加载或浏览器重启时清空，正好与代码的生命周期一致。
export async function readBundleDigest(
  fetchFn: (url: string) => Promise<Response>,
  url: string,
  session: SessionStore,
): Promise<string | null> {
  const cached = (await session.get([SESSION_KEY]))[SESSION_KEY];
  if (typeof cached === "string") {
    return cached === "" ? null : cached;
  }
  const digest = await readMarker(fetchFn, url);
  await session.set({ [SESSION_KEY]: digest ?? "" });
  return digest;
}

async function readMarker(fetchFn: (url: string) => Promise<Response>, url: string): Promise<string | null> {
  try {
    const response = await fetchFn(url);
    if (!response.ok) {
      return null;
    }
    const body = (await response.json()) as { digest?: unknown };
    return typeof body.digest === "string" && DIGEST.test(body.digest) ? body.digest : null;
  } catch {
    return null;
  }
}
