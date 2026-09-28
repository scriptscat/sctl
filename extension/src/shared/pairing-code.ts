import { PAIRING_CODE } from "@/protocol/generated/protocol.generated";

const CROCKFORD = /^[0-9A-HJKMNP-TV-Z]+$/;

// 与 daemon 的 NormalizePairingCode 相同：去掉分隔符、转大写，按 Crockford 规则把 O 视为 0、I/L 视为 1，
// 使用户手输的易混字符与 daemon 生成的规范码派生出同一把密钥。无法构成合法配对码时返回 null。
export function normalizePairingCode(input: string): string | null {
  const code = input.replace(/[-\s]/g, "").toUpperCase().replace(/O/g, "0").replace(/[IL]/g, "1");
  return code.length === PAIRING_CODE.length && CROCKFORD.test(code) ? code : null;
}
