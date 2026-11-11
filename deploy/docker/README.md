# XTrader em containers (Docker Compose)

Quatro serviços, no padrão de `/var/containers`:

| Serviço | O que roda | Dados |
|---|---|---|
| `controller` | painel web (com login), Telegram e os agentes do banco, processos do controller | `services/controller/data` (outbox de cada agente e pidfiles em `data/pids`), `services/controller/config/config.yml` |
| `migrator` | migrações e seeds do banco (`migrator up`), a cada subida, antes do controller: num banco novo, os prompts e os agentes iniciais, desligados | `services/migrator/config/config.yml` |
| `postgres` | PostgreSQL 18: agentes, prompts, usuários, Telegram, ordens, decisões e journal | `services/postgres/data` |
| `mt5` | terminal MetaTrader 5 da Exness no Wine, numa tela virtual (Xvfb), e a bridge HTTP (`tools/mt5bridge`) que os agentes usam | `services/mt5/data` (prefixo do Wine: Python, terminal e os dados dele) |

Os agentes de uma mesma conta dividem o mesmo `mt5`, separados pelo magic number: o controller recusa dois agentes ativos em live com o mesmo magic e um par em comum (veja "Magic e comentário" no README da raiz). Um terminal fica numa conta por vez; outras contas cadastradas no painel ficam inativas até serem ativadas nele. Duas contas ao mesmo tempo pedem outro serviço `mt5` (copie o serviço com outro nome, volume e token, por exemplo `mt5-2`, e cadastre a conta com a bridge `http://mt5-2:18812`).

## Estrutura

```none
deploy/docker/                      → vira /var/containers/xtrader no servidor
├── docker-compose.yml
├── build.sh                        # constrói as imagens (a partir do repositório)
├── rebuild.sh                      # pergunta quais imagens reconstruir e se recria o compose
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
    │   └── config/                 # config.yml
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

Para atualizar um servidor já instalado, o `rebuild.sh` (ou `make docker-rebuild`) faz as duas etapas, perguntando antes de cada uma:

```bash
deploy/docker/rebuild.sh            # ou: rebuild.sh /outro/diretorio; --dry-run só mostra os comandos
```

1. Mostra o branch e o commit do repositório (e avisa quando há alterações não commitadas, que também entram nas imagens) e pergunta quais imagens reconstruir: `1` controller (também a do migrator), `2` mt5, `3` as duas ou `0` nenhuma; aceita mais de uma, ex.: `1 2`.
2. Pergunta se recria o compose instalado com `docker compose up -d --force-recreate` (padrão: não). Isso recria todos os serviços: os agentes param e voltam, o terminal do MT5 reinicia e o `migrator` aplica as migrações novas. Prefira fazê-lo sem posição aberta. Com Enter ou `n`, nada é recriado, e o comando para aplicar depois é mostrado.

## 2. Instalar em `/var/containers/xtrader`

```bash
sudo deploy/docker/install.sh       # ou: install.sh /outro/diretorio
```

O script:
- cria a estrutura;
- copia o compose e os `config.yml`;
- cria o `.env` de cada serviço a partir do `.env.example`;
- gera uma vez, e grava onde cada um é usado:
  - o `MT5_BRIDGE_TOKEN` (no `.env` do `mt5`);
  - a senha do banco (`POSTGRES_PASSWORD` e `XTRADER_DB_PASSWORD` do `controller` e do `migrator`);
  - a chave dos segredos `XTRADER_SECRETS_KEY` (do `controller` e do `migrator`). **Guarde uma cópia**: sem ela, as chaves de API e os tokens do banco precisam ser definidos de novo.

Ao rodar de novo, ele preserva os `.env`, os dados e as configurações existentes.

## 3. Preencher os `.env`

- **`services/mt5/.env`:** só o que a subida do container precisa: o `MT5_BRIDGE_TOKEN` (gerado pelo install.sh), `MT5_ALGO_TRADING`, `MT5_MCP_FORWARD` e `MT5_VNC_PASSWORD`. As chaves aceitam `1`/`true`/`yes` e `0`/`false`/`no` (maiúsculas ou minúsculas; vazio é o padrão); qualquer outro valor impede o container de subir, com o erro no log (`docker compose logs mt5`), em vez de valer como desligado.
  - **A conta não vai mais aqui.** Login, servidor, senha, "permitir conta real" e "somente leitura" são cadastrados no painel, em **Configurações > Contas MT5** (passo 5), cifrados no banco. O controller entrega a conta ativa à bridge, que a guarda em `services/mt5/data/xtrader-bridge/session.json`, sem a senha. Sem conta, a bridge sobe mesmo assim (saudável, aguardando a conta), e o painel continua acessível.
  - `MT5_LOGIN`, `MT5_SERVER`, `MT5_PASSWORD`, `MT5_ALLOW_REAL` e `MT5_READ_ONLY` são legadas: valem só até o controller entregar a primeira conta. Numa instalação anterior, o controller importa a conta da bridge na subida; depois de conferir a conta e a senha no painel, apague-as do `.env`.
- **`services/controller/.env`:** `XTRADER_BOOTSTRAP_USERNAME` e `XTRADER_BOOTSTRAP_PASSWORD` (8 caracteres ao menos), o primeiro usuário do painel, criado enquanto o banco não tem nenhum, com o papel Administrador (todas as permissões). Apague os dois depois do primeiro login. Os outros usuários e os papéis deles ficam em **Configurações > Usuários e papéis**; um usuário sem papel não entra.

As contas MT5, as chaves dos LLMs, o token e os usuários do Telegram e o token do MCP não vão mais nos `.env`: ficam no banco, cifrados, definidos na página **Configurações** do painel. No ambiente ficam só o que a subida precisa: a senha do banco, a chave dos segredos (`XTRADER_SECRETS_KEY`, fora do banco), o primeiro usuário, o `TZ` e, no `mt5`, o token da bridge e as chaves do container.

## 4. Subir

```bash
cd /var/containers/xtrader
docker compose up -d
docker compose logs -f mt5
```

A ordem é `postgres` → `migrator` (o schema, os prompts e os agentes iniciais) → `controller`, que também espera o `mt5` ficar saudável. Na **primeira subida**, o `mt5` cria o prefixo do Wine e instala o Python, os pacotes da bridge e o terminal, que é baixado pelo instalador da Exness. Isso leva alguns minutos. Se o `controller` não subir porque a instalação passou do `start_period`, basta rodar `docker compose up -d` de novo.

Para conferir:

```bash
docker compose ps -a                       # postgres, mt5 e controller "healthy"; migrator "Exited (0)"
docker compose logs migrator
docker compose exec mt5 sh -c 'curl -s -H "X-Bridge-Token: $MT5_BRIDGE_TOKEN" http://127.0.0.1:18812/health'
```

Na resposta da bridge, `connected` e `trade_allowed` precisam estar `true` depois de a conta ser ativada no painel; antes disso, ela responde `"waiting": "session"` (aguardando a conta) ou `"waiting": "password"` (o terminal pede a senha). O `MT5_ALGO_TRADING=1` liga o Algo Trading a cada subida. Ele também o mantém ligado depois de uma troca de conta, porque o padrão do terminal o desliga nesse caso, e o primeiro login de um terminal novo conta como troca.

Os campos `deadline` e `timeout` em `true` indicam uma bridge que respeita os dois headers do prazo das ordens (`X-Bridge-Deadline` e `X-Bridge-Timeout`, este contado pelo relógio da bridge). Atualize as duas imagens juntas (`build.sh` sem argumento): um agente novo com uma bridge antiga avisa no log e não consegue confirmar que uma ordem sem resposta deixou de ser executada. Com o terminal logado em outra conta que não a ativa no painel (ex.: trocada pelo VNC), a bridge recusa as ordens e os agentes param de operar; o controller loga o terminal de volta na conta ativa assim que nenhum agente roda nele.

## 5. Agentes, Telegram e usuários

1. **Agentes:** num banco novo, o migrator cria os agentes iniciais pelo seed `sql/seeds/0004_agents.sql`, todos desligados e sem segredos, em XAUUSDm e BTCUSDm com lote 0.01 (menos os dois da estratégia `pairs`, `pairsgold` e `pairscrypto`, cada um nas duas pernas do seu spread): `llm-trend`, `llm-smc-liquidity` e `llm-support-resistance` (LLM), e um agente sem LLM de cada estratégia do código, entre eles `matrend`, `fibonacci` e `fimathe` e as regras dos prompts codificadas (54 ao todo; veja "Agentes iniciais" no README da raiz). Cada um é criado uma única vez: o seed nunca sobrescreve uma edição do painel nem recria um agente apagado. Defina os segredos de cada um (passo 2 e seção 6) e ligue-o em **Configurações > Agentes**. Outros agentes são criados em **Novo agente** (modo, endereço da API no loopback do container, ex.: `localhost:5580`, conta MT5 com a bridge `http://mt5:18812`, modelo do LLM, parâmetros e segredos). Nunca rode o mesmo agente aqui e em outra máquina ao mesmo tempo: eles operariam a mesma conta com o mesmo magic number.

   O controller lança os agentes habilitados em até 30 segundos. Depois, cada agente é editado ou excluído (desligado antes) na mesma tela; uma mudança de inicialização num agente rodando fica como "reinício pendente".

2. Entre no painel (`http://127.0.0.1:8082` no servidor, a URL do ngrok ou um túnel SSH; veja "Portas e segurança") com o usuário do bootstrap e abra **Configurações** (ícone de engrenagem):
   - **Contas MT5:** a conta do terminal (login, servidor, senha, a bridge `http://mt5:18812` e o token dela, o `MT5_BRIDGE_TOKEN` do `services/mt5/.env`), com **Permitir conta real** para uma conta real; **Testar conexão** confere o tipo (real ou demo) e a moeda sem expor credenciais; a chave **Ativa no terminal** faz o terminal logar nela (desligada, a conta deixa de ser servida e os agentes dela não são iniciados), sempre com confirmação: os agentes do terminal são parados e, confirmado, o que eles tiverem aberto na conta atual é encerrado antes da troca;
   - **Agentes:** a conta MT5 de cada agente (a bridge e o token vêm dela), a chave da API do LLM, o prompt dos agentes LLM, o painel de trading, o controle via Telegram e a reanálise dos agentes LLM (**Permitir reanálise**), além de **Editar** e **Excluir**. Uma troca de segredo fica como "reinício pendente": use **Reiniciar**.
   - **Telegram:** o token do bot, os IDs autorizados e **Bot ativo**.
   - **Usuários:** crie os usuários do painel e troque a senha do primeiro.

## 6. Calendário econômico (servidor MCP do terminal), uma vez

O terminal já vem com o servidor MCP ligado (`http://127.0.0.1:22346/mcp`, encaminhado para a rede interna em `mt5:22347`), com uma chave gerada por ele. Essa chave fica cifrada na configuração do terminal, por isso precisa ser copiada uma vez pela interface:

1. Em `services/mt5/.env`, defina `MT5_VNC_PASSWORD=<senha>` e rode `docker compose up -d mt5`. A porta 5900 já é publicada no loopback do servidor, mas o VNC só sobe com a senha.
2. Conecte um cliente VNC em `127.0.0.1:5900` no próprio servidor ou, de outra máquina, por um túnel SSH (`ssh -N -L 5900:127.0.0.1:5900 <usuário>@<servidor>` e o cliente em `127.0.0.1:5900` dessa máquina; veja "Acesso de outra máquina"). A porta não é alcançável pela rede, e o protocolo do VNC não é cifrado.
3. No terminal, vá em **Ferramentas > Opções** e copie a chave do servidor MCP do MetaTrader.
4. No painel, em **Configurações > Agentes**, defina o **Token do calendário (MCP)** do agente llm e reinicie-o. Depois apague o `MT5_VNC_PASSWORD` e rode `docker compose up -d mt5`, que desliga o VNC.

Sem esse passo, o agente llm roda sem o calendário. Ficam sem efeito as notícias no contexto e o bloqueio de entradas perto de notícias de alto impacto.

## Operação

| Ação | Comando |
|---|---|
| Logs (rotação de 10 MB x 3) | `docker compose logs -f controller` (ou `mt5`, `postgres`, `migrator`) |
| Reiniciar os agentes | `docker compose restart controller`, ou um agente pelo painel (**Configurações > Reiniciar**). Com posição aberta, o agente recupera a posição e o stop. |
| Parar tudo | `docker compose down`. O controller para primeiro: os agentes têm `agents.stop_timeout` (20 s) e a parada inteira cabe no `shutdown_timeout` (30 s) do `services/controller/config/config.yml`, abaixo do `stop_grace_period` do compose (45 s), depois do qual o Docker mata o container. Ao mudar esses tempos, mantenha essa ordem. Uma instalação feita antes desse ajuste só ganha o `stop_grace_period` depois de reinstalar: o compose instalado não o tem, e o Docker espera só o padrão de 10 s, menos que o `agents.stop_timeout`. Rode o `install.sh` de novo (ele troca o compose, guarda o anterior em `docker-compose.yml.bak` e mantém os `config.yml`), pare o controller com o tempo inteiro (`docker compose stop -t 45 controller`) e depois `docker compose up -d`, que recria o container com o compose novo. De preferência, faça isso sem posição aberta. |
| Atualizar | `git pull && deploy/docker/build.sh`, trocando a tag no compose se for uma nova versão; depois `docker compose up -d` (o `migrator` aplica as migrações novas; faça o backup do banco antes). Um banco de antes de 14/10/2026 (versão 22 das migrações antigas) é recusado: recrie-o como em "Recriar o banco do zero" no README da raiz |
| Backup do banco | `docker compose exec postgres pg_dump -U xtrader -Fc xtrader > xtrader.dump` |
| Backup dos arquivos | `services/*/.env` (a chave dos segredos), `services/controller/data` (outbox dos agentes) e `services/mt5/data` (prefixo do Wine com o terminal logado; pode ser reinstalado) |
| Disco do outbox cheio | O agente segue operando e acompanha as ordens em memória. Ele notifica a falha no painel e no Telegram e regrava as ordens quando houver espaço. Libere espaço em `services/controller/data` antes de reiniciar o agente, porque uma ordem ainda não gravada se perde no reinício. |
| Registros sem chegar ao banco | O card do agente em **Configurações** mostra os registros do outbox ainda não gravados (quando o envio falha ou o mais antigo tem mais de 1 min) os recusados para sempre, separados no arquivo, e os retidos por falharem seguidamente, reenviados de tempos em tempos (veja "Registros" no README da raiz). O motivo está no log do controller (`the records`) ou no do agente (`set apart for good`, `held apart`). |
| Outbox ilegível (o agente não inicia) | O log do agente diz o arquivo (`services/controller/data/outbox/<agente>-<id>.db`). Para iniciar mesmo assim, dê ao agente o parâmetro `outbox-quarantine` = `true` e reinicie: o arquivo vai para `<arquivo>.corrupt-<hora>`, guardado, e o agente começa com um outbox vazio (os registros dele não chegam ao banco). Tire o parâmetro depois. Faça o backup do arquivo antes. |

## Portas e segurança

- **Portas publicadas**, só no loopback do host (`127.0.0.1`; ajuste os números conforme a reserva de portas de rede). O Docker publica as portas com regras próprias de NAT, que passam por fora do firewall do host (ufw): fechar a porta no firewall não basta, por isso o compose nunca as publica em `0.0.0.0`. Da rede (LAN, Wi-Fi, VPN) nenhuma é alcançável; de outra máquina, use um túnel SSH (abaixo).
  - **`127.0.0.1:8082`, o painel:** o ngrok do host (ou um proxy reverso com HTTPS no host) tunela para esse endereço. Use `127.0.0.1`, e não `localhost`, que pode resolver para o IPv6 `::1`, onde nada é publicado. O cookie de sessão sai como `Secure` quando o proxy envia `X-Forwarded-Proto: https`. Esse header e o `X-Forwarded-For` (o endereço do cliente nos logs e no limite de login) só valem vindos de um proxy de `ui.trusted_proxies` em `services/controller/config/config.yml`: o loopback e a faixa privada do Docker (`172.16.0.0/12`), por onde o ngrok do host chega ao container. Isso só é seguro com a porta no loopback: publicada em todas as interfaces, outro cliente poderia chegar por essa faixa. Se a rede `xtrader` tiver outra faixa (`docker network inspect xtrader`), ajuste a lista;
  - **`127.0.0.1:5900`, o VNC do terminal:** só sobe com o `MT5_VNC_PASSWORD`; deixe a senha vazia fora do uso;
  - **`127.0.0.1:5432`, o PostgreSQL:** para `psql` e ferramentas no host, com a senha do usuário `xtrader`. O backup e as migrações não usam a porta: rodam pelo `docker compose exec`/`run`, na rede `xtrader`.

  Numa instalação anterior, com as portas em todas as interfaces, rode o `install.sh` de novo (ele troca o compose e guarda o anterior em `docker-compose.yml.bak`) e depois `docker compose up -d`, que recria o `controller`, o `mt5` e o `postgres` para publicar as portas de novo. De preferência, faça isso sem posição aberta. Para conferir: `ss -ltn` mostra `127.0.0.1:8082`, `127.0.0.1:5900` e `127.0.0.1:5432`, e nenhum `0.0.0.0` nem `[::]` nessas portas.
- **Portas internas**, comentadas no compose: a bridge (18812) e o MCP (22347) ficam na rede `xtrader`, e a bridge exige o token; a API interna dos agentes (8090) e a de cada agente (ex.: 5580) só no loopback do container do controller, que faz o proxy. O painel recusa um endereço de agente fora do loopback, e um agente gravado antes com outro host (ex.: `0.0.0.0:5580`) não é lançado até o endereço ser corrigido nas configurações dele.
- **Segredos:** a senha do banco e a chave dos segredos ficam nos `.env` (modo 600, fora do git); as chaves de API e os tokens, cifrados no banco, nunca aparecem no painel, nos logs nem no Telegram. Senhas do painel em bcrypt. Cada segredo é cifrado junto com o seu lugar (tabela, coluna e registro): copiado para outra linha ou coluna, não abre. Os gravados por uma versão anterior continuam valendo até serem definidos de novo; depois disso, voltar a uma imagem anterior exige definir os segredos de novo (veja "Segurança" no README da raiz).
- **Usuário:** o `controller` e o `mt5` rodam com um usuário não-root (uid 1000), dono dos dados; o `postgres`, com o usuário da imagem oficial.

### Acesso de outra máquina

Por um túnel SSH ao servidor, que encaminha uma porta local para o loopback dele (`-N`: só o túnel, sem shell):

```bash
ssh -N -L 5900:127.0.0.1:5900 <usuário>@<servidor>    # VNC: o cliente em 127.0.0.1:5900
ssh -N -L 5433:127.0.0.1:5432 <usuário>@<servidor>    # PostgreSQL: psql -h 127.0.0.1 -p 5433 -U xtrader xtrader
ssh -N -L 8082:127.0.0.1:8082 <usuário>@<servidor>    # painel sem o ngrok: http://127.0.0.1:8082
```

Uma porta local diferente (ex.: `5433`) evita o conflito com um PostgreSQL da própria máquina. O túnel cifra o tráfego, inclusive o do VNC, e só quem tem acesso SSH ao servidor o abre.
