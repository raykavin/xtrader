// Portuguese labels of the values the API sends as they are in the exchange,
// eg: the side and status of the orders.

const sides: Record<string, string> = { BUY: "Compra", SELL: "Venda" };

const orderTypes: Record<string, string> = {
  MARKET: "Mercado",
  LIMIT: "Limite",
  LIMIT_MAKER: "Limite (maker)",
  STOP_LOSS: "Stop loss",
  STOP_LOSS_LIMIT: "Stop loss limite",
  TAKE_PROFIT: "Take profit",
  TAKE_PROFIT_LIMIT: "Take profit limite",
};

const orderStatuses: Record<string, string> = {
  NEW: "Aberta",
  PARTIALLY_FILLED: "Parcial",
  FILLED: "Executada",
  CANCELED: "Cancelada",
  PENDING_CANCEL: "Cancelando",
  REJECTED: "Rejeitada",
  EXPIRED: "Expirada",
};

const botStatuses: Record<string, string> = { running: "executando", stopped: "parado" };

/** The label of value, or value itself when it has none. */
function label(labels: Record<string, string>, value: string): string {
  return labels[value] ?? value;
}

export const sideLabel = (side: string) => label(sides, side);
export const orderTypeLabel = (type: string) => label(orderTypes, type);
export const orderStatusLabel = (status: string) => label(orderStatuses, status);
export const botStatusLabel = (status: string) => label(botStatuses, status || "desconhecido");
