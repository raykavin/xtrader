import { describe, expect, it } from "vitest";
import type { AgentStatus } from "../api/types";
import { agentLabel, pickAgent } from "./agents";

const crossover: AgentStatus = {
  name: "crossover",
  kind: "trend",
  address: "localhost:5555",
  online: false,
};
const ninjaagent: AgentStatus = {
  name: "ninjaagent",
  kind: "llm",
  address: "localhost:5556",
  online: true,
};
const agents = [crossover, ninjaagent];

describe("pickAgent", () => {
  it("keeps the wanted agent while configured, even offline", () => {
    expect(pickAgent(agents, "crossover")).toBe("crossover");
    expect(pickAgent(agents, "ninjaagent")).toBe("ninjaagent");
  });

  it("falls back to the first online, else the first", () => {
    expect(pickAgent(agents, null)).toBe("ninjaagent");
    expect(pickAgent(agents, "removed")).toBe("ninjaagent");
    expect(pickAgent([{ ...ninjaagent, online: false }, crossover], null)).toBe("ninjaagent");
    expect(pickAgent([], "ninjaagent")).toBeNull();
  });
});

describe("agentLabel", () => {
  it("tells the offline agents", () => {
    expect(agentLabel(crossover)).toBe("crossover (offline)");
    expect(agentLabel(ninjaagent)).toBe("ninjaagent");
  });
});
