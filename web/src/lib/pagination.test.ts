import { describe, expect, it } from "vitest";
import {
  clampPage,
  listQuery,
  PAGE_SIZE,
  pageCount,
  pageList,
  pageRows,
  searchNote,
} from "./pagination";

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

  it("asks the API for a page, its search and more", () => {
    expect(listQuery({ page: 2, perPage: 10 })).toBe("?page=2&per_page=10");
    expect(listQuery({ page: 1, perPage: 10, q: "  ouro xau " })).toBe(
      "?page=1&per_page=10&q=ouro+xau",
    );
    expect(listQuery({ page: 1, perPage: 10, q: "   " }), "a blank search left out").toBe(
      "?page=1&per_page=10",
    );
    expect(
      listQuery(
        { page: 3, perPage: 5 },
        { sort: "ratio", llm: false, min_trades: 0, order: undefined },
      ),
    ).toBe("?page=3&per_page=5&sort=ratio&llm=false&min_trades=0");
    expect(listQuery({ page: 1, perPage: 10, q: "a&b=%" })).toBe(
      "?page=1&per_page=10&q=a%26b%3D%25",
    );
  });

  it("tells what a search found", () => {
    const prompt = { one: "prompt", many: "prompts" };
    const account = { one: "conta", many: "contas", feminine: true };
    expect(searchNote(3, "", prompt), "no search").toBeNull();
    expect(searchNote(0, " ouro ", prompt)).toBe("Nenhum prompt encontrado para “ouro”.");
    expect(searchNote(1, "ouro", prompt)).toBe("1 prompt encontrado.");
    expect(searchNote(12, "ouro", prompt)).toBe("12 prompts encontrados.");
    expect(searchNote(0, "demo", account)).toBe("Nenhuma conta encontrada para “demo”.");
    expect(searchNote(1, "demo", account)).toBe("1 conta encontrada.");
    expect(searchNote(2, "demo", account)).toBe("2 contas encontradas.");
  });
});
