import { useId, useState } from "react";
import {
  deleteAgent,
  renameAgent,
  restartAgent,
  setAgentEntries,
  updateAgentSettings,
} from "../api/client";
import type { AgentSettings, AgentSettingsRequest, PromptInfo } from "../api/types";
import { PermAgentsManage, PermLimitsManage, PermTradingOperate } from "../api/types";
import {
  type AgentGroup,
  agentGroups,
  agentKindLabel,
  agentNote,
  agentState,
  agentSummary,
  outboxNote,
  promptStatusLabel,
  reanalysisHint,
  searchAgents,
  tradePanelHint,
} from "../lib/settings";
import { byActor } from "../lib/supervisor";
import { useCan } from "./AccessContext";
import { AgentForm } from "./AgentForm";
import { AgentResultsDialog } from "./AgentResultsDialog";
import { AgentsParamDialog } from "./AgentsParamDialog";
import { ConfirmButton } from "./ConfirmButton";
import { DailyLimitDialog } from "./DailyLimitDialog";
import { Icon } from "./Icon";
import { PageHeader } from "./PageHeader";
import { ProtectionDialog } from "./ProtectionDialog";
import { RenameAgentDialog } from "./RenameAgentDialog";
import { SearchField } from "./SearchField";
import { SecretField } from "./SecretField";
import { Select } from "./Select";
import type { SectionIntro } from "./SettingsPage";
import { EmptyState } from "./States";
import { StatusIcon } from "./StatusIcon";
import { Switch } from "./Switch";

interface Props {
  /** The title of the section and what it is for. */
  intro: SectionIntro;
  agents: AgentSettings[];
  prompts: PromptInfo[];
  /** Replaces an agent changed by the page. */
  onChange: (agent: AgentSettings) => void;
  /** Loads the agents again, one created or deleted. */
  onReload: () => void;
}

/** The value of the default prompt among the prompts of the select. */
const DEFAULT_PROMPT = "default";

/** What the form shows: a new agent, or one of the list, as it was when opened. */
type Editing = { agent: AgentSettings } | "new" | null;

/**
 * The agents of the controller, a group at a time: the LLM ones, apart from
 * the strategies, split by kind. Of each agent: whether it runs, its switches,
 * the prompt of an LLM agent with what came of its last switch, its secrets
 * (written, never read) and its restart, the settings of its launch changed.
 * New agents and the settings of each one are edited in a dialog; a disabled
 * one is deleted, its orders and its decisions kept. A search lists the
 * agents of every group matching it, by their name, kind, strategy, pairs or
 * model, in place of the group shown.
 */
export function AgentsSettings({ intro, agents, prompts, onChange, onReload }: Props) {
  const manageAgents = useCan(PermAgentsManage);
  const id = useId();
  const [editing, setEditing] = useState<Editing>(null);
  const [parametrizing, setParametrizing] = useState(false);
  // the kind of the group shown: the first group until another is chosen
  const [kind, setKind] = useState<string | null>(null);
  // the search typed: the agents of every group matching it, in place of the group
  const [query, setQuery] = useState("");
  const groups = agentGroups(agents);
  const shown = groups.find((g) => g.kind === kind) ?? groups[0];
  const found = query.trim() === "" ? null : searchAgents(agents, query);
  const strategies = groups.filter((g) => g.kind !== "llm");
  // the agent edited as the list has it now, eg: running or not
  const edited =
    editing && editing !== "new"
      ? (agents.find((a) => a.name === editing.agent.name) ?? editing.agent)
      : null;

  /** The button showing a group: its agents, and how many of them are enabled. */
  const groupButton = (group: AgentGroup) => {
    const enabled = group.enabled === 1 ? "1 ligado" : `${group.enabled} ligados`;
    return (
      <button
        key={group.kind}
        type="button"
        className="filter-chip"
        aria-pressed={found === null && group === shown}
        aria-controls={`${id}-agents`}
        aria-label={`${group.label}: ${group.agents.length} ${group.agents.length === 1 ? "agente" : "agentes"}, ${enabled}`}
        onClick={() => {
          setKind(group.kind);
          setQuery(""); // the group, not the search
        }}
      >
        {group.label}
        <span className="filter-count">{group.agents.length}</span>
        <span className={group.enabled > 0 ? "filter-note on" : "filter-note"}>{enabled}</span>
      </button>
    );
  };

  const create = manageAgents ? (
    <button type="button" className="btn btn-primary" onClick={() => setEditing("new")}>
      <Icon name="plus" />
      Novo agente
    </button>
  ) : undefined;
  const actions = (
    <div className="agents-actions">
      {manageAgents && agents.length > 0 && (
        <button type="button" className="btn" onClick={() => setParametrizing(true)}>
          <Icon name="settings" />
          Parametrizador global
        </button>
      )}
      {create}
    </div>
  );

  return (
    <div className="settings-section">
      <PageHeader
        eyebrow="Configurações"
        title={intro.title}
        description={intro.description}
        actions={actions}
      />
      {!shown ? (
        <div className="card">
          <EmptyState icon="bot" title="Nenhum agente ainda" action={create} />
        </div>
      ) : (
        <>
          <SearchField
            value={query}
            onChange={setQuery}
            label="Buscar agentes em todos os grupos"
            placeholder="Buscar agente por nome, estratégia, ativo ou modelo"
            controls={`${id}-agents`}
            note={
              found === null
                ? null
                : found.length === 0
                  ? `Nenhum agente encontrado para “${query.trim()}”.`
                  : `${found.length === 1 ? "1 agente encontrado" : `${found.length} agentes encontrados`} em todos os grupos.`
            }
          />
          <fieldset className="filter-bar" aria-label="Grupos de agentes">
            {groups.filter((g) => g.kind === "llm").map(groupButton)}
            {strategies.length > 0 && (
              <fieldset className="filter-set" aria-labelledby={`${id}-strategies`}>
                <span id={`${id}-strategies`} className="filter-heading">
                  Estratégias
                </span>
                {strategies.map(groupButton)}
              </fieldset>
            )}
          </fieldset>
          <div id={`${id}-agents`} className="agent-grid">
            {(found ?? shown.agents).map((agent) => (
              <AgentCard
                key={agent.name}
                agent={agent}
                prompts={prompts}
                onChange={onChange}
                onEdit={() => setEditing({ agent })}
                onDeleted={onReload}
              />
            ))}
          </div>
        </>
      )}
      {editing && (
        <AgentForm
          agent={edited}
          prompts={prompts}
          onSaved={
            editing === "new"
              ? (agent) => {
                  setKind(agent.kind); // the group of the agent created
                  onReload();
                }
              : onChange
          }
          onClose={() => setEditing(null)}
        />
      )}
      {parametrizing && (
        <AgentsParamDialog
          agents={agents}
          onApplied={onReload}
          onClose={() => setParametrizing(false)}
        />
      )}
    </div>
  );
}

/**
 * The card of an agent: its name and its state, what tells it apart, its
 * switches, its prompt, its secrets, and its actions at the bottom.
 */
function AgentCard({
  agent,
  prompts,
  onChange,
  onEdit,
  onDeleted,
}: {
  agent: AgentSettings;
  prompts: PromptInfo[];
  onChange: (agent: AgentSettings) => void;
  /** Opens the form of the agent. */
  onEdit: () => void;
  onDeleted: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // changed by a user allowed to only: its entries by one allowed to operate,
  // its daily limit by one allowed to change the limits
  const manage = useCan(PermAgentsManage);
  const operate = useCan(PermTradingOperate);
  const setLimits = useCan(PermLimitsManage);
  const [results, setResults] = useState(false);
  const [protection, setProtection] = useState(false);
  const [dailyLimit, setDailyLimit] = useState(false);
  const [renaming, setRenaming] = useState(false);
  const state = agentState(agent);
  const status = promptStatusLabel(agent.prompt_status);
  const summary = agentSummary(agent);
  const paper = agent.mode === "paper";
  const llm = agent.kind === "llm";
  // disabled, its process is stopped before it may be deleted
  const stopping = !agent.enabled && agent.running;

  const change = async (request: AgentSettingsRequest) => {
    setBusy(true);
    setError(null);
    try {
      onChange(await updateAgentSettings(agent.name, request));
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  const secret = (field: "llm_api_key" | "bridge_token") => async (value: string) => {
    onChange(await updateAgentSettings(agent.name, { [field]: value }));
  };
  const restart = async () => {
    setBusy(true);
    setError(null);
    try {
      onChange(await restartAgent(agent.name));
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  const entries = async (blocked: boolean) => {
    setBusy(true);
    setError(null);
    try {
      const result = await setAgentEntries(agent.name, { blocked });
      if (result.action.status !== "succeeded") {
        setError(
          `${blocked ? "Bloqueio salvo" : "Liberação salva"}, sem confirmação do agente: ${
            result.action.result?.items.map((item) => item.detail).join("; ") ??
            result.action.status
          }`,
        );
      }
      onDeleted(); // listed again, with its supervision
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  const remove = async () => {
    setBusy(true);
    setError(null);
    try {
      await deleteAgent(agent.name);
      onDeleted();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const options = [
    { value: DEFAULT_PROMPT, label: "default (embutido)" },
    ...prompts.map((p) => ({ value: String(p.id), label: p.name, hint: `v${p.version}` })),
  ];
  const channelsTitle = paper ? "Em paper, fica sempre ligado" : undefined;
  const renameTitle = agent.enabled
    ? "Desligue o agente antes de renomeá-lo"
    : stopping
      ? "O agente está parando: aguarde para renomeá-lo"
      : "Muda só o nome: a conta, as ordens e as decisões continuam dele";
  const deleteTitle = agent.enabled
    ? "Desligue o agente antes de excluí-lo"
    : stopping
      ? "O agente está parando: aguarde para excluí-lo"
      : "As ordens e as decisões do agente são mantidas";
  const notes = [agentNote(agent), outboxNote(agent)].filter(Boolean);
  const blocked = Boolean(agent.supervision?.entries_blocked);

  return (
    <article className={`card agent-card ${state.tone}`} aria-label={`Agente ${agent.name}`}>
      <header className="agent-card-head">
        <div className="agent-title">
          <h3 className="agent-name" title={agent.name}>
            {agent.name}
          </h3>
          <span className={`status-chip ${state.tone}`}>
            <StatusIcon mark={state.mark} />
            {state.label}
          </span>
        </div>
        <div className="agent-meta">
          <span className="tag">{agentKindLabel(agent.kind)}</span>
          <span
            className={paper ? "tag" : "tag tag-live"}
            title={paper ? "Carteira simulada" : "Conta MT5"}
          >
            {agent.mode}
          </span>
          {summary.map((part) => (
            <span key={part.label} className="tag tag-mono" title={part.label}>
              {part.value}
            </span>
          ))}
        </div>
        <span className="agent-address mono" title="Endereço da API">
          {agent.api_address}
        </span>
      </header>

      <div className="agent-block">
        <div className="agent-switches">
          <Switch
            label="Ligado"
            checked={agent.enabled}
            disabled={busy || !manage}
            title="Ligado, o controller inicia o agente; desligado, o agente para"
            onChange={(enabled) => change({ enabled })}
          />
          <Switch
            label="Painel de trading"
            checked={paper || agent.trade_panel}
            disabled={busy || paper || !manage}
            title={channelsTitle ?? tradePanelHint}
            onChange={(trade_panel) => change({ trade_panel })}
          />
          <Switch
            label="Controle via Telegram"
            checked={paper || agent.telegram_control}
            disabled={busy || paper || !manage}
            title={channelsTitle}
            onChange={(telegram_control) => change({ telegram_control })}
          />
          {llm && (
            <Switch
              label="Permitir reanálise"
              checked={agent.reanalysis}
              disabled={busy || !manage}
              title={reanalysisHint}
              onChange={(reanalysis) => change({ reanalysis })}
            />
          )}
        </div>
      </div>

      {llm && (
        <div className="agent-block field">
          <span className="field-label">Prompt</span>
          {manage ? (
            <Select
              caption="Prompt"
              value={agent.prompt_id ? String(agent.prompt_id) : DEFAULT_PROMPT}
              options={options}
              onChange={(value) =>
                change(
                  value === DEFAULT_PROMPT
                    ? { default_prompt: true }
                    : { prompt_id: Number(value) },
                )
              }
            />
          ) : (
            <span className="mono">
              {options.find(
                (o) => o.value === (agent.prompt_id ? String(agent.prompt_id) : DEFAULT_PROMPT),
              )?.label ?? `prompt ${agent.prompt_id}`}
            </span>
          )}
          {status && (
            <p
              className={
                status.tone === "on"
                  ? "msg msg-ok"
                  : status.tone === "warn"
                    ? "msg msg-warn"
                    : "msg"
              }
            >
              {status.text}
            </p>
          )}
          <p className="field-hint">Vale a partir da próxima análise, sem reiniciar.</p>
        </div>
      )}

      <div className="agent-block">
        <h4 className="section-title">
          <Icon name="key" size={13} />
          Segredos
        </h4>
        <div className="secret-list">
          {llm && (
            <SecretField
              label="Chave da API do LLM"
              set={agent.secrets.llm_api_key}
              disabled={busy || !manage}
              onSave={secret("llm_api_key")}
            />
          )}
          {agent.mt5_account_id ? (
            <p className="field-hint">
              Bridge e token vêm da conta MT5 associada (Configurações › Contas MT5).
            </p>
          ) : (
            <SecretField
              label="Token da bridge MT5"
              set={agent.secrets.bridge_token}
              disabled={busy || !manage}
              onSave={secret("bridge_token")}
            />
          )}
        </div>
      </div>

      {agent.supervision && (agent.supervision.stop_state !== "none" || blocked) && (
        <div className="agent-block agent-supervision">
          {agent.supervision.stop_state === "stopped" && (
            <p className="msg msg-error">
              {`Parado ${byActor(agent.supervision.stop_by)}`.trim()}:{" "}
              {agent.supervision.stop_reason || "sem motivo registrado"}. Ligue o agente para
              retomá-lo.
            </p>
          )}
          {agent.supervision.stop_state === "pending_flat" && (
            <p className="msg msg-warn">
              Parada pendente: o agente desliga quando ficar sem posição e sem ordem pendente
              {agent.supervision.stop_reason ? ` (${agent.supervision.stop_reason})` : ""}.
            </p>
          )}
          {blocked && (
            <p className="msg msg-warn">
              {`Entradas bloqueadas ${byActor(agent.supervision.entries_by)}`.trim()}:{" "}
              {agent.supervision.entries_reason || "sem motivo registrado"}. O agente continua
              gerenciando as posições abertas.
            </p>
          )}
        </div>
      )}

      <footer className="agent-card-foot">
        {notes.map((note) => (
          <p key={note} className="agent-note">
            {note}
          </p>
        ))}
        <div className="agent-actions">
          <button
            type="button"
            className="btn btn-sm"
            aria-label={`Resultados de ${agent.name}`}
            title="Ver e redefinir os resultados por período"
            onClick={() => setResults(true)}
          >
            <Icon name="results" size={14} />
            Resultados
          </button>
          {manage && (
            <button
              type="button"
              className={agentProtected(agent) ? "btn btn-sm protection-on" : "btn btn-sm"}
              aria-label={`Proteção de posições de ${agent.name}`}
              title="Breakeven e trailing stop das posições, e o que o XTrader AI pode acionar"
              onClick={() => setProtection(true)}
            >
              <Icon name="shield" size={14} />
              Proteção
            </button>
          )}
          {setLimits && (
            <button
              type="button"
              className={agent.daily_limit?.enabled ? "btn btn-sm protection-on" : "btn btn-sm"}
              aria-label={`Limite diário de ${agent.name}`}
              title="Perda e ganho máximos do dia deste agente: atingido um, fecha as posições dele e bloqueia as entradas até 00:00"
              onClick={() => setDailyLimit(true)}
            >
              <Icon name="alert" size={14} />
              Limite diário
            </button>
          )}
          {manage && (
            <button
              type="button"
              className="btn btn-sm"
              aria-label={`Editar ${agent.name}`}
              disabled={busy}
              onClick={onEdit}
            >
              <Icon name="edit" size={14} />
              Editar
            </button>
          )}
          {manage && (
            <button
              type="button"
              className={agent.restart_pending ? "btn btn-sm btn-primary" : "btn btn-sm"}
              aria-label={`Reiniciar ${agent.name}`}
              title={
                agent.restart_pending
                  ? "Aplica as configurações de inicialização alteradas"
                  : undefined
              }
              disabled={busy || !agent.enabled || !agent.launched}
              onClick={restart}
            >
              <Icon name="restart" size={14} />
              Reiniciar
            </button>
          )}
          {!operate ? null : blocked ? (
            <button
              type="button"
              className="btn btn-sm"
              aria-label={`Liberar as entradas de ${agent.name}`}
              title="O agente volta a abrir posições conforme a estratégia"
              disabled={busy}
              onClick={() => entries(false)}
            >
              <Icon name="lock" size={14} />
              Liberar entradas
            </button>
          ) : (
            <ConfirmButton
              label="Bloquear entradas"
              icon="lock"
              ariaLabel={`Bloquear as entradas de ${agent.name}`}
              question="Bloquear novas entradas?"
              title="O agente não abre nem aumenta posições; continua gerenciando as que tem"
              disabled={busy}
              onConfirm={() => entries(true)}
            />
          )}
          {manage && (
            <button
              type="button"
              className="btn btn-sm"
              aria-label={`Renomear ${agent.name}`}
              title={renameTitle}
              disabled={busy || agent.enabled || stopping}
              onClick={() => setRenaming(true)}
            >
              <Icon name="rename" size={14} />
              Renomear
            </button>
          )}
          {manage && (
            <span className="agent-actions-end">
              <ConfirmButton
                label="Excluir"
                icon="trash"
                ariaLabel={`Excluir ${agent.name}`}
                question={`Excluir ${agent.name}?`}
                disabled={busy || agent.enabled || stopping}
                title={deleteTitle}
                onConfirm={remove}
              />
            </span>
          )}
        </div>
      </footer>
      {error && (
        <p className="msg msg-error" role="alert">
          {error}
        </p>
      )}
      {results && <AgentResultsDialog agent={agent} onClose={() => setResults(false)} />}
      {protection && (
        <ProtectionDialog agent={agent} onChange={onChange} onClose={() => setProtection(false)} />
      )}
      {dailyLimit && (
        <DailyLimitDialog agent={agent} onChange={onChange} onClose={() => setDailyLimit(false)} />
      )}
      {renaming && (
        <RenameAgentDialog
          name={agent.name}
          onConfirm={async (name) => {
            await renameAgent(agent.name, name);
            onDeleted(); // listed again under its new name
          }}
          onClose={() => setRenaming(false)}
        />
      )}
    </article>
  );
}

/** Whether the protection of the positions of agent runs, or may be armed by the XTrader AI. */
function agentProtected(agent: AgentSettings): boolean {
  const { breakeven, trailing } = agent.protection;
  return breakeven.enabled || breakeven.supervisor || trailing.enabled || trailing.supervisor;
}
