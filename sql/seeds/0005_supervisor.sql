-- Serialized with the seeds of any other migrator running at once (eg: two
-- controllers starting): the file runs as one implicit transaction, holding
-- the lock to its end, and the statements after it see what the other one
-- inserted. The same key in every seed file.
SELECT pg_advisory_xact_lock(hashtext('xtrader.seeds'));

-- The supervisor of the agents, off until it has a model and its key (see the
-- settings of the dashboard): an analysis every 15 minutes once enabled, an
-- answer of the model taking 2 minutes at most; alone, it may block the
-- entries of an agent and stop it once it holds nothing, 3 actions an
-- analysis at most, every other action proposed to the users; its criteria,
-- without a fixed financial limit, edited on the dashboard. No credential
-- here.
INSERT INTO supervisor_settings (id, enabled, interval_seconds, timeout_seconds, instructions, autonomy)
VALUES (1, FALSE, 900, 120, $instructions$Critérios de supervisão dos agentes (edite à vontade).

Operacional:
- agente ligado que não está rodando, reinicia em loop, não conseguiu ser lançado ou saiu sozinho;
- registros do agente que não chegam ao banco (outbox com pendências antigas ou falhando);
- prompt recusado por um agente LLM;
- posição aberta sem stop loss na corretora, ou de um agente que não está rodando (sem gerenciamento);
- conta que não pôde ser lida: sem esse dado, não conclua que o agente está zerado.

Risco:
- vários agentes na mesma direção do mesmo ativo e da mesma conta: some a exposição e compare com o saldo da conta, na moeda dela;
- posição maior do que os limites configurados no próprio agente (lote, max-risk-pct, alavancagem, limites diários do prompt);
- perda, aberta ou realizada, que se aproxima dos limites diários configurados no agente.

Desempenho:
- só julgue desempenho com amostra suficiente: com poucas operações fechadas, o resultado não diz nada sobre a estratégia;
- compare o agente com ele mesmo (acerto, fator de lucro, expectativa, queda) e cite o período e a moeda;
- nunca compare nem some valores de moedas diferentes.

Ações:
- prefira bloquear novas entradas a parar o agente;
- pare o agente quando um problema operacional o impede de funcionar, ou quando há risco que ele não gerencia;
- fechar posições é sempre uma proposta ao usuário, nunca uma ação sua;
- justifique cada ação com os números do retrato, a moeda e o horário dos dados.$instructions$,
        '{"block_entries": true, "stop_when_flat": true, "cancel_pending": false, "max_actions_per_run": 3}')
ON CONFLICT (id) DO NOTHING;
