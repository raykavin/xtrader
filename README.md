# XTrader

[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)](go.mod)
[![React](https://img.shields.io/badge/React-19-61DAFB?logo=react&logoColor=black)](web/package.json)
[![TypeScript](https://img.shields.io/badge/TypeScript-7-3178C6?logo=typescript&logoColor=white)](web/package.json)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-18-4169E1?logo=postgresql&logoColor=white)](sql/migrations)
[![Docker](https://img.shields.io/badge/Docker-Compose-2496ED?logo=docker&logoColor=white)](deploy/docker)
[![MetaTrader 5](https://img.shields.io/badge/MetaTrader-5-1E88E5)](tools/mt5bridge)
[![LLM](https://img.shields.io/badge/LLM-OpenAI%20%7C%20Anthropic-412991)](agents/llm)
[![Telegram](https://img.shields.io/badge/Telegram-bot-26A5E4?logo=telegram&logoColor=white)](controller/telegram.go)

Plataforma de trading automatizado em Go para símbolos do **MetaTrader 5** (Exness). Um **controller** lança e supervisiona **agentes**:

- um agente que opera as decisões de um **modelo de linguagem** (OpenAI ou Anthropic);
- agentes de **estratégias clássicas**, divididos em quatro famílias.

O controller também serve o **painel web** e o **bot do Telegram**. Toda a configuração operacional e os registros ficam no **PostgreSQL**.

> [!WARNING]
> Trading automatizado envolve risco real de perda. Valide qualquer agente em backtest e em conta demo antes de uma conta real. Uma conta real só é aceita com a opção **Permitir conta real** ligada no agente.

## Sumário

- [Recursos](#recursos)
- [Arquitetura](#arquitetura)
- [Estrutura do repositório](#estrutura-do-repositório)
- [Requisitos](#requisitos)
- [Início rápido com Docker](#início-rápido-com-docker)
- [Execução local (host)](#execução-local-host)
- [Configuração](#configuração)
- [Uso](#uso)
  - [Painel web](#painel-web)
  - [Agentes e parâmetros](#agentes-e-parâmetros)
  - [Redefinir os resultados](#redefinir-os-resultados)
  - [Prompts](#prompts)
  - [Limites diários](#limites-diários)
  - [Telegram](#telegram)
  - [Backtest e dados históricos](#backtest-e-dados-históricos)
  - [Migração da versão anterior](#migração-da-versão-anterior)
- [Desenvolvimento](#desenvolvimento)
- [API](#api)
- [Segurança](#segurança)

## Recursos

### Agente LLM

O agente `agents/llm/llmagent` pede uma decisão ao modelo e a aplica dentro de regras de risco.

- **Contexto enviado ao modelo:**
  - candles e indicadores;
  - estrutura de preço e níveis;
  - timeframes maiores;
  - sessão de mercado;
  - mercados correlacionados;
  - calendário econômico;
  - memória das últimas decisões e operações.
- **Decisões possíveis:** entrar, sair, manter, mover stop loss e take profit, ou esperar. Cada entrada leva stop loss (e take profit, conforme o prompt) na própria ordem.
- **Quando consultar:** uma decisão por fechamento de candle ou, com `llm-consult-interval`, numa agenda própria (por exemplo, a cada 10 min). Há também a **reanálise sob demanda**, pelo painel ou pelo Telegram, com uma nota para o modelo.
- **Início do agente:** ao (re)iniciar, os ativos são analisados de uma vez, em paralelo, com as mesmas regras das outras análises: um ativo sem entrada possível (por exemplo, com a tendência maior exigida e mista) não é consultado, e a análise aparece no painel como "Não consultado", com o motivo. Cada decisão é aplicada no tick seguinte do ativo.
- **Prompts:** editáveis e trocados com o agente rodando. A troca vale a partir da próxima análise, e cada decisão registra o prompt e a versão usados.
- **Proteções:**
  - confiança mínima;
  - risco:retorno mínimo;
  - spread máximo em ATR;
  - distância mínima do stop;
  - bloqueio perto de notícias de alto impacto;
  - limites diários de ganho e de perda por ativo, definidos no prompt (veja [Limites diários](#limites-diários)).
- **Histórico do dia:** o contexto leva as posições e as ordens do ativo no dia operacional (`daily_history`), com o resultado de cada posição.
- **Calendário econômico:** lido pelo servidor MCP do terminal MT5.

### Agentes de estratégia

| Família    | Binário                | Estratégias                                                                                                                                                                           |
| ---------- | ---------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Tendência  | `agents/trendagent`    | `trendconfluence`, `matrend`, `emapullback`, `emacross`, `ema200`, `ribbon`, `supertrend`, `emasupertrend`, `psarema`, `atrtrailing`, `ichimoku`, `ichimokuadx`, `mtftrend`, `mtfema` |
| Momentum   | `agents/momentumagent` | `adxema`, `macdtrend`, `emamacd`, `emarsiadx`, `rsitrend`, `stochpullback`, `papullback`                                                                                              |
| Rompimento | `agents/breakoutagent` | `atrbreakout`, `donchian`, `channel`, `adxdonchian`, `donchianatr`, `bbbreakout`, `keltner`, `volumebreakout`, `highlow`, `momentumbreakout`                                          |
| Intraday   | `agents/intradayagent` | `orb`, `asialondon`, `vwap`, `sweep`, `zscore`                                                                                                                                        |

Cada agente roda em modo `live` (lançado pelo controller) ou `paper`. Pela linha de comando, também roda backtests.

### Controller

- **Supervisão:** lança os agentes ligados como processos filhos, com um token novo a cada início, e os reinicia se caírem.
- **Configuração:** entrega aos agentes a configuração deles por uma API interna, só no loopback.
- **Registros:** recebe dos agentes as ordens, as decisões e o journal por um **outbox durável**. O agente grava localmente e envia depois, e o fluxo da estratégia nunca espera o banco.
- **Interfaces:** serve o painel web (login com sessão e CSRF) e o bot do Telegram.

### Painel web

- **Painel:**
  - gráfico de preço com as ordens e as linhas de stop e take profit, e um botão (ícone de mira, no topo) que volta ao candle e ao preço atuais;
  - estatísticas e curva de patrimônio;
  - boleta de ordens;
  - decisões do modelo, com o contexto de cada uma, e as análises em que o modelo não foi consultado ("Não consultado"), com o motivo;
  - histórico de ordens.
- **Bot:** iniciar e parar o bot de cada agente no card do topo.
- **Configurações, em abas:**
  - **Agentes:** os LLM separados das estratégias, estas agrupadas por família; criar, editar, excluir, ligar e desligar, reiniciar e definir os segredos.
  - **Resultados** (botão no card do agente): os resultados por período e a redefinição deles. Veja [Redefinir os resultados](#redefinir-os-resultados).
  - **Prompts:** editor com histórico de versões.
  - **Telegram:** liga e desliga o bot, token e IDs autorizados.
  - **Usuários.**
- Tema claro e escuro, e layout responsivo.

### Persistência

- **PostgreSQL 18:**
  - usuários, com senha em bcrypt;
  - prompts com versões;
  - agentes, contas MT5 e configuração dos modelos;
  - Telegram;
  - ordens, decisões com anexos e journal;
  - períodos de apuração dos resultados de cada agente.
- **Segredos** (chaves de API e tokens) cifrados com AES-256-GCM.
- **Migrações SQL** versionadas, aplicadas pelo `cmd/migrator`.

## Arquitetura

```none
                       ┌──────────────── controller (cmd/controller) ────────────────┐
navegador ── HTTPS ──► │ painel web (/api, sessão + CSRF)   Telegram (IDs autorizados)│
                       │ Manager: liga/desliga os agentes do banco, tokens por start │
                       │ API interna dos agentes (loopback, Bearer por agente)       │──► PostgreSQL
                       └──────▲───────────────────────────────▲───────────────────────┘
                              │ config, runtime, outbox        │ proxy da API de cada agente
                     ┌────────┴────────┐              ┌────────┴────────┐
                     │ agente llm      │  ...         │ agente trend    │ ──► bridge MT5 ──► terminal MT5
                     └─────────────────┘              └─────────────────┘      (tools/mt5bridge)
```

- **`cmd/controller`:** o único processo que acessa o banco.
- **`cmd/migrator`:** aplica as migrações e os seeds (`gobox/database/migrate`) e importa, uma única vez, a configuração e os dados da versão anterior. O controller não migra: ele confere a versão do schema.
- **Agentes:** recebem a configuração pela API interna (`agentapi`). Os parâmetros viram as mesmas flags da linha de comando, e os segredos nunca passam por argumento.
- **`tools/mt5bridge`:** uma ponte HTTP em Python para o terminal MT5, que roda no Wine, dentro do container `mt5`.

## Estrutura do repositório

```none
agentapi/            contrato da API interna entre controller e agentes
agents/
  llm/               estratégia do modelo de linguagem (+ llmagent, o binário)
  strategies/        estratégias clássicas
  trendagent/ momentumagent/ breakoutagent/ intradayagent/   binários das famílias
  internal/          runner comum e cliente remoto (outbox, config)
cmd/
  controller/        painel, Telegram e supervisão dos agentes
  migrator/          migrações, seeds e import da versão anterior
  xtrader/           utilitários (download de candles)
config/ configs/     leitura do config.yml e o exemplo do host
controller/          implementação do controller
deploy/docker/       imagens, compose e scripts de instalação
exchange/ order/     MT5, paper wallet e controle de ordens
sql/                 migrações e seeds
store/               repositórios do PostgreSQL (GORM só para consultas)
tools/               bridge MT5, automator.sh, logs e métricas
ui/                  API de cada agente para o painel e DTOs (fonte dos tipos do web)
web/                 painel (React 19 + Vite + TypeScript)
```

## Requisitos

- **Docker e Docker Compose**, para a stack completa, que é o caminho recomendado.
- **Para desenvolver no host:**
  - Go 1.25+;
  - PostgreSQL 18;
  - [bun](https://bun.sh), para o painel;
  - um terminal MT5 com a bridge (`tools/mt5bridge/run.sh`), para os modos live e paper.
- **Uma conta MT5** (Exness) e, para o agente LLM, uma chave da OpenAI ou da Anthropic.

## Início rápido com Docker

A stack tem quatro serviços:

| Serviço      | Papel                                           |
| ------------ | ----------------------------------------------- |
| `postgres`   | banco                                           |
| `migrator`   | migrações e seeds, a cada subida                |
| `mt5`        | terminal no Wine, com a bridge e o servidor MCP |
| `controller` | painel, Telegram e agentes                      |

```bash
deploy/docker/build.sh                          # imagens xtrader-controller e xtrader-mt5
sudo deploy/docker/install.sh                   # instala em /var/containers/xtrader e gera a senha do banco e a chave dos segredos
sudo nano /var/containers/xtrader/services/mt5/.env         # conta MT5: MT5_LOGIN, MT5_SERVER, MT5_PASSWORD
sudo nano /var/containers/xtrader/services/controller/.env  # primeiro usuário do painel (XTRADER_BOOTSTRAP_*)
cd /var/containers/xtrader && sudo docker compose up -d
```

O painel fica em `http://<servidor>:8082`. Depois do login, crie os agentes em **Configurações > Agentes > Novo agente**. O passo a passo completo, com calendário econômico, portas, backup e migração, está em [deploy/docker/README.md](deploy/docker/README.md).

## Execução local (host)

```bash
# PostgreSQL 18 em container
docker run -d --name xtrader-postgres -e POSTGRES_USER=xtrader -e POSTGRES_DB=xtrader \
  -e POSTGRES_PASSWORD="$XTRADER_DB_PASSWORD" -p 127.0.0.1:5432:5432 postgres:18.6-trixie

export XTRADER_DB_PASSWORD='...'                                   # a senha do usuário xtrader
export XTRADER_SECRETS_KEY="$(head -c 32 /dev/urandom | base64)"   # uma vez: guarde, ela cifra os segredos
export XTRADER_BOOTSTRAP_USERNAME=admin XTRADER_BOOTSTRAP_PASSWORD='...'

cp configs/config.example.yml configs/config.yml
(cd web && bun install --frozen-lockfile && bun run build)   # o painel em web/dist
go run ./cmd/migrator -config configs/config.yml up          # schema e seeds (43 prompts)
go run ./cmd/controller -config configs/config.yml           # ou: tools/automator.sh start configs/config.yml
```

O `tools/automator.sh` roda o migrator e o controller numa sessão própria, que não para ao fechar o terminal, e oferece `start`, `stop`, `status` e `logs`.

## Configuração

O `config.yml` tem só o necessário para subir e acessar o banco. O resto fica no banco e é alterado pelo painel.

| No `config.yml`                                                                                                                                                                      | No banco (Configurações)                                                                               |
| ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------ |
| `listen`, `database`, `secrets_key`, `migrations`, `bootstrap` (primeiro usuário), `ui`, `agents` (API interna, diretório, comando de cada tipo), `services` (bridge do host), `log` | agentes (modo, endereço, conta MT5, modelo, parâmetros, segredos, prompt), prompts, Telegram, usuários |

| Variável                                                    | Uso                                                                                                     |
| ----------------------------------------------------------- | ------------------------------------------------------------------------------------------------------- |
| `XTRADER_DB_PASSWORD`                                       | senha do usuário `xtrader` do PostgreSQL                                                                |
| `XTRADER_SECRETS_KEY`                                       | 32 bytes em base64 que cifram os segredos do banco: sem ela, os segredos precisam ser definidos de novo |
| `XTRADER_BOOTSTRAP_USERNAME` / `XTRADER_BOOTSTRAP_PASSWORD` | primeiro usuário do painel, criado enquanto não houver nenhum                                           |
| `MT5_BRIDGE_TOKEN`                                          | token da bridge do host (`services` do config)                                                          |

Os `${VAR}` são trocados no texto antes da leitura do YAML, por isso ficam entre aspas simples. Assim, qualquer valor sem `'` funciona.

## Uso

### Painel web

- **Painel (`/`):** escolha o agente e o par no card do topo, onde também se inicia e para o bot.
  - A **boleta** envia ordens a mercado ou limitadas, com stop loss e take profit. As ordens pendentes podem ser arrastadas no gráfico.
  - A boleta, a movimentação das ordens e a reanálise dependem do switch **Painel de trading** do agente. Iniciar e parar o bot, não.
- **Configurações (`/settings`):** abas Agentes, Prompts, Telegram e Usuários, que também abrem por link (`/settings#prompts`).

### Agentes e parâmetros

**Criar um agente:** use **Configurações > Agentes > Novo agente**. O formulário pede:

- tipo e modo;
- endereço da API do agente;
- conta MT5 (bridge, magic, comentário);
- modelo, para o agente LLM;
- segredos;
- parâmetros.

**Quais mudanças valem na hora:** num agente rodando, **Ativo**, **Painel de trading**, **Controle via Telegram** e o prompt. As demais ficam como "reinício pendente", aplicadas pelo botão **Reiniciar**.

**Parâmetros:** são as flags do agente sem campo próprio. No formulário, o nome vai sem o traço, de um lado, e o valor do outro. Cada binário lista as suas flags com `-h`, por exemplo `go run ./agents/llm/llmagent -h`. Nas listas, os itens são separados por `;` (a vírgula continua aceita).

| Nome                   | Valor                                             | Efeito                                                                                  |
| ---------------------- | ------------------------------------------------- | --------------------------------------------------------------------------------------- |
| `pair`                 | `BTCUSDm;XAUUSDm`                                 | pares operados pelo agente                                                              |
| `timeframe`            | `30m`                                             | timeframe dos candles                                                                   |
| `lot`                  | `0.01` ou `BTCUSDm=0.03;XAUUSDm=0.01`             | lote fixo: um para todos ou um por par                                                  |
| `leverage`             | `400`                                             | teto de segurança: recusa uma posição maior que saldo × alavancagem                     |
| `llm-consult-interval` | `10m`                                             | consulta ao modelo a cada 10 min (mínimo de 10s); sem ele, uma por fechamento de candle |
| `llm-timeout`          | `2m`                                              | tempo máximo de uma requisição ao modelo                                                |
| `llm-correlated`       | `XAUUSDm=USTECm+DXYm;BTCUSDm=XAUUSDm+DXYm+USTECm` | mercados correlacionados de cada par (ou uma lista única, como `USTECm;DXYm`)           |
| `llm-candles`          | `60`                                              | candles recentes enviados ao modelo                                                     |
| `llm-setup`            | `free` ou `trend_pullback`                        | modo de decisão do modelo                                                               |

**Sobre `llm-consult-interval`:**

- A agenda segue o relógio: com `10m`, as consultas caem em :00, :10, :20 e assim por diante.
- Uma consulta que cai num fechamento usa o candle fechado. As outras usam o candle em andamento.
- Com um intervalo maior que o timeframe, os fechamentos do meio são pulados.
- Cada consulta é uma requisição cobrada.

### Redefinir os resultados

Em **Configurações → Agentes**, o botão **Resultados** do card do agente mostra os resultados por período e permite redefini-los.

- **O que zera:** Operações, Taxa de acerto, Lucro líquido e Fator de lucro de todos os ativos do agente. Depois da redefinição, os cards do painel e o `/profit` do Telegram contam só as operações fechadas a partir dela, pela hora de fechamento.
- **Depósito:** é o saldo inicial do novo período, na moeda informada (a da conta, por exemplo BRL). É um saldo único do agente: os resultados de todos os ativos somam nele, sem divisão entre ativos.
- **O que não muda:**
  - as ordens continuam no histórico;
  - posições abertas e ordens pendentes seguem na corretora, e uma posição aberta na hora da redefinição conta no novo período quando fechar;
  - os limites diários e a memória do LLM continuam considerando todas as operações.
- **Períodos anteriores:** ficam guardados no banco (`agent_periods`) e aparecem no mesmo diálogo, cada um com depósito, resultado por ativo e saldo final. Os resultados vêm das ordens do agente, então só aparecem com ele executando.

### Prompts

- **Onde ficam:** no banco, com histórico de versões. Os 43 de exemplo vêm pelo seed.
- **Cabeçalho:** as linhas `# Indicators:` e `# Take profit:` definem os indicadores enviados e o tratamento do take profit.
- **Troca:** pelo card do agente ou pelo Telegram (`/useprompt`), com o agente rodando, a partir da próxima análise.
- **Prompt inválido para o agente:** é recusado, o anterior continua, e o motivo aparece no card.

#### Limites diários

Um prompt pode definir limites diários do resultado de cada ativo, no cabeçalho, na moeda da conta:

```none
# Daily gain limit: 250 BRL
# Daily loss limit: 150 BRL
```

- **Resultado do dia:** a soma dos resultados das posições do ativo fechadas no dia operacional, como a corretora registra, com swap, comissão e taxa. Uma posição conta no dia em que fecha.
- **Dia operacional:** começa à meia-noite do fuso do agente, o `TZ` do `.env` (America/Belem na stack).
- **Ao atingir um limite:**
  - novas entradas no ativo ficam bloqueadas até o dia seguinte;
  - o modelo nem é consultado sem posição, inclusive nas reanálises;
  - uma entrada devolvida pelo modelo é recusada;
  - os outros ativos seguem livres.
- **Posição aberta:** continua gerenciada normalmente. O limite não força saída nem mexe no stop ou no alvo, e o resultado dela conta quando fecha.
- **Fechamento sem resultado registrado:** enquanto a corretora não registra o resultado de um fechamento do dia, as entradas esperam, em geral por segundos.
- **Operações manuais:** as da boleta e do Telegram contam no resultado, mas não são bloqueadas.
- **Moeda:** os limites precisam estar na moeda da conta; senão, o prompt é recusado.
- **Onde vale:** só em live no MT5, onde a corretora registra os resultados. Em backtest e paper, os limites não são aplicados, e um aviso vai para o log.

### Telegram

O bot responde só aos IDs autorizados em **Configurações > Telegram**. Os comandos de operação valem para o agente escolhido e exigem o **Controle via Telegram** dele ligado.

| Comando                          | O que faz                                                       |
| -------------------------------- | --------------------------------------------------------------- |
| `/agents`, `/agent <nome>`       | lista os agentes e escolhe o dos comandos                       |
| `/status`, `/balance`, `/profit` | estado do bot, saldo e resumo das últimas operações             |
| `/start`, `/stop`                | inicia e para o bot                                             |
| `/buy`, `/sell`                  | ordem manual, por exemplo `/buy BTCUSDm 0.1 lot`                |
| `/reanalyze <par> [nota]`        | pede uma nova análise ao modelo agora                           |
| `/prompts`, `/prompt <nome>`     | lista e mostra os prompts                                       |
| `/editprompt <nome>`, `/cancel`  | edita um prompt, com o texto na mensagem seguinte ou num `.txt` |
| `/useprompt <agente> <nome>`     | troca o prompt de um agente LLM                                 |
| `/help`                          | ajuda                                                           |

### Backtest e dados históricos

```bash
# candles pela bridge do MT5
go run ./cmd/xtrader download --exchange mt5 --mt5-bridge http://127.0.0.1:18812 \
  --pair XAUUSDm --timeframe 1h --days 90 --output xau-1h.csv

# backtest de uma estratégia sobre o CSV
go run ./agents/trendagent -mode backtest -strategy matrend \
  -pair XAUUSDm -timeframe 1h -csv xau-1h.csv -lot 0.01

# com -api, o resultado fica aberto para o painel ao terminar
go run ./agents/trendagent -mode backtest -strategy matrend -pair XAUUSDm -timeframe 1h \
  -csv xau-1h.csv -api 127.0.0.1:5570
```

O agente LLM também roda backtests, mas cada candle vira uma requisição paga ao modelo.

### Migração da versão anterior

Faça uma vez só, com o serviço antigo parado. Nunca rode os dois ao mesmo tempo: eles usariam a mesma conta, o mesmo magic e o mesmo bot.

```bash
go run ./cmd/migrator -config configs/config.yml import -agents agents.yml \
  -env ~/.config/automator/automator.env -dry-run      # o que seria importado
go run ./cmd/migrator -config configs/config.yml import -agents agents.yml \
  -env ~/.config/automator/automator.env
```

- **O que o import traz:**
  - os agentes MT5;
  - o usuário do painel;
  - o Telegram;
  - as chaves e os tokens, já cifrados;
  - os dados de `data/`: ordens, decisões e journal.
- **Repetir não duplica nada:** o import é idempotente.
- **Nos containers:** use `-bridge http://mt5:18812 -calendar http://mt5:22347/mcp`.

## Desenvolvimento

| Comando                                                            | O que faz                                                                              |
| ------------------------------------------------------------------ | -------------------------------------------------------------------------------------- |
| `make test`                                                        | `go test -race -cover ./...`                                                           |
| `make lint`                                                        | `golangci-lint run --fix`                                                              |
| `make generate` / `make generate-check`                            | gera e confere `web/src/api/types.ts` a partir de `ui/dto.go` (tygo)                   |
| `make ui-install`, `make ui-dev`, `make ui-check`, `make ui-build` | dependências, servidor de desenvolvimento, lint + typecheck + testes e build do painel |
| `deploy/docker/build.sh [controller\|mt5]`                         | imagens da stack                                                                       |

**Testes com banco:** sem `XTRADER_TEST_DATABASE_URL`, os testes de banco são pulados. Com ela, cada teste cria um banco próprio, com migrações e seeds reais, e o apaga no fim. O usuário precisa poder criar bancos:

```bash
XTRADER_TEST_DATABASE_URL='postgres://postgres:<senha>@127.0.0.1:55432/postgres?sslmode=disable' go test -race ./...
```

## API

- **Painel** (`/api`): sessão por cookie. Métodos que alteram algo exigem o header `X-CSRF-Token` com o valor do cookie `csrf_token`.
  - `/api/auth/login`, `/api/auth/logout` e `/api/auth/session`;
  - `/api/agents` e `/api/agents/{agente}/…`: proxy da API de cada agente (snapshot, eventos SSE, controles, decisões);
  - `/api/settings/agents[/{nome}[/restart]]`, `/api/prompts[/{id}]`, `/api/settings/telegram[/users[/{id}]]` e `/api/users[/{id}]`;
  - `/api/settings/agents/{nome}/periods`: `GET` lista os períodos de apuração do agente (o atual primeiro); `POST` com `{"deposit": 1000, "currency": "BRL"}` redefine os resultados (201 com o período novo);
  - os tipos estão em `ui/dto.go`, gerados para `web/src/api/types.ts`.
- **API interna dos agentes** (`agentapi`): só no loopback, com `Authorization: Bearer <token do start>`, e cada token só alcança o próprio agente. Rotas: `config`, `runtime` (long-poll), `prompt`, `order-ids`, `orders`, `decisions`, `decision-links`, `journal/memory` e `outbox`.

## Segurança

- **Login:** senhas em bcrypt, sessão com cookie `HttpOnly` (e `Secure` atrás de proxy HTTPS) e proteção CSRF.
- **Segredos:** cifrados no banco (AES-256-GCM), só de escrita no painel e nunca mostrados em respostas, logs ou no Telegram. O token do bot é mascarado nos logs.
- **Agentes:** recebem um ambiente mínimo e um token novo a cada início, revogado quando saem.
- **Conta real:** exige **Permitir conta real** no agente. No container `mt5`, a bridge também tem `MT5_ALLOW_REAL` e `MT5_READ_ONLY`.
- **Portas publicadas no Docker:** 8082 (painel), 5900 (VNC, que só sobe com senha) e 5432 (PostgreSQL). Num servidor com IP público, feche 5900 e 5432 no firewall ou use-os por túnel SSH.
