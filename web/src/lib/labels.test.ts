import { describe, expect, it } from "vitest";
import { botStatusLabel, orderStatusLabel, orderTypeLabel, sideLabel } from "./labels";

describe("labels", () => {
  it("translates the values of the API", () => {
    expect(sideLabel("BUY")).toBe("Compra");
    expect(sideLabel("SELL")).toBe("Venda");
    expect(orderTypeLabel("MARKET")).toBe("Mercado");
    expect(orderTypeLabel("STOP_LOSS")).toBe("Stop loss");
    expect(orderStatusLabel("FILLED")).toBe("Executada");
    expect(orderStatusLabel("CANCELED")).toBe("Cancelada");
    expect(botStatusLabel("running")).toBe("executando");
    expect(botStatusLabel("stopped")).toBe("parado");
  });

  it("keeps the values it doesn't know", () => {
    expect(orderStatusLabel("SOMETHING_NEW")).toBe("SOMETHING_NEW");
    expect(botStatusLabel("")).toBe("desconhecido");
  });
});
