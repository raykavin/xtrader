import { describe, expect, it } from "vitest";
import type { TradingHoursStatus, TradingWindow } from "../api/types";
import {
  agentsLabel,
  agentsOutside,
  agentsTitle,
  endActionsText,
  type HoursForm,
  hoursError,
  hoursSettings,
  hoursState,
  hoursWindows,
  lastEndLabel,
  MAX_WINDOWS,
  nextWindow,
  parsePairs,
  type WindowForm,
  windowAllows,
  windowDetails,
  windowLabel,
  windowsCheck,
  windowsForm,
  windowsLabel,
  windowsOf,
  windowsRequest,
} from "./tradingHours";

const zone = "America/Belem";
// 2026-10-30 17:30 and 2026-10-31 09:00 in Belém (UTC−3)
const end = Date.UTC(2026, 9, 30, 20, 30) / 1000;
const start = Date.UTC(2026, 9, 31, 12, 0) / 1000;

const status = (fields: Partial<TradingHoursStatus> = {}): TradingHoursStatus => ({
  enabled: true,
  start: "09:00",
  end: "17:30",
  zone,
  open: true,
  blocked: false,
  next_at: end,
  ...fields,
});

const window = (from: string, to: string, fields: Partial<TradingWindow> = {}): TradingWindow => ({
  start: from,
  end: to,
  flatten: false,
  cancel_pending: false,
  ...fields,
});

/** windows as the rows of a form, keyed by their place. */
const rows = (...windows: TradingWindow[]): WindowForm[] =>
  windows.map(({ pairs, ...w }, i) => ({
    ...w,
    agents: w.agents ?? [],
    pairsText: (pairs ?? []).join(";"),
    key: `k${i}`,
  }));

describe("the trading hours", () => {
  it("tells how they stand: off, open until the end, closed until the start", () => {
    expect(hoursState(status({ enabled: false }))).toEqual({ tone: "off", text: "Desligado" });
    expect(hoursState(status())).toEqual({ tone: "on", text: "Aberto · fecha às 17:30" });
    expect(hoursState(status({ open: false, blocked: true, next_at: start }))).toEqual({
      tone: "warn",
      text: "Fora do horário · abre às 09:00",
    });
    // without the next change, the end of the window open or the start of the next one
    expect(hoursState(status({ next_at: undefined, end: "10:00" })).text).toBe(
      "Aberto · fecha às 10:00",
    );
    expect(hoursState(status({ blocked: true, next_at: undefined, start: "12:00" })).text).toBe(
      "Fora do horário · abre às 12:00",
    );
  });

  it("names the windows, of the hours of before too", () => {
    expect(windowLabel(window("08:00", "10:00"))).toBe("08:00–10:00");
    expect(windowsLabel([window("08:00", "10:00"), window("12:00", "14:00")])).toBe(
      "08:00–10:00, 12:00–14:00",
    );
    expect(windowsLabel([])).toBe("");
    expect(hoursWindows(status())).toBe("09:00–17:30");
    expect(
      hoursWindows(status({ windows: [window("08:00", "10:00"), window("12:00", "14:00")] })),
    ).toBe("08:00–10:00, 12:00–14:00");
    expect(hoursWindows(status({ enabled: false }))).toBe("");
    expect(
      windowsOf({ start: "22:00", end: "02:00", flatten: true, cancel_pending: false }),
    ).toEqual([window("22:00", "02:00", { flatten: true })]);
    expect(windowsOf({ windows: null, start: "", end: "" })).toEqual([]);
    expect(
      windowsOf({ windows: [window("08:00", "10:00")], start: "09:00", end: "17:00" }),
    ).toEqual([window("08:00", "10:00")]);
  });

  it("tells what the end does and how the last one went", () => {
    expect(endActionsText(true, true)).toBe("zera as posições e cancela as ordens pendentes");
    expect(endActionsText(false, true)).toBe("cancela as ordens pendentes");
    expect(endActionsText(false, false)).toBe("só bloqueia novas entradas");
    expect(
      lastEndLabel({ at: end, flatten: true, cancel_pending: true, status: "succeeded" }, zone),
    ).toEqual({
      tone: "on",
      text: "Fim às 17:30: posições zeradas e ordens pendentes canceladas · Concluída",
    });
    expect(
      lastEndLabel({ at: end, flatten: true, cancel_pending: false, status: "partial" }, zone),
    ).toEqual({ tone: "warn", text: "Fim às 17:30: posições zeradas · Parcial" });
    expect(
      lastEndLabel({ at: end, flatten: false, cancel_pending: true, status: "executing" }, zone),
    ).toEqual({ tone: "info", text: "Fim às 17:30: ordens pendentes canceladas · executando" });
    expect(
      lastEndLabel(
        {
          at: end,
          start: "14:00",
          end: "17:30",
          flatten: true,
          cancel_pending: false,
          status: "failed",
        },
        zone,
      ),
    ).toEqual({
      tone: "error",
      text: "Fim da janela 14:00–17:30 às 17:30: posições zeradas · Falhou",
    });
  });

  it("checks the form as the controller does", () => {
    const form: HoursForm = {
      enabled: true,
      windows: rows(window("08:00", "10:00"), window("12:00", "14:00")),
    };
    expect(hoursError(form)).toBeNull();
    expect(hoursError({ enabled: true, windows: [] })).toBe("Adicione ao menos uma janela.");
    expect(hoursError({ enabled: false, windows: [] })).toBeNull();
    expect(hoursError({ ...form, windows: rows(window("", "10:00")) })).toBe(
      "O início da janela 1 deve ser um horário HH:MM, de 00:00 a 23:59.",
    );
    // checked even while off, as the controller does
    expect(
      hoursError({
        enabled: false,
        windows: rows(window("08:00", "10:00"), window("12:00", "24:00")),
      }),
    ).toBe("O fim da janela 2 deve ser um horário HH:MM, de 00:00 a 23:59.");
    expect(hoursError({ ...form, windows: rows(window("22:00", "02:00")) })).toBe(
      "A janela 22:00–02:00 deve começar antes de terminar: uma janela não atravessa a meia-noite.",
    );
    expect(hoursError({ ...form, windows: rows(window("09:00", "09:00")) })).toBe(
      "A janela 09:00–09:00 deve começar antes de terminar: uma janela não atravessa a meia-noite.",
    );
    // in the order of the day, whatever the order of the user
    expect(
      hoursError({ ...form, windows: rows(window("09:00", "11:00"), window("08:00", "10:00")) }),
    ).toBe(
      "As janelas 08:00–10:00 e 09:00–11:00 se sobrepõem ou se encostam: junte-as numa só ou deixe um intervalo entre elas.",
    );
    expect(
      hoursError({ ...form, windows: rows(window("10:00", "12:00"), window("12:00", "14:00")) }),
    ).toBe(
      "As janelas 10:00–12:00 e 12:00–14:00 se sobrepõem ou se encostam: junte-as numa só ou deixe um intervalo entre elas.",
    );
    expect(
      hoursError({ ...form, windows: rows(window("10:00", "12:00"), window("12:01", "14:00")) }),
    ).toBeNull();
    const many = rows(
      ...Array.from({ length: MAX_WINDOWS + 1 }, (_, h) =>
        window(`${String(h).padStart(2, "0")}:00`, `${String(h).padStart(2, "0")}:30`),
      ),
    );
    expect(hoursError({ ...form, windows: many.slice(0, MAX_WINDOWS) })).toBeNull();
    expect(hoursError({ ...form, windows: many })).toBe("Use no máximo 24 janelas.");
  });

  it("tells each problem by its row, the ones of the list apart", () => {
    const check = windowsCheck(
      rows(
        window("", "24:00"),
        window("15:00", "13:00"),
        window("08:00", "10:00"),
        window("09:00", "11:00"),
      ),
      true,
    );
    expect(check.rows).toEqual({
      k0: {
        start: "O início da janela 1 deve ser um horário HH:MM, de 00:00 a 23:59.",
        end: "O fim da janela 1 deve ser um horário HH:MM, de 00:00 a 23:59.",
      },
      k1: {
        window:
          "A janela 15:00–13:00 deve começar antes de terminar: uma janela não atravessa a meia-noite.",
      },
    });
    expect(check.list).toBe(
      "As janelas 08:00–10:00 e 09:00–11:00 se sobrepõem ou se encostam: junte-as numa só ou deixe um intervalo entre elas.",
    );
    // the first one as the controller tells it: the rows before the overlaps
    expect(check.first).toBe("O início da janela 1 deve ser um horário HH:MM, de 00:00 a 23:59.");
    expect(windowsCheck([], true)).toEqual({
      rows: {},
      list: "Adicione ao menos uma janela.",
      first: "Adicione ao menos uma janela.",
    });
    expect(windowsCheck([], false)).toEqual({ rows: {}, list: undefined, first: null });
  });

  it("sends the windows in the order of the day, the first as the start and the end", () => {
    const form: HoursForm = {
      enabled: true,
      windows: rows(
        window("12:00", "14:00", { cancel_pending: true }),
        window("08:00", "10:00", { flatten: true }),
      ),
    };
    expect(windowsRequest(form.windows)).toEqual([
      window("08:00", "10:00", { flatten: true }),
      window("12:00", "14:00", { cancel_pending: true }),
    ]);
    expect(hoursSettings(form)).toEqual({
      enabled: true,
      windows: [
        window("08:00", "10:00", { flatten: true }),
        window("12:00", "14:00", { cancel_pending: true }),
      ],
      start: "08:00",
      end: "10:00",
      flatten: true,
      cancel_pending: false,
    });
    // without windows, no start nor end the controller would take as one
    expect(hoursSettings({ enabled: false, windows: [] })).toEqual({
      enabled: false,
      windows: [],
      start: "",
      end: "",
      flatten: false,
      cancel_pending: false,
    });
    const keyed = windowsForm([window("08:00", "10:00"), window("12:00", "14:00")]);
    expect(new Set(keyed.map((w) => w.key)).size).toBe(2);
    expect(windowsRequest(keyed)).toEqual([window("08:00", "10:00"), window("12:00", "14:00")]);
  });

  it("tells the agents of a window, all of them by default", () => {
    expect(agentsLabel(undefined)).toBe("Todos os agentes");
    expect(agentsLabel([])).toBe("Todos os agentes");
    expect(agentsLabel(["highlow"])).toBe("highlow");
    expect(agentsLabel(["highlow", "matrend", "orb"])).toBe("3 agentes");
    expect(agentsTitle([])).toBe("Todos os agentes operam nesta janela");
    expect(agentsTitle(["highlow", "matrend"])).toBe("Operam nesta janela: highlow, matrend");
    expect(windowDetails(window("08:00", "10:00"))).toBe("08:00–10:00");
    expect(windowDetails(window("08:00", "10:00", { agents: ["highlow", "matrend"] }))).toBe(
      "08:00–10:00 (agentes: highlow, matrend)",
    );
    expect(
      hoursWindows(
        status({
          windows: [window("08:00", "10:00", { agents: ["highlow"] }), window("12:00", "14:00")],
        }),
      ),
    ).toBe("08:00–10:00 (agente: highlow), 12:00–14:00");
  });

  it("lets an agent open positions in the windows of all the agents or of its own", () => {
    const morning = window("08:00", "10:00", { agents: ["highlow"] });
    const noon = window("12:00", "14:00");
    expect(windowAllows(morning, "highlow")).toBe(true);
    expect(windowAllows(morning, "matrend")).toBe(false);
    expect(windowAllows(noon, "matrend")).toBe(true);
    expect(windowAllows({ agents: [] }, "matrend")).toBe(true);
    expect(agentsOutside(["highlow", "matrend"], [morning])).toEqual(["matrend"]);
    expect(agentsOutside(["highlow", "matrend"], [morning, noon])).toEqual([]);
    expect(agentsOutside(["highlow", "matrend"], [])).toEqual([]);
  });

  it("carries the agents of a window through the form, sent only when some", () => {
    const [kept, none] = windowsForm([
      window("12:00", "14:00", { agents: ["matrend", "highlow"] }),
      window("08:00", "10:00"),
    ]);
    expect(kept?.agents).toEqual(["matrend", "highlow"]);
    expect(none?.agents).toEqual([]);
    const sent = windowsRequest([kept, none].filter((w) => w !== undefined));
    expect(sent).toEqual([
      window("08:00", "10:00"),
      window("12:00", "14:00", { agents: ["matrend", "highlow"] }),
    ]);
    expect(sent[0]).not.toHaveProperty("agents");
    expect(
      hoursSettings({ enabled: true, windows: rows(window("08:00", "10:00", { agents: ["orb"] })) })
        .windows,
    ).toEqual([window("08:00", "10:00", { agents: ["orb"] })]);
  });

  it("takes the assets of a window typed separated by ;, the case kept", () => {
    expect(parsePairs("")).toEqual([]);
    expect(parsePairs(" ; ;")).toEqual([]);
    expect(parsePairs("XAUUSDm")).toEqual(["XAUUSDm"]);
    expect(parsePairs(" XAUUSDm ;BTCUSDm; USOILm;")).toEqual(["XAUUSDm", "BTCUSDm", "USOILm"]);
    expect(parsePairs("xauusdm;XAUUSDm")).toEqual(["xauusdm", "XAUUSDm"]);
    // kept as typed in the form, a list of before joined by ;
    const [typed] = windowsForm([window("08:00", "10:00", { pairs: ["XAUUSDm", "BTCUSDm"] })]);
    expect(typed?.pairsText).toBe("XAUUSDm;BTCUSDm");
    expect(windowsForm([window("08:00", "10:00")])[0]?.pairsText).toBe("");
  });

  it("names a window with its agents and its assets, each when some", () => {
    expect(windowDetails(window("08:00", "10:00", { pairs: ["XAUUSDm"] }))).toBe(
      "08:00–10:00 (ativo: XAUUSDm)",
    );
    expect(
      windowDetails(
        window("08:00", "10:00", { agents: ["highlow"], pairs: ["XAUUSDm", "BTCUSDm"] }),
      ),
    ).toBe("08:00–10:00 (agente: highlow; ativos: XAUUSDm, BTCUSDm)");
    expect(windowDetails(window("08:00", "10:00", { agents: [], pairs: [] }))).toBe("08:00–10:00");
    expect(
      hoursWindows(
        status({
          windows: [window("08:00", "10:00", { pairs: ["XAUUSDm"] }), window("12:00", "14:00")],
        }),
      ),
    ).toBe("08:00–10:00 (ativo: XAUUSDm), 12:00–14:00");
  });

  it("checks the assets of a window as the controller does", () => {
    const check = (pairsText: string, from = "08:00", to = "10:00") =>
      windowsCheck([{ ...rows(window(from, to))[0], pairsText } as WindowForm], true);
    expect(check("").first).toBeNull();
    expect(check("XAUUSDm;BTCUSDm;USOILm").first).toBeNull();
    expect(check("US30.cash;GER40#;BRENT_X-1").first).toBeNull();
    expect(check("XAUUSDm;xauusdm").first).toBeNull();
    expect(check("XAUUSDm,BTCUSDm").first).toBe(
      'A janela 08:00–10:00 tem um ativo inválido, "XAUUSDm,BTCUSDm": use o nome do ativo no MT5, ex.: XAUUSDm, separados por ;.',
    );
    expect(check("XAU USD").rows.k0?.pairs).toBe(
      'A janela 08:00–10:00 tem um ativo inválido, "XAU USD": use o nome do ativo no MT5, ex.: XAUUSDm, separados por ;.',
    );
    expect(check(".XAU").first).toContain('ativo inválido, ".XAU"');
    expect(check("A".repeat(33)).first).toContain("ativo inválido");
    expect(check("A".repeat(32)).first).toBeNull();
    expect(check("XAUUSDm; BTCUSDm ;XAUUSDm").first).toBe(
      "A janela 08:00–10:00 repete o ativo XAUUSDm.",
    );
    // named by its place while its times are typed, its times told first
    const typing = check("XAU USD", "");
    expect(typing.rows.k0?.pairs).toBe(
      'A janela 1 tem um ativo inválido, "XAU USD": use o nome do ativo no MT5, ex.: XAUUSDm, separados por ;.',
    );
    expect(typing.first).toBe("O início da janela 1 deve ser um horário HH:MM, de 00:00 a 23:59.");
    // the times of every window before the assets, the assets before the overlaps
    const all = windowsCheck(
      rows(
        window("08:00", "10:00", { pairs: ["XAU USD"] }),
        window("09:00", "11:00"),
        window("15:00", "13:00"),
      ),
      true,
    );
    expect(all.first).toBe(
      "A janela 15:00–13:00 deve começar antes de terminar: uma janela não atravessa a meia-noite.",
    );
    const noTimes = windowsCheck(
      rows(window("08:00", "10:00", { pairs: ["XAU USD"] }), window("09:00", "11:00")),
      true,
    );
    expect(noTimes.first).toContain('A janela 08:00–10:00 tem um ativo inválido, "XAU USD"');
    // a window with an asset refused still weighed for the overlaps
    expect(noTimes.list).toBe(
      "As janelas 08:00–10:00 e 09:00–11:00 se sobrepõem ou se encostam: junte-as numa só ou deixe um intervalo entre elas.",
    );
  });

  it("sends the assets of a window as a list, only when some", () => {
    const sent = windowsRequest(
      rows(window("12:00", "14:00"), window("08:00", "10:00", { agents: ["orb"] })).map((w, i) =>
        i === 0 ? { ...w, pairsText: " XAUUSDm; ;BTCUSDm " } : w,
      ),
    );
    expect(sent).toEqual([
      window("08:00", "10:00", { agents: ["orb"] }),
      window("12:00", "14:00", { pairs: ["XAUUSDm", "BTCUSDm"] }),
    ]);
    expect(sent[0]).not.toHaveProperty("pairs");
    expect(
      hoursSettings({
        enabled: true,
        windows: rows(window("08:00", "10:00", { pairs: ["USOILm"] })),
      }).windows,
    ).toEqual([window("08:00", "10:00", { pairs: ["USOILm"] })]);
  });

  it("adds a window free of the others, inside the day", () => {
    expect(nextWindow([])).toEqual(window("09:00", "17:00"));
    expect(nextWindow([window("08:00", "10:00")])).toEqual(window("11:00", "12:00"));
    expect(nextWindow([window("12:00", "14:00"), window("08:00", "10:30")])).toEqual(
      window("15:00", "16:00"),
    );
    expect(nextWindow([window("21:00", "22:30")])).toEqual(window("23:00", "23:59"));
    // no hour left after the latest end: the first free hour of the day
    expect(nextWindow([window("02:00", "23:30")])).toEqual(window("00:00", "01:00"));
    // no free hour at all: the first free minutes
    expect(nextWindow([window("00:00", "11:30"), window("12:10", "23:59")])).toEqual(
      window("11:31", "12:09"),
    );
    // no room left in the day: blank, to type
    expect(nextWindow([window("00:00", "23:59")])).toEqual(window("", ""));
    // the windows refused are left out
    expect(nextWindow([window("", "10:00")])).toEqual(window("09:00", "17:00"));
    for (const added of [
      nextWindow([window("08:00", "10:00")]),
      nextWindow([window("21:00", "22:30")]),
      nextWindow([window("00:00", "11:30"), window("12:10", "23:59")]),
    ]) {
      expect(windowsCheck(rows(added), true).first).toBeNull();
    }
  });
});
