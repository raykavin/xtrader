package ui

import (
	"slices"
	"strings"
)

// permissionAreas is the catalog of the permissions of the dashboard, in the
// order of its pages and of its settings: every Perm constant in one area.
var permissionAreas = []PermissionArea{
	{Key: "dashboard", Label: "Painel", Hint: "Agentes, gráficos, decisões, posições e o limite do dia.",
		View: PermDashboardView, ViewLabel: "Ver"},
	{Key: "trading", Label: "Operação",
		Hint: "Iniciar e parar bots, mover e cancelar ordens, reanálise, fechar posições, zerar tudo, bloquear " +
			"entradas e confirmar as ações do XTrader AI.",
		Manage: PermTradingOperate, ManageLabel: "Operar"},
	{Key: "chat", Label: "Chat do XTrader AI", Hint: "Conversar com o XTrader AI e pedir uma análise.",
		View: PermSupervisorChat, ViewLabel: "Usar"},
	{Key: "performance", Label: "Desempenho", Hint: "O resumo das operações das contas.",
		View: PermPerformanceView, ViewLabel: "Ver"},
	{Key: "backtests", Label: "Backtesting", Hint: "Os backtests: iniciar, cancelar e excluir ao gerenciar.",
		View: PermBacktestsView, ViewLabel: "Ver", Manage: PermBacktestsManage, ManageLabel: "Gerenciar"},
	{Key: "agents", Label: "Agentes",
		Hint: "As configurações dos agentes e os resultados: criar, editar, reiniciar, renomear e excluir ao gerenciar.",
		View: PermAgentsView, ViewLabel: "Ver", Manage: PermAgentsManage, ManageLabel: "Gerenciar"},
	{Key: "mt5-accounts", Label: "Contas MT5",
		Hint: "As contas do MetaTrader 5: editar, trocar a conta do terminal e testar ao gerenciar.",
		View: PermMT5AccountsView, ViewLabel: "Ver", Manage: PermMT5AccountsManage, ManageLabel: "Gerenciar"},
	{Key: "limits", Label: "Limites", Hint: "Os limites diários, os horários de operação e a exposição por ativo.",
		View: PermLimitsView, ViewLabel: "Ver", Manage: PermLimitsManage, ManageLabel: "Gerenciar"},
	{Key: "news", Label: "Notícias", Hint: "O calendário de notícias e a janela de bloqueio.",
		View: PermNewsView, ViewLabel: "Ver", Manage: PermNewsManage, ManageLabel: "Gerenciar"},
	{Key: "prompts", Label: "Prompts", Hint: "Os prompts dos agentes LLM.",
		View: PermPromptsView, ViewLabel: "Ver", Manage: PermPromptsManage, ManageLabel: "Gerenciar"},
	{Key: "supervisor", Label: "XTrader AI", Hint: "A configuração do XTrader AI, as ações e as proteções.",
		View: PermSupervisorView, ViewLabel: "Ver", Manage: PermSupervisorManage, ManageLabel: "Gerenciar"},
	{Key: "telegram", Label: "Telegram", Hint: "O bot do Telegram e os usuários autorizados nele.",
		View: PermTelegramView, ViewLabel: "Ver", Manage: PermTelegramManage, ManageLabel: "Gerenciar"},
	{Key: "users", Label: "Usuários e papéis",
		Hint: "Os usuários do painel e os papéis. Quem gerencia pode dar a si mesmo qualquer papel.",
		View: PermUsersView, ViewLabel: "Ver", Manage: PermUsersManage, ManageLabel: "Gerenciar"},
}

// PermissionAreas returns the catalog of the permissions of the dashboard, in
// the order of its pages and of its settings.
func PermissionAreas() []PermissionArea {
	return slices.Clone(permissionAreas)
}

// AllPermissions returns every permission of the catalog, in its order: the
// ones of the fixed role.
func AllPermissions() []string {
	var all []string
	for _, area := range permissionAreas {
		for _, p := range []string{area.View, area.Manage} {
			if p != "" {
				all = append(all, p)
			}
		}
	}
	return all
}

// KnownPermission reports whether p is a permission of the catalog.
func KnownPermission(p string) bool {
	return slices.Contains(AllPermissions(), p)
}

// implied returns the permissions p implies, itself excluded: the view of its
// area for a manage, the panel for trading:operate and supervisor:chat.
func implied(p string) []string {
	switch p {
	case PermTradingOperate, PermSupervisorChat:
		return []string{PermDashboardView}
	}
	if area, ok := strings.CutSuffix(p, ":manage"); ok {
		return []string{area + ":view"}
	}
	return nil
}

// Effective returns the permissions granted adds up to, the implied ones (see
// implied) included, in the order of the catalog: the unknown ones dropped.
func Effective(granted []string) []string {
	set := map[string]bool{}
	for _, p := range granted {
		set[p] = true
		for _, more := range implied(p) {
			set[more] = true
		}
	}
	effective := []string{}
	for _, p := range AllPermissions() {
		if set[p] {
			effective = append(effective, p)
		}
	}
	return effective
}
