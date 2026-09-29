import { describe, expect, it } from "vitest";
import { HandlerRegistry } from "@/background/registry";
import { registerHandlers } from "@/handlers";
import type { ApprovalKind } from "@/shared/approvals";
import { APPROVAL_KINDS } from "./kinds";

describe("approval kinds", () => {
  it("gives the window a way to carry out exactly the kinds the service worker leaves to it", () => {
    const registry = new HandlerRegistry();
    registerHandlers(registry);

    for (const kind of Object.keys(APPROVAL_KINDS) as ApprovalKind[]) {
      expect([kind, APPROVAL_KINDS[kind].inWindow !== undefined]).toEqual([kind, registry.executesInWindow(kind)]);
    }
  });
});
