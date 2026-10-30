import { describe, expect, it } from "vitest";
import { newsState, newsStatusLabel } from "./news";

describe("the news of the agents", () => {
  it("tells the test of a server apart: answered, no token, refused, unavailable", () => {
    expect(newsStatusLabel("ok")).toMatchObject({ text: "Respondeu", tone: "on" });
    expect(newsStatusLabel("missing_token")).toMatchObject({ text: "Token ausente", tone: "warn" });
    expect(newsStatusLabel("unauthorized")).toMatchObject({
      text: "Token recusado",
      tone: "error",
    });
    expect(newsStatusLabel("unavailable")).toMatchObject({ text: "Indisponível", tone: "error" });
    expect(newsStatusLabel("anything")).toMatchObject({ text: "Indisponível" });
  });

  it("never takes a calendar not read for no news", () => {
    expect(newsState(false, true).text).toBe("Desativadas");
    expect(newsState(false, true).hint).toContain("não significa que não haja divulgações");
    expect(newsState(true, false)).toMatchObject({ text: "Sem token", tone: "warn" });
    expect(newsState(true, true)).toMatchObject({ text: "Ativas", tone: "on" });
  });
});
