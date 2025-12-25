# XTrader em containers (Docker Compose)

Quatro serviços, no padrão de `/var/containers`:

| Serviço | O que roda | Dados |
|---|---|---|
| `controller` | painel web (com login), Telegram e os agentes do banco, processos do controller | `services/controller/data` (outbox de cada agente), `services/controller/config/config.yml` |
| `migrator` | migrações e seeds do banco (`migrator up`), a cada subida, antes do controller; e o import único da versão anterior | `services/migrator/config/` (`config.yml`, `agents.yml` de exemplo para o import) |
| `postgres` | PostgreSQL 18: agentes, prompts, usuários, Telegram, ordens, decisões e journal | `services/postgres/data` |
| `mt5` | terminal MetaTrader 5 da Exness no Wine, numa tela virtual (Xvfb), e a bridge HTTP (`tools/mt5bridge`) que os agentes usam | `services/mt5/data` (prefixo do Wine: Python, terminal e os dados dele) |

Os agentes de uma mesma conta dividem o mesmo `mt5`, separados pelo magic number. Uma segunda conta seria outro serviço `mt5`.

## Estrutura

```none
deploy/docker/                      → vira /var/containers/xtrader no servidor
├── docker-compose.yml
├── build.sh                        # constrói as imagens (a partir do repositório)
├── install.sh                      # instala em /var/containers/xtrader
├── images/                         # Dockerfiles, usados só pelo build.sh
│   ├── controller/                 # controller, migrator, agentes, painel e sql/
│   └── mt5/
└── services/
    ├── controller/
    │   ├── .env                    # criado pelo install.sh a partir do .env.example
    │   ├── config/config.yml
    │   └── data/
    ├── migrator/
    │   ├── .env
    │   └── config/                 # config.yml e agents.yml (import)
    ├── mt5/
    │   ├── .env
    │   └── data/
    └── postgres/
        ├── .env
        └── data/
```

## Requisitos do servidor

- Linux **x86_64**: o Wine não roda de forma confiável em ARM.
- Docker Engine com Compose v2 e BuildKit.
- Disco: cerca de 2,5 GB para as imagens e 1,5 GB para o prefixo do Wine.
- Memória: 2 GB livres. O terminal no Wine usa CPU de forma contínua.
- Internet: na primeira subida, o instalador da Exness baixa o terminal. Depois, o acesso é às cotações e às APIs (OpenAI, Telegram).

## 1. Construir as imagens

No servidor, ou em outra máquina com o repositório:

```bash
git clone <repositório> xtrader && cd xtrader
deploy/docker/build.sh              # as duas; ou: build.sh controller | build.sh mt5
```

As tags vêm do `docker-compose.yml` (`xtrader-controller:1.1.0`, também usada pelo `migrator`, e `xtrader-mt5:1.0.0`); o `postgres` usa a imagem oficial `postgres:18.6-trixie`. Para uma nova versão, troque a tag lá e rode o `build.sh` de novo.

## 2. Instalar em `/var/containers/xtrader`

```bash
sudo deploy/docker/install.sh       # ou: install.sh /outro/diretorio
```

O script:
- cria a estrutura;
- copia o compose, os `config.yml` e o `agents.yml` de exemplo;
- cria o `.env` de cada serviço a partir do `.env.example`;
- gera uma vez, e grava onde cada um é usado:
  - o `MT5_BRIDGE_TOKEN` (no `.env` do `mt5`);
  - a senha do banco (`POSTGRES_PASSWORD` e `XTRADER_DB_PASSWORD` do `controller` e do `migrator`);
  - a chave dos segredos `XTRADER_SECRETS_KEY` (do `controller` e do `migrator`). **Guarde uma cópia**: sem ela, as chaves de API e os tokens do banco precisam ser definidos de novo.

Ao rodar de novo, ele preserva os `.env`, os dados e as configurações existentes.

## 3. Preencher os `.env`

- **`services/mt5/.env`:**
  - `MT5_LOGIN`, `MT5_SERVER` e `MT5_PASSWORD`: o terminal loga sozinho com eles. A senha é lida do ambiente, nunca da linha de comando. Ela é obrigatória num terminal novo, porque a senha salva de outro terminal não funciona nele;
  - `MT5_ALLOW_REAL=1` somente se for conta real, porque a bridge recusa conta real sem isso.
- **`services/controller/.env`:** `XTRADER_BOOTSTRAP_USERNAME` e `XTRADER_BOOTSTRAP_PASSWORD` (8 caracteres ao menos), o primeiro usuário do painel, criado enquanto o banco não tem nenhum. Apague os dois depois do primeiro login.

As chaves dos LLMs, o token e os usuários do Telegram e os tokens da bridge e do MCP não vão mais nos `.env`: ficam no banco, cifrados, definidos na página **Configurações** do painel.

## 4. Subir

```bash
cd /var/containers/xtrader
docker compose up -d
docker compose logs -f mt5
```

A ordem é `postgres` → `migrator` (migrações e os 43 prompts de exemplo) → `controller`, que também espera o `mt5` ficar saudável. Na **primeira subida**, o `mt5` cria o prefixo do Wine e instala o Python, os pacotes da bridge e o terminal, que é baixado pelo instalador da Exness. Isso leva alguns minutos. Se o `controller` não subir porque a instalação passou do `start_period`, basta rodar `docker compose up -d` de novo.

Para conferir:

```bash
docker compose ps -a                       # postgres, mt5 e controller "healthy"; migrator "Exited (0)"
docker compose logs migrator
docker compose exec mt5 sh -c 'curl -s -H "X-Bridge-Token: $MT5_BRIDGE_TOKEN" http://127.0.0.1:18812/health'
```

Na resposta da bridge, `connected` e `trade_allowed` precisam estar `true`. O `MT5_ALGO_TRADING=1` liga o Algo Trading a cada subida. Ele também o mantém ligado depois de uma troca de conta, porque o padrão do terminal o desliga nesse caso, e o primeiro login de um terminal novo conta como troca.

## 5. Agentes, Telegram e usuários

1. **Agentes:** o banco começa sem agentes. Crie-os em **Configurações > Agentes > Novo agente** (modo, endereço da API, conta MT5 com a bridge `http://mt5:18812`, modelo do LLM, parâmetros e segredos), ou importe os do `services/migrator/config/agents.yml`, no formato da versão anterior:

   ```bash
   docker compose run --rm migrator migrator -config /app/config/config.yml import -agents /app/config/agents.yml -dry-run
   docker compose run --rm migrator migrator -config /app/config/config.yml import -agents /app/config/agents.yml
   ```

   O controller lança os agentes habilitados em até 30 segundos. Depois, cada agente é editado ou excluído (desligado antes) na mesma tela; uma mudança de inicialização num agente rodando fica como "reinício pendente".

2. Entre no painel com o usuário do bootstrap e abra **Configurações** (ícone de engrenagem):
   - **Agentes:** a chave da API do LLM e o token da bridge de cada agente (o `MT5_BRIDGE_TOKEN` do `services/mt5/.env`), o prompt dos agentes LLM, o painel de trading e o controle via Telegram, além de **Editar** e **Excluir**. Uma troca de segredo fica como "reinício pendente": use **Reiniciar**.
   - **Telegram:** o token do bot, os IDs autorizados e **Bot ativo**.
   - **Usuários:** crie os usuários do painel e troque a senha do primeiro.

## 6. Calendário econômico (servidor MCP do terminal), uma vez

O terminal já vem com o servidor MCP ligado (`http://127.0.0.1:22346/mcp`, encaminhado para a rede interna em `mt5:22347`), com uma chave gerada por ele. Essa chave fica cifrada na configuração do terminal, por isso precisa ser copiada uma vez pela interface:

1. Em `services/mt5/.env`, defina `MT5_VNC_PASSWORD=<senha>` e rode `docker compose up -d mt5`. A porta 5900 já é publicada, mas o VNC só sobe com a senha.
2. Conecte um cliente VNC em `<servidor>:5900`, de preferência por um túnel SSH (`ssh -L 5900:127.0.0.1:5900 <usuário>@<servidor>` e o cliente em `localhost:5900`): o protocolo do VNC não é cifrado.
3. No terminal, vá em **Ferramentas > Opções** e copie a chave do servidor MCP do MetaTrader.
4. No painel, em **Configurações > Agentes**, defina o **Token do calendário (MCP)** do agente llm e reinicie-o. Depois apague o `MT5_VNC_PASSWORD` e rode `docker compose up -d mt5`, que desliga o VNC.

Sem esse passo, o agente llm roda sem o calendário. Ficam sem efeito as notícias no contexto e o bloqueio de entradas perto de notícias de alto impacto.

## Migrar do serviço atual (systemd no host)

**Nunca rode os dois ao mesmo tempo.** Eles operariam a mesma conta com o mesmo magic number, gerando ordens em dobro, e disputariam o mesmo bot do Telegram.

1. De preferência, faça a migração sem posição aberta. Mesmo com posição, o agente a recupera a partir das ordens importadas e do terminal.
2. Na máquina atual, pare o serviço: `systemctl --user disable --now automator-llm.service`.
3. Copie para `services/migrator/config/import/` (um diretório temporário):
   - o `agents.yml` do host, com todos os agentes;
   - o `data/` do host, com `nb-llm.db` (ordens), `nb-llm.decisions.db`, `nb-llm.decisions.attachments/` e `nb-llm.decisions.jsonl` (decisões e journal);
   - o `~/.config/automator/automator.env` como `import.env`, trocando nele o `MT5_BRIDGE_TOKEN` pelo do `services/mt5/.env`;
   - depois, `chown -R 1000:1000 services/migrator/config/import && chmod 600 services/migrator/config/import/import.env`: o migrator roda com o uid 1000.
4. Importe, trocando a bridge de todos os agentes e o servidor MCP dos calendários pelos do serviço `mt5` (`-bridge` e `-calendar`), já que os do host apontam para `127.0.0.1`:

   ```bash
   docker compose up -d postgres migrator
   docker compose run --rm migrator migrator -config /app/config/config.yml import \
     -agents /app/config/import/agents.yml -env /app/config/import/import.env \
     -bridge http://mt5:18812 -calendar http://mt5:22347/mcp -dry-run
   docker compose run --rm migrator migrator -config /app/config/config.yml import \
     -agents /app/config/import/agents.yml -env /app/config/import/import.env \
     -bridge http://mt5:18812 -calendar http://mt5:22347/mcp
   ```

   O import traz os agentes (os da Capital e da Binance ficam de fora), o usuário do painel (senha em bcrypt), o Telegram (token e IDs), as chaves dos LLMs, os tokens (cifrados) e as ordens, decisões e journal de cada agente. O relatório nunca mostra os segredos.
5. Apague `services/migrator/config/import/` (ele tem as chaves) e rode `docker compose up -d`. O token do MCP pode precisar do passo 6, porque o terminal é outro.

## Operação

| Ação | Comando |
|---|---|
| Logs (rotação de 10 MB x 3) | `docker compose logs -f controller` (ou `mt5`, `postgres`, `migrator`) |
| Reiniciar os agentes | `docker compose restart controller`, ou um agente pelo painel (**Configurações > Reiniciar**). Com posição aberta, o agente recupera a posição e o stop. |
| Parar tudo | `docker compose down` |
| Atualizar | `git pull && deploy/docker/build.sh`, trocando a tag no compose se for uma nova versão; depois `docker compose up -d` (o `migrator` aplica as migrações novas) |
| Backup do banco | `docker compose exec postgres pg_dump -U xtrader -Fc xtrader > xtrader.dump` |
| Backup dos arquivos | `services/*/.env` (a chave dos segredos), `services/controller/data` (outbox dos agentes) e `services/mt5/data` (prefixo do Wine com o terminal logado; pode ser reinstalado) |

## Portas e segurança

- **Portas publicadas**, em todas as interfaces do host (ajuste-as conforme a reserva de portas de rede):
  - **8082, o painel:** coloque-o atrás do proxy reverso com HTTPS; o cookie de sessão sai como `Secure` quando o proxy envia `X-Forwarded-Proto: https`;
  - **5900, o VNC do terminal:** só sobe com o `MT5_VNC_PASSWORD`; deixe a senha vazia fora do uso;
  - **5432, o PostgreSQL:** protegido pela senha do usuário `xtrader`.

  Num servidor com IP público, feche o 5900 e o 5432 no firewall e use-os por túnel SSH, ou publique-os só no loopback (`"127.0.0.1:5432:5432"`).
- **Portas internas**, comentadas no compose: a bridge (18812) e o MCP (22347) ficam na rede `xtrader`, e a bridge exige o token; a API interna dos agentes (8090) e a de cada agente (ex.: 5580) só no container do controller, que faz o proxy.
- **Segredos:** a senha do banco e a chave dos segredos ficam nos `.env` (modo 600, fora do git); as chaves de API e os tokens, cifrados no banco, nunca aparecem no painel, nos logs nem no Telegram. Senhas do painel em bcrypt.
- **Usuário:** o `controller` e o `mt5` rodam com um usuário não-root (uid 1000), dono dos dados; o `postgres`, com o usuário da imagem oficial.
