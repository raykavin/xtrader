import { describe, expect, it } from "vitest";
import {
  formatChartPrice,
  formatMoney,
  formatNumber,
  formatPercent,
  formatPrice,
  formatQuantity,
  formatTime,
} from "./format";

describe("format", () => {
  it("writes numbers as in Brazil", () => {
    expect(formatNumber(1234.567)).toBe("1.234,57");
    expect(formatPrice(84500.33)).toBe("84.500,33");
    expect(formatPrice(0.12345678)).toBe("0,12345678");
    expect(formatQuantity(0.01)).toBe("0,01");
  });

  it("keeps the prices of the chart short, the scales narrow", () => {
    expect(formatChartPrice(4169.29)).toBe("4.169,29");
    expect(formatChartPrice(90.422)).toBe("90,422");
    expect(formatChartPrice(1.08456)).toBe("1,08456");
    expect(formatChartPrice(13.11111111111111)).toBe("13,1111"); // the width the chart measures
    expect(formatChartPrice(0.00012345678)).toBe("0,000123457");
  });

  it("writes the money with two decimals", () => {
    expect(formatMoney(500)).toBe("500,00");
    expect(formatMoney(1010.333)).toBe("1.010,33");
    expect(formatMoney(-159.92)).toBe("-159,92");
    expect(formatMoney(-0)).toBe("0,00");
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
