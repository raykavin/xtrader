import { describe, expect, it } from "vitest";
import { formatNumber, formatPercent, formatPrice, formatQuantity, formatTime } from "./format";

describe("format", () => {
  it("writes numbers as in Brazil", () => {
    expect(formatNumber(1234.567)).toBe("1.234,57");
    expect(formatPrice(84500.33)).toBe("84.500,33");
    expect(formatPrice(0.12345678)).toBe("0,12345678");
    expect(formatQuantity(0.01)).toBe("0,01");
  });

  it("signs the percentages", () => {
    expect(formatPercent(0.1234)).toBe("+12,34%");
    expect(formatPercent(-0.05, 1)).toBe("-5,0%");
    expect(formatPercent(0)).toBe("0,00%");
    expect(formatPercent(-0)).toBe("0,00%");
    expect(formatPercent(Number.POSITIVE_INFINITY)).toBe("∞");
  });

  it("writes the day and time in UTC", () => {
    expect(formatTime(Date.UTC(2021, 8, 26, 20, 5) / 1000)).toBe("26/09/2021 20:05");
  });
});
