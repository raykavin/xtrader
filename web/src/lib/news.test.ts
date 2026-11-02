import { describe, expect, it } from "vitest";
import { newsState, newsStatusLabel } from "./news";

describe("the news of the agents", () => {
  it("tells the test of a server apart: answered, no token, refused, unavailable", () => {
    expect(newsStatusLabel("ok")).toMatchObject({ text: "Respondeu", tone: "on" });
    expect(newsStatusLabel("missing_server")).toMatchObject({ text: "Sem servidor", tone: "warn" });
    expect(newsStatusLabel("missing_token")).toMatchObject({ text: "Sem token", tone: "warn" });
    expect(newsStatusLabel("unauthorized")).toMatchObject({
      text: "Token recusado",
      tone: "error",
    });
    expect(newsStatusLabel("unavailable")).toMatchObject({ text: "Indisponível", tone: "error" });
    expect(newsStatusLabel("anything")).toMatchObject({ text: "Indisponível" });
  });

  it("never takes a calendar not read for no news", () => {
    expect(newsState(false, true).text).toBe("Desligadas");
    expect(newsState(false, true).hint).toContain("não significa que não haja divulgações");
    expect(newsState(true, false)).toMatchObject({ text: "Sem token", tone: "warn" });
    expect(newsState(true, true, "")).toMatchObject({ text: "Sem servidor" });
    expect(newsState(true, true, "http://mt5:22347/mcp", 0)).toMatchObject({ text: "Sem janela" });
    expect(newsState(true, true, "http://mt5:22347/mcp", 30)).toMatchObject({
      text: "Ligadas",
      tone: "on",
    });
  });
});
