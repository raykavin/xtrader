import { describe, expect, it } from "vitest";
import { clampPage, PAGE_SIZE, pageCount, pageList, pageRows } from "./pagination";

describe("pagination", () => {
  it("pages 10 rows at a time", () => {
    expect(PAGE_SIZE).toBe(10);
    expect(pageCount(0)).toBe(1);
    expect(pageCount(10)).toBe(1);
    expect(pageCount(11)).toBe(2);
    expect(pageCount(50)).toBe(5);
  });

  it("keeps the page within the rows, eg: once they shrink", () => {
    expect(clampPage(-1, 25)).toBe(0);
    expect(clampPage(2, 25)).toBe(2);
    expect(clampPage(7, 25)).toBe(2);
    expect(clampPage(3, 0)).toBe(0);
  });

  it("returns the rows of a page", () => {
    const rows = Array.from({ length: 23 }, (_, i) => i);
    expect(pageRows(rows, 0)).toEqual([0, 1, 2, 3, 4, 5, 6, 7, 8, 9]);
    expect(pageRows(rows, 2)).toEqual([20, 21, 22]);
    expect(pageRows(rows, 9), "the last page").toEqual([20, 21, 22]);
    expect(pageRows([], 0)).toEqual([]);
  });

  it("offers the pages around the current one", () => {
    expect(pageList(0, 1)).toEqual([0]);
    expect(pageList(3, 7), "all of a few").toEqual([0, 1, 2, 3, 4, 5, 6]);
    expect(pageList(0, 12)).toEqual([0, 1, null, 11]);
    expect(pageList(5, 12)).toEqual([0, null, 4, 5, 6, null, 11]);
    expect(pageList(3, 12), "a gap of one page is the page").toEqual([0, 1, 2, 3, 4, null, 11]);
    expect(pageList(11, 12)).toEqual([0, null, 10, 11]);
  });
});
