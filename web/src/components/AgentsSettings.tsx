import { useId, useState } from "react";
import { deleteAgent, restartAgent, updateAgentSettings } from "../api/client";
import type { AgentSettings, AgentSettingsRequest, PromptInfo } from "../api/types";
import {
  type AgentGroup,
  agentGroups,
  agentKindLabel,
  agentNote,
  agentState,
  agentSummary,
  promptStatusLabel,
  tradePanelHint,
} from "../lib/settings";
import { AgentForm } from "./AgentForm";
import { AgentResultsDialog } from "./AgentResultsDialog";
import { ConfirmButton } from "./ConfirmButton";
import { SecretField } from "./SecretField";
import { Select } from "./Select";
import { StatusIcon } from "./StatusIcon";
import { Switch } from "./Switch";

interface Props {
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
 * one is deleted, its orders and its decisions kept.
 */
export function AgentsSettings({ agents, prompts, onChange, onReload }: Props) {
  const id = useId();
  const [editing, setEditing] = useState<Editing>(null);
  // the kind of the group shown: the first group until another is chosen
  const [kind, setKind] = useState<string | null>(null);
  const groups = agentGroups(agents);
  const shown = groups.find((g) => g.kind === kind) ?? groups[0];
  const strategies = groups.filter((g) => g.kind !== "llm");
  // the agent edited as the list has it now, eg: running or not
  const edited =
    editing && editing !== "new"
      ? (agents.find((a) => a.name === editing.agent.name) ?? editing.agent)
      : null;

  /** The button showing a group: its agents, and how many of them are enabled. */
  const groupButton = (group: AgentGroup) => {
    const enabled = group.enabled === 1 ? "1 ativo" : `${group.enabled} ativos`;
    return (
      <button
        key={group.kind}
        type="button"
        className={group === shown ? "agent-group active" : "agent-group"}
        aria-pressed={group === shown}
        aria-controls={`${id}-agents`}
        aria-label={`${group.label}: ${group.agents.length} ${group.agents.length === 1 ? "agente" : "agentes"}, ${enabled}`}
        onClick={() => setKind(group.kind)}
      >
        {group.label}
        <span className="agent-group-count">{group.agents.length}</span>
        <span className={group.enabled > 0 ? "agent-group-enabled on" : "agent-group-enabled"}>
          {enabled}
        </span>
      </button>
    );
  };

  return (
    <div className="settings-agents">
      <div className="settings-toolbar">
        <p className="settings-hint">
          Num agente em execução, Ativo, Painel de trading, Controle via Telegram e o prompt valem
          na hora; as demais alterações, quando ele for reiniciado.
        </p>
        <button type="button" className="button primary" onClick={() => setEditing("new")}>
          Novo agente
        </button>
      </div>
      {!shown ? (
        <p className="empty">
          Nenhum agente cadastrado: crie um em Novo agente, ou importe os da versão anterior com o
          migrator (import).
        </p>
      ) : (
        <>
          <fieldset className="agent-groups" aria-label="Grupos de agentes">
            {groups.filter((g) => g.kind === "llm").map(groupButton)}
            {strategies.length > 0 && (
              <fieldset className="agent-groups-set" aria-labelledby={`${id}-strategies`}>
                <span id={`${id}-strategies`} className="field-label">
                  Estratégias
                </span>
                {strategies.map(groupButton)}
              </fieldset>
            )}
          </fieldset>
          <div id={`${id}-agents`} className="settings-grid">
            {shown.agents.map((agent) => (
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
  const [results, setResults] = useState(false);
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
  const secret =
    (field: "llm_api_key" | "bridge_token" | "calendar_token") => async (value: string) => {
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
  const channelsTitle = paper
    ? "No modo paper, o painel de controle fica ativo em todos os canais"
    : undefined;
  const deleteTitle = agent.enabled
    ? "Desative o agente antes de excluí-lo"
    : stopping
      ? "O agente ainda está parando: aguarde para excluí-lo"
      : "Exclui o agente; as ordens e as decisões dele são mantidas";
  const footnote = agentNote(agent);

  return (
    <article className={`card settings-agent ${state.tone}`} aria-label={`Agente ${agent.name}`}>
      <header className="agent-head">
        <div className="agent-title">
          <h2 className="agent-name" title={agent.name}>
            {agent.name}
          </h2>
          <span className={`status-chip ${state.tone}`}>
            <StatusIcon mark={state.mark} />
            {state.label}
          </span>
        </div>
        <div className="agent-meta">
          <span>{agentKindLabel(agent.kind)}</span>
          <span className="badge">{agent.mode}</span>
          {summary.length > 0 && (
            <span className="agent-summary">
              {summary.map((part) => (
                <span key={part.label} className="mono" title={part.label}>
                  {part.value}
                </span>
              ))}
            </span>
          )}
        </div>
        <span className="settings-agent-address mono" title="Endereço da API">
          {agent.api_address}
        </span>
      </header>

      <div className="agent-switches">
        <Switch
          label="Ativo"
          checked={agent.enabled}
          disabled={busy}
          title="Ligado, o controller o inicia; desligado, o para"
          onChange={(enabled) => change({ enabled })}
        />
        <Switch
          label="Painel de trading"
          checked={paper || agent.trade_panel}
          disabled={busy || paper}
          title={channelsTitle ?? tradePanelHint}
          onChange={(trade_panel) => change({ trade_panel })}
        />
        <Switch
          label="Controle via Telegram"
          checked={paper || agent.telegram_control}
          disabled={busy || paper}
          title={channelsTitle}
          onChange={(telegram_control) => change({ telegram_control })}
        />
      </div>

      {llm && (
        <div className="field settings-prompt">
          <span className="field-label">Prompt</span>
          <Select
            caption="Prompt"
            value={agent.prompt_id ? String(agent.prompt_id) : DEFAULT_PROMPT}
            options={options}
            onChange={(value) =>
              change(
                value === DEFAULT_PROMPT ? { default_prompt: true } : { prompt_id: Number(value) },
              )
            }
          />
          {status && <p className={`settings-status ${status.tone}`}>{status.text}</p>}
          <p className="settings-hint">A troca vale a partir da próxima análise, sem reiniciar.</p>
        </div>
      )}

      <div className="settings-secrets">
        {llm && (
          <SecretField
            label="Chave da API do LLM"
            set={agent.secrets.llm_api_key}
            disabled={busy}
            onSave={secret("llm_api_key")}
          />
        )}
        <SecretField
          label="Token da bridge MT5"
          set={agent.secrets.bridge_token}
          disabled={busy}
          onSave={secret("bridge_token")}
        />
        {llm && (
          <SecretField
            label="Token do calendário (MCP)"
            set={agent.secrets.calendar_token}
            disabled={busy}
            onSave={secret("calendar_token")}
          />
        )}
      </div>

      <footer className="settings-footer">
        {footnote && <span className="settings-hint">{footnote}</span>}
        <span className="settings-actions">
          <button
            type="button"
            className="mini-btn"
            aria-label={`Resultados de ${agent.name}`}
            title="Os resultados por período, e a redefinição deles"
            onClick={() => setResults(true)}
          >
            Resultados
          </button>
          <button
            type="button"
            className="mini-btn"
            aria-label={`Editar ${agent.name}`}
            disabled={busy}
            onClick={onEdit}
          >
            Editar
          </button>
          <ConfirmButton
            label="Excluir"
            question={`Excluir ${agent.name}?`}
            disabled={busy || agent.enabled || stopping}
            title={deleteTitle}
            onConfirm={remove}
          />
          <button
            type="button"
            className={agent.restart_pending ? "mini-btn accent" : "mini-btn"}
            disabled={busy || !agent.enabled || !agent.launched}
            onClick={restart}
          >
            Reiniciar
          </button>
        </span>
      </footer>
      {error && (
        <p className="control-feedback error" role="alert">
          {error}
        </p>
      )}
      {results && <AgentResultsDialog agent={agent} onClose={() => setResults(false)} />}
    </article>
  );
}
