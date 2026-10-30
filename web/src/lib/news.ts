// The news of the agents on the page of the settings: how the test of an MCP
// server of the calendar went, in words and tone.

/** A status of a test of an MCP server in words, its tone and what it means. */
export interface NewsStatusLabel {
  text: string;
  tone: "on" | "warn" | "error";
  hint: string;
}

/** The status of a test of an MCP server of the news, see NewsTestResult. */
export function newsStatusLabel(status: string): NewsStatusLabel {
  switch (status) {
    case "ok":
      return {
        text: "Respondeu",
        tone: "on",
        hint: "O servidor aceitou o token e devolveu o calendário.",
      };
    case "missing_server":
      return {
        text: "Sem servidor",
        tone: "warn",
        hint: "Nada foi consultado: defina o endereço do servidor MCP.",
      };
    case "missing_token":
      return {
        text: "Token ausente",
        tone: "warn",
        hint: "Nada foi consultado: defina o token do servidor MCP.",
      };
    case "unauthorized":
      return {
        text: "Token recusado",
        tone: "error",
        hint: "O servidor recusou o token: confira em Ferramentas › Opções › Servidor MCP do MetaTrader 5.",
      };
  }
  return {
    text: "Indisponível",
    tone: "error",
    hint: "O servidor não respondeu: o terminal ou o servidor MCP pode estar fora do ar.",
  };
}

/**
 * The state of the news in words: off (nothing read), on without the token
 * (nothing read: the credential missing), or on. Never "no news" for a
 * calendar not read.
 */
export function newsState(
  enabled: boolean,
  tokenSet: boolean,
  url = "x",
  window = 1,
): NewsStatusLabel {
  if (!enabled) {
    return {
      text: "Desativadas",
      tone: "warn",
      hint: "Nenhum agente consulta o calendário nem bloqueia entradas por notícia. O modelo dos agentes LLM é avisado de que as notícias não foram lidas: isso não significa que não haja divulgações.",
    };
  }
  if (url.trim() === "") {
    return {
      text: "Sem servidor",
      tone: "warn",
      hint: "Ligadas, mas sem o endereço do servidor MCP: nenhuma consulta é feita e nada é bloqueado.",
    };
  }
  if (!tokenSet) {
    return {
      text: "Sem token",
      tone: "warn",
      hint: "Ligadas, mas sem o token do servidor MCP: nenhuma consulta é feita, nada é bloqueado e o modelo é avisado da credencial ausente.",
    };
  }
  if (window <= 0) {
    return {
      text: "Sem janela",
      tone: "warn",
      hint: "Ligadas, mas com a janela em 0: os agentes LLM leem o calendário, e nenhuma entrada é bloqueada.",
    };
  }
  return {
    text: "Ativas",
    tone: "on",
    hint: "Todos os agentes, LLM ou não, ficam fora do mercado na janela de cada divulgação das moedas dos seus ativos.",
  };
}
