import { describe, expect, it } from "vitest";
import type { AgentSettings } from "../api/types";
import {
  type AgentFormValues,
  addressError,
  agentCreateRequest,
  agentFormErrors,
  agentFormValues,
  agentGroups,
  agentKindLabel,
  agentKinds,
  agentNameError,
  agentNote,
  agentState,
  agentSummary,
  agentUpdateRequest,
  commentError,
  csrfToken,
  httpURLError,
  magicError,
  maxTokensError,
  outboxNote,
  paramKeyError,
  paramValueError,
  parseCount,
  parseTelegramID,
  passwordError,
  promptLabel,
  promptNameError,
  promptStatusLabel,
  settingsTabFromHash,
  settingsTabs,
  usernameError,
} from "./settings";

const agent = (fields: Partial<AgentSettings>): AgentSettings => ({
  name: "llm",
  kind: "llm",
  mode: "live",
  api_address: "localhost:5580",
  enabled: true,
  launched: true,
  restart: true,
  trade_panel: false,
  telegram_control: false,
  reanalysis: false,
  running: false,
  restart_pending: false,
  params: {},
  secrets: { llm_api_key: false, bridge_token: false, calendar_token: false },
  ...fields,
});

const account = { bridge_url: "http://mt5:18812", real_allowed: false };

describe("csrfToken", () => {
  it("reads the token of the session among the cookies", () => {
    expect(csrfToken("theme=dark; csrf_token=abc%3D%3D; other=1")).toBe("abc==");
    expect(csrfToken("csrf_token=x=y")).toBe("x=y");
    expect(csrfToken("theme=dark")).toBe("");
    expect(csrfToken("")).toBe("");
  });
});

describe("agents", () => {
  it("labels the kinds", () => {
    expect(agentKindLabel("trend")).toBe("Tendência");
    expect(agentKindLabel("other")).toBe("other");
  });

  it("tells the state of the process", () => {
    expect(agentState(agent({ enabled: false, running: true })).label).toBe("Desativado");
    expect(agentState(agent({ running: true }))).toEqual({
      label: "Executando",
      tone: "on",
      mark: "play",
    });
    expect(agentState(agent({ running: true, restart_pending: true })).label).toBe(
      "Reinício pendente",
    );
    expect(agentState(agent({}))).toEqual({ label: "Parado", tone: "warn", mark: "stop" });
    expect(agentState(agent({ launched: false })).label).toBe("Iniciado à parte");
    const blocked = "the agent llm, running in live mode, trades XAUUSDm";
    expect(agentState(agent({ launch_error: blocked }))).toEqual({
      label: "Não iniciado",
      tone: "warn",
      mark: "stop",
    });
    expect(agentState(agent({ running: true, launch_error: blocked })).label).toBe("Não iniciado");
    expect(agentState(agent({ enabled: false, launch_error: blocked })).label).toBe("Desativado");
    expect(agentState(agent({ running: true, restarting: true, restart_pending: true }))).toEqual({
      label: "Reiniciando",
      tone: "warn",
      mark: "stop",
    });
    expect(agentState(agent({ exited: true, restart: false })).label).toBe("Parado");
  });

  it("tells why an agent stopped, or how often it was started again", () => {
    expect(agentNote(agent({}))).toBe("");
    expect(agentNote(agent({ last_exit: "exit status 1" }))).toBe("Última saída: exit status 1");
    expect(
      agentNote(agent({ launch_error: "the address of its API", last_exit: "exit status 1" })),
    ).toBe("Não iniciado: the address of its API");
    expect(agentNote(agent({ running: true, restart_pending: true, restarts: 2 }))).toBe(
      "Configurações de inicialização alteradas: reinicie para aplicá-las.",
    );
    expect(agentNote(agent({ restart: false, exited: true, last_exit: "exit status 1" }))).toBe(
      "Saiu e fica parado, iniciado com “Reiniciar se cair” desligado: reinicie para iniciá-lo de novo (um reinício do controller também o inicia). Última saída: exit status 1",
    );
    expect(agentNote(agent({ restart: false, exited: true }))).toBe(
      "Saiu e fica parado, iniciado com “Reiniciar se cair” desligado: reinicie para iniciá-lo de novo (um reinício do controller também o inicia).",
    );
    expect(agentNote(agent({ running: true, restarts: 1, last_exit: "signal: killed" }))).toBe(
      "Última saída: signal: killed. Reiniciado 1 vez",
    );
    expect(
      agentNote(
        agent({ running: true, restarting: true, restarts: 3, last_exit: "exit status 2" }),
      ),
    ).toBe("Última saída: exit status 2. Reiniciado 3 vezes");
    expect(agentNote(agent({ running: true, restarts: 2 }))).toBe("Reiniciado 2 vezes");
  });

  it("tells what the outbox of an agent holds not written, once failing or a minute old", () => {
    const now = Date.UTC(2026, 9, 6, 12, 0, 0);
    const ago = (seconds: number) => Math.floor(now / 1000) - seconds;
    expect(outboxNote(agent({}), now)).toBe("");
    expect(outboxNote(agent({ outbox: { pending: 3, since: ago(20) } }), now)).toBe("");
    expect(outboxNote(agent({ outbox: { pending: 1, since: ago(20), failing: true } }), now)).toBe(
      "Outbox: 1 registro ainda não gravado no banco (o envio está falhando: veja o log do controller)",
    );
    expect(outboxNote(agent({ outbox: { pending: 40, since: ago(12 * 60 + 5) } }), now)).toBe(
      "Outbox: 40 registros ainda não gravados no banco, o mais antigo há 12 min",
    );
    expect(
      outboxNote(
        agent({ outbox: { pending: 2, since: ago(3 * 3600), failing: true, set_apart: 1 } }),
        now,
      ),
    ).toBe(
      "Outbox: 2 registros ainda não gravados no banco, o mais antigo há 3 h (o envio está falhando: veja o log do controller). Outbox: 1 registro recusado para sempre, separado no arquivo do agente (veja o log do agente)",
    );
    expect(outboxNote(agent({ outbox: { pending: 0, set_apart: 4 } }), now)).toBe(
      "Outbox: 4 registros recusados para sempre, separados no arquivo do agente (veja o log do agente)",
    );
    expect(outboxNote(agent({ outbox: { pending: 5, since: ago(3 * 86400) } }), now)).toBe(
      "Outbox: 5 registros ainda não gravados no banco, o mais antigo há 3 dias",
    );
    expect(outboxNote(agent({ outbox: { pending: 0, held: 1 } }), now)).toBe(
      "Outbox: 1 registro que falhou seguidamente, separado no arquivo do agente e reenviado de tempos em tempos (veja o log do agente)",
    );
    expect(outboxNote(agent({ outbox: { pending: 0, set_apart: 1, held: 2 } }), now)).toBe(
      "Outbox: 1 registro recusado para sempre, separado no arquivo do agente (veja o log do agente). Outbox: 2 registros que falharam seguidamente, separados no arquivo do agente e reenviados de tempos em tempos (veja o log do agente)",
    );
  });

  it("groups the agents by kind, the LLM ones first, the unknown kinds last", () => {
    const groups = agentGroups([
      agent({ name: "t1", kind: "trend" }),
      agent({ name: "x1", kind: "scalper", enabled: false }),
      agent({ name: "i1", kind: "intraday", enabled: false }),
      agent({ name: "llm" }),
      agent({ name: "t2", kind: "trend", enabled: false }),
      agent({ name: "a1", kind: "arbitrage" }),
      agent({ name: "t3", kind: "trend" }),
    ]);
    expect(groups.map((g) => [g.kind, g.label, g.agents.length, g.enabled])).toEqual([
      ["llm", "LLM", 1, 1],
      ["trend", "Tendência", 3, 2],
      ["intraday", "Intraday", 1, 0],
      ["arbitrage", "arbitrage", 1, 1],
      ["scalper", "scalper", 1, 0],
    ]);
    expect(
      groups[1]?.agents.map((a) => a.name),
      "in the order of the list",
    ).toEqual(["t1", "t2", "t3"]);
    expect(agentGroups([agent({ kind: "momentum" }), agent({ kind: "breakout" })])).toMatchObject([
      { kind: "momentum" },
      { kind: "breakout" },
    ]);
    expect(agentGroups([])).toEqual([]);
  });

  it("tells an agent apart by its pairs, its timeframe and its strategy or model", () => {
    const trend = agent({
      kind: "trend",
      params: { pair: "BTCUSDm", timeframe: "5m", strategy: "matrend", lot: "0.01" },
      model: "left out",
    });
    expect(agentSummary(trend)).toEqual([
      { label: "Par", value: "BTCUSDm" },
      { label: "Timeframe", value: "5m" },
      { label: "Estratégia", value: "matrend" },
    ]);
    const llm = agent({
      params: { pair: "BTCUSDm, XAUUSDm,", timeframe: "30m", strategy: "left out" },
      model: "openai gpt-5",
    });
    expect(agentSummary(llm)).toEqual([
      { label: "Pares", value: "BTCUSDm, XAUUSDm" },
      { label: "Timeframe", value: "30m" },
      { label: "Modelo", value: "openai gpt-5" },
    ]);
    expect(agentSummary(agent({ kind: "trend", params: { pair: " ", timeframe: "1h" } }))).toEqual([
      { label: "Timeframe", value: "1h" },
    ]);
    expect(agentSummary(agent({}))).toEqual([]);
  });
});

describe("the tabs of the settings", () => {
  it("reads the tab of the hash of the URL, the agents by default", () => {
    expect(settingsTabs).toEqual([
      "agentes",
      "prompts",
      "supervisor",
      "telegram",
      "usuarios",
      "layout",
    ]);
    for (const tab of settingsTabs) {
      expect(settingsTabFromHash(`#${tab}`)).toBe(tab);
    }
    expect(settingsTabFromHash("#Telegram")).toBe("telegram");
    expect(settingsTabFromHash("usuarios")).toBe("usuarios");
    for (const hash of ["", "#", "#outra", "#prompts/x"]) {
      expect(settingsTabFromHash(hash), hash).toBe("agentes");
    }
  });
});

describe("prompts", () => {
  it("names a prompt with its version", () => {
    expect(promptLabel({ id: 3, name: "trend_following", version: 2 })).toBe("trend_following v2");
    expect(promptLabel({ name: "default" })).toBe("default (embutido)");
    expect(promptLabel({ id: 1, name: "default", version: 1 })).toBe("default v1");
    expect(promptLabel({ name: "scalp" })).toBe("scalp");
  });

  it("tells what came of the last switch", () => {
    expect(promptStatusLabel(undefined)).toBeNull();
    expect(
      promptStatusLabel({ prompt_id: 3, name: "trend", version: 2, applied: true, at: 1 }),
    ).toEqual({ text: "trend v2: aplicado", tone: "on" });
    const refused = promptStatusLabel({
      prompt_id: 4,
      name: "ema200",
      version: 1,
      applied: false,
      error: "its indicators need 1000 candles",
      at: 1,
    });
    expect(refused?.tone).toBe("warn");
    expect(refused?.text).toBe(
      "ema200 v1: recusado, o anterior mantido (its indicators need 1000 candles)",
    );
  });

  it("takes the names the controller takes", () => {
    expect(promptNameError("trend_following")).toBeNull();
    expect(promptNameError("v1.2-x")).toBeNull();
    expect(promptNameError("Trend")).not.toBeNull();
    expect(promptNameError("_x")).not.toBeNull();
    expect(promptNameError("")).not.toBeNull();
    expect(promptNameError("a".repeat(65))).not.toBeNull();
  });
});

describe("forms", () => {
  it("reads the Telegram IDs", () => {
    expect(parseTelegramID(" 123456789 ")).toBe(123456789);
    expect(parseTelegramID("-1001234567890")).toBe(-1001234567890);
    expect(parseTelegramID("0")).toBeNull();
    expect(parseTelegramID("12a")).toBeNull();
    expect(parseTelegramID("")).toBeNull();
    expect(parseTelegramID("99999999999999999")).toBeNull();
  });

  it("takes the passwords bcrypt takes", () => {
    expect(passwordError("1234567")).not.toBeNull();
    expect(passwordError("12345678")).toBeNull();
    expect(passwordError("é".repeat(36))).toBeNull();
    expect(passwordError("é".repeat(37))).not.toBeNull();
  });

  it("takes the usernames the controller takes", () => {
    expect(usernameError("ray.kavin@x")).toBeNull();
    expect(usernameError(".x")).not.toBeNull();
    expect(usernameError("a b")).not.toBeNull();
  });
});

describe("the fields of an agent", () => {
  it("lists the kinds of the controller", () => {
    expect(agentKinds).toEqual(["llm", "trend", "momentum", "breakout", "intraday"]);
  });

  it("takes the names the controller takes", () => {
    for (const name of ["gold", "nb.llm-1_x", "A", "a".repeat(64), " gold "]) {
      expect(agentNameError(name), name).toBeNull();
    }
    for (const name of ["", "-x", ".x", "_x", "a b", "a/b", "../gold", "a".repeat(65), "ção"]) {
      expect(agentNameError(name), name).not.toBeNull();
    }
  });

  it("takes the addresses host:port, of a port of 1 to 65535, on the loopback", () => {
    for (const address of [
      "localhost:5580",
      "LocalHost:5580",
      "127.0.0.1:1",
      "127.1.2.255:5580",
      "[::1]:65535",
      "[0:0:0:0:0:0:0:1]:5580",
      "[::ffff:127.0.0.1]:5580",
      " localhost:5580 ",
    ]) {
      expect(addressError(address), address).toBeNull();
    }
    for (const address of [
      "mt5-bridge.local:8080",
      "0.0.0.0:5580",
      "[::]:5580",
      "10.0.0.5:5580",
      "169.254.169.254:80",
      "128.0.0.1:5580",
      "127.0.0.256:5580",
      "127.0.0.01:5580",
      "127.1:5580",
      "localhost.evil.example:5580",
      "127.0.0.1.nip.io:5580",
      "[::ffff:10.0.0.5]:5580",
      "[fe80::1]:5580",
    ]) {
      expect(addressError(address), address).toMatch(/loopback/);
    }
    for (const address of [
      "",
      "localhost",
      ":5580",
      "localhost:0",
      "localhost:65536",
      "localhost:port",
      "::1:80",
      "[::1]",
      "[]:80",
      "host:80:90",
      "http://localhost:80",
      "local host:80",
    ]) {
      expect(addressError(address), address).not.toBeNull();
    }
  });

  it("takes the http(s) URLs without credentials", () => {
    for (const url of [
      "http://mt5:18812",
      "https://api.openai.com/v1",
      "HTTP://127.0.0.1:22346/mcp",
      " http://mt5:18812 ",
      "http://[::1]:18812",
    ]) {
      expect(httpURLError(url), url).toBeNull();
    }
    for (const url of ["", "mt5:18812", "ftp://mt5", "http://", "http:///mt5", "http:mt5"]) {
      expect(httpURLError(url), url).toBe("Use uma URL que comece com http:// ou https://.");
    }
    expect(httpURLError("http://mt5\t:18812")).not.toBeNull();
    for (const url of ["http://user:pass@mt5:18812", "https://token@mt5", "http://@mt5"]) {
      expect(httpURLError(url), url).toContain("Sem usuário nem senha");
    }
  });

  it("reads the whole numbers above 0", () => {
    expect(parseCount("")).toBeUndefined();
    expect(parseCount(" 42 ")).toBe(42);
    expect(parseCount("007")).toBe(7);
    expect(parseCount("0")).toBeNaN();
    expect(parseCount("-1")).toBeNaN();
    expect(parseCount("1.5")).toBeNaN();
    expect(parseCount("1e3")).toBeNaN();
    expect(parseCount("9007199254740993")).toBeNaN();
    expect(parseCount("11", 10)).toBeNaN();
  });

  it("takes the magic numbers, the tokens and the comments MT5 takes", () => {
    for (const magic of ["", "  ", "1", "4242", String(Number.MAX_SAFE_INTEGER)]) {
      expect(magicError(magic), magic).toBeNull();
    }
    for (const magic of ["0", "-1", "1.5", "abc"]) {
      expect(magicError(magic), magic).not.toBeNull();
    }
    expect(maxTokensError("")).toBeNull();
    expect(maxTokensError("2147483647")).toBeNull();
    expect(maxTokensError("2147483648")).not.toBeNull();
    expect(maxTokensError("0")).not.toBeNull();
    for (const comment of ["", "nb:gold", "a".repeat(31), " nb:gold ", "x[y"]) {
      expect(commentError(comment), comment).toBeNull();
    }
    expect(commentError("a".repeat(32))).toBe("Use até 31 caracteres.");
    expect(commentError("[TP]")).toBe("O comentário não pode começar com [.");
    expect(commentError("ação")).toBe("Use só caracteres ASCII imprimíveis, sem acentos.");
    expect(commentError("nb:\tgold")).not.toBeNull();
  });

  it("takes the params the controller takes", () => {
    expect(paramKeyError("pair", "trend")).toBeNull();
    expect(paramKeyError(" lot ", "llm")).toBeNull();
    expect(paramKeyError("1h", "trend")).toBeNull();
    for (const key of ["llm-min-stop-atr", "calendar-importance", "news-block-before"]) {
      expect(paramKeyError(key, "llm"), key).toBeNull();
      expect(paramKeyError(key, "trend"), key).toBe(
        "Parâmetro do modelo de linguagem: só para agentes LLM.",
      );
    }
    expect(paramKeyError("", "trend")).toBe("Informe o nome do parâmetro.");
    expect(paramKeyError("--lot", "trend")).toBe("Sem o traço: lot, não --lot.");
    for (const key of ["Lot", "pair name", "-", "_x", "lot=0.01"]) {
      expect(paramKeyError(key, "trend"), key).toBe(
        "Use letras minúsculas, dígitos e -, como no nome da flag.",
      );
    }
    for (const key of [
      "mode",
      "api",
      "reanalysis",
      "mt5-comment",
      "mt5-token",
      "llm-model",
      "prompt-path",
      "db",
    ]) {
      expect(paramKeyError(key, "llm"), key).toBe(
        `${key} é uma configuração do próprio agente, não um parâmetro.`,
      );
    }

    for (const value of ["XAUUSDm", "", " XAUUSDm ", "x".repeat(1024), "é".repeat(512)]) {
      expect(paramValueError(value), value).toBeNull();
    }
    expect(paramValueError("x".repeat(1025))).toBe("Use até 1024 bytes.");
    for (const [value, name] of [
      ["llm-correlated = XAUUSDm=USTECm+DXYm", "llm-correlated"],
      ["lot=0.01", "lot"],
      ["-lot = 0.01", "lot"],
    ] as [string, string][]) {
      expect(paramValueError(value, name), value).toBe(
        "O valor repete o nome do parâmetro: preencha só o que vem depois do =.",
      );
    }
    for (const [value, name] of [
      ["BTCUSDm=0.03;XAUUSDm=0.01", "lot"],
      ["XAUUSDm=USTECm+DXYm", "llm-correlated"],
      ["lottery", "lot"],
    ] as [string, string][]) {
      expect(paramValueError(value, name), value).toBeNull();
    }
    expect(paramValueError("é".repeat(513))).toBe("Use até 1024 bytes.");
    for (const value of ["XAUUSDm\n-mode=paper", "a\u0085b", "a\u007fb", "a\tb"]) {
      expect(paramValueError(value), value).toBe(
        "Use uma linha de texto, sem caracteres de controle.",
      );
    }
  });
});

describe("the form of an agent", () => {
  const gold = agent({
    name: "gold",
    api_address: "localhost:5600",
    params: { pair: "XAUUSDm", "llm-min-stop-atr": "1" },
    account: { ...account, magic: 4242, comment: "nb:gold" },
    llm: {
      provider: "openai",
      model: "gpt-5",
      reasoning_effort: "low",
      calendar_url: "http://mt5:22346/mcp",
    },
    model: "openai gpt-5",
    prompt_id: 3,
    reanalysis: true,
  });
  /** A new agent of the form, taken. */
  const newAgent = (fields: Partial<AgentFormValues> = {}): AgentFormValues => ({
    ...agentFormValues(),
    name: "gold",
    kind: "trend",
    apiAddress: "localhost:5601",
    bridgeURL: "http://mt5:18812",
    ...fields,
  });

  it("starts a new agent as the controller creates one", () => {
    expect(agentFormValues()).toEqual({
      name: "",
      kind: "",
      mode: "paper",
      apiAddress: "",
      enabled: false,
      launched: true,
      restart: true,
      tradePanel: false,
      telegramControl: false,
      reanalysis: false,
      bridgeURL: "",
      magic: "",
      comment: "",
      realAllowed: false,
      provider: "openai",
      model: "",
      baseURL: "",
      reasoningEffort: "",
      maxTokens: "",
      calendarURL: "",
      promptID: "",
      llmAPIKey: "",
      bridgeToken: "",
      calendarToken: "",
      params: [],
    });
  });

  it("fills the form with the settings of the agent", () => {
    const values = agentFormValues(gold);
    expect(values).toMatchObject({
      name: "gold",
      kind: "llm",
      mode: "live",
      apiAddress: "localhost:5600",
      bridgeURL: "http://mt5:18812",
      magic: "4242",
      comment: "nb:gold",
      provider: "openai",
      model: "gpt-5",
      baseURL: "",
      reasoningEffort: "low",
      maxTokens: "",
      calendarURL: "http://mt5:22346/mcp",
      promptID: "3",
      llmAPIKey: "",
      reanalysis: true,
    });
    expect(values.params).toEqual([
      { id: 0, key: "llm-min-stop-atr", value: "1" },
      { id: 1, key: "pair", value: "XAUUSDm" },
    ]);
    expect(agentFormValues(agent({})).provider, "without its language model").toBe("");
  });

  it("checks the form as the controller checks it", () => {
    expect(Object.keys(agentFormErrors(agentFormValues(), true)).sort()).toEqual([
      "apiAddress",
      "bridgeURL",
      "kind",
      "name",
    ]);
    expect(agentFormErrors(newAgent(), true)).toEqual({});
    expect(agentFormErrors(agentFormValues(gold), false), "the name and kind of an edit").toEqual(
      {},
    );
    expect(
      Object.keys(
        agentFormErrors(
          newAgent({
            mode: "backtest",
            apiAddress: "localhost",
            bridgeURL: "mt5:18812",
            magic: "0",
            comment: "[x",
          }),
          true,
        ),
      ).sort(),
    ).toEqual(["apiAddress", "bridgeURL", "comment", "magic", "mode"]);

    // the language model of an LLM agent only
    expect(agentFormErrors(newAgent({ kind: "llm" }), true)).toEqual({
      model: "Informe o modelo.",
    });
    expect(
      Object.keys(
        agentFormErrors(
          newAgent({
            kind: "llm",
            provider: "",
            model: "gpt-5",
            baseURL: "api",
            maxTokens: "0",
            calendarURL: "mt5:22346",
          }),
          true,
        ),
      ).sort(),
    ).toEqual(["baseURL", "calendarURL", "maxTokens", "provider"]);
    expect(agentFormErrors(newAgent({ baseURL: "api", maxTokens: "x" }), true)).toEqual({});

    // the params by row, the blank ones left out, a name repeated refused
    const params = [
      { id: 4, key: "pair", value: "XAUUSDm" },
      { id: 5, key: "", value: " " },
      { id: 6, key: " pair ", value: "BTCUSDm" },
      { id: 7, key: "", value: "0.01" },
      { id: 8, key: "llm-candles", value: "60" },
      { id: 9, key: "timeframe", value: "15m\n" },
    ];
    expect(agentFormErrors(newAgent({ params }), true)).toEqual({
      params: {
        6: "Parâmetro repetido.",
        7: "Informe o nome do parâmetro.",
        8: "Parâmetro do modelo de linguagem: só para agentes LLM.",
      },
    });
    expect(agentFormErrors(newAgent({ kind: "llm", model: "gpt-5", params }), true)).toEqual({
      params: { 6: "Parâmetro repetido.", 7: "Informe o nome do parâmetro." },
    });
  });

  it("creates the agent of the form, its optional fields left out", () => {
    const request = agentCreateRequest(
      newAgent({
        name: " gold ",
        apiAddress: " localhost:5601 ",
        comment: " ",
        model: "gpt-5",
        llmAPIKey: "left out",
        reanalysis: true, // of an LLM agent only: left out
        params: [
          { id: 0, key: " pair ", value: " XAUUSDm " },
          { id: 1, key: "", value: "" },
        ],
      }),
    );
    expect(JSON.parse(JSON.stringify(request))).toStrictEqual({
      name: "gold",
      kind: "trend",
      mode: "paper",
      api_address: "localhost:5601",
      enabled: false,
      launched: true,
      restart: true,
      trade_panel: false,
      telegram_control: false,
      params: { pair: "XAUUSDm" },
      account: { bridge_url: "http://mt5:18812", real_allowed: false },
    });

    const llm = agentCreateRequest(
      newAgent({
        kind: "llm",
        mode: "live",
        enabled: true,
        tradePanel: true,
        magic: "4242",
        comment: "nb:gold",
        realAllowed: true,
        bridgeToken: " bridge ",
        provider: "anthropic",
        model: " claude-sonnet-5 ",
        reasoningEffort: "low",
        maxTokens: "8192",
        calendarURL: " http://mt5:22346/mcp ",
        promptID: "3",
        llmAPIKey: " key ",
        calendarToken: " ",
        reanalysis: true,
      }),
    );
    expect(JSON.parse(JSON.stringify(llm))).toStrictEqual({
      name: "gold",
      kind: "llm",
      mode: "live",
      api_address: "localhost:5601",
      enabled: true,
      launched: true,
      restart: true,
      trade_panel: true,
      telegram_control: false,
      params: {},
      account: {
        bridge_url: "http://mt5:18812",
        magic: 4242,
        comment: "nb:gold",
        real_allowed: true,
      },
      bridge_token: "bridge",
      llm: {
        provider: "anthropic",
        model: "claude-sonnet-5",
        reasoning_effort: "low",
        max_tokens: 8192,
        calendar_url: "http://mt5:22346/mcp",
      },
      prompt_id: 3,
      llm_api_key: "key",
      reanalysis: true,
    });
    const off = agentCreateRequest(newAgent({ kind: "llm", model: "gpt-5" }));
    expect(off.reanalysis, "off unless switched on").toBe(false);
  });

  it("changes the fields changed only", () => {
    const initial = agentFormValues(gold);
    expect(agentUpdateRequest(initial, initial)).toBeNull();
    expect(agentUpdateRequest(initial, { ...initial, apiAddress: " localhost:5600 " })).toBeNull();
    expect(
      agentUpdateRequest(initial, {
        ...initial,
        mode: "paper",
        apiAddress: "localhost:5700",
        restart: false,
      }),
    ).toEqual({ mode: "paper", api_address: "localhost:5700", restart: false });
    expect(
      agentUpdateRequest(initial, {
        ...initial,
        launched: false,
        magic: "7",
        realAllowed: true,
        model: "gpt-5-mini",
        maxTokens: "8192",
      }),
    ).toEqual({
      launched: false,
      account: { magic: 7, real_allowed: true },
      llm: { model: "gpt-5-mini", max_tokens: 8192 },
    });

    // the optional fields emptied are removed, their defaults applying
    expect(
      agentUpdateRequest(initial, {
        ...initial,
        magic: "",
        comment: " ",
        reasoningEffort: "",
        calendarURL: "",
      }),
    ).toEqual({
      account: { magic: 0, comment: "" },
      llm: { reasoning_effort: "", calendar_url: "" },
    });
    expect(
      agentUpdateRequest(
        agentFormValues(agent({ llm: { provider: "openai", model: "gpt-5", max_tokens: 4096 } })),
        agentFormValues(agent({ llm: { provider: "openai", model: "gpt-5" } })),
      ),
    ).toEqual({ llm: { max_tokens: 0 } });

    // the params, one changed, replace them all: {} removes them
    expect(
      agentUpdateRequest(initial, {
        ...initial,
        params: [...initial.params, { id: 9, key: "lot", value: "0.01" }],
      }),
    ).toEqual({ params: { "llm-min-stop-atr": "1", pair: "XAUUSDm", lot: "0.01" } });
    expect(agentUpdateRequest(initial, { ...initial, params: [] })).toEqual({ params: {} });
    expect(
      agentUpdateRequest(initial, {
        ...initial,
        params: [...initial.params].reverse().concat({ id: 9, key: "", value: "" }),
      }),
      "the same, in another order",
    ).toBeNull();

    // the language model of the other kinds is never sent
    const trend = agentFormValues(agent({ kind: "trend", account }));
    expect(agentUpdateRequest(trend, { ...trend, provider: "anthropic", model: "x" })).toBeNull();
    // an LLM agent without one gets it
    const bare = agentFormValues(agent({ account }));
    expect(agentUpdateRequest(bare, { ...bare, provider: "openai", model: "gpt-5" })).toEqual({
      llm: { provider: "openai", model: "gpt-5" },
    });
  });
});
