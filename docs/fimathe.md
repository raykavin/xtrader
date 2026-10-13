# Estratégia Fimathe (`fimathe`)

Estratégia determinística, sem LLM, do `agents/breakoutagent` (`-strategy fimathe`), implementada em [`agents/strategies/fimathe.go`](../agents/strategies/fimathe.go). Ela opera a **primeira entrada da tendência** de cada mapeamento do canal de referência da técnica Fimathe (Marcelo Ferreira) e desenha os níveis no gráfico do painel.

## Versão e fontes

A versão seguida é a **Fimathe 4.0**, aula 3, "Mapeando pontos de negociação", de Marcelo Ferreira. É a fonte principal.

| Fonte | Situação | Uso |
| --- | --- | --- |
| [Grupo Fimathe — quem somos](https://fimatheprop.com/en/quem-somos/o-grupo-fimathe/) | Acessível; só nomes de conceitos, sem regras | Criador; conceitos (zonas neutras, canais de referência, Linhas do Equador, teoria macro/micro e do ciclo do canal, subciclo, "stop fora da caixinha", teoria dos ciclos); o "Robô Fimathe 2.0" automatiza a **primeira entrada** da técnica |
| [Fimathe 4.0 — Aula 3 (Scribd)](https://pt.scribd.com/document/396644202/Fimathe-4-0-Aula-3) | **Inacessível**: só prévia, pede login | Lida pela cópia do mesmo material no [pdfcoffee](https://pdfcoffee.com/fimathe-marcelo-ferreira-aula-03-mapeando-pontos-de-negociaao-pdf-free.html) (texto extraído com falhas de OCR, sem as figuras) |
| [Fimathe 4.0 — gestão por ciclos (Studocu)](https://www.studocu.com/pt-br/document/universidade-de-sao-paulo/economia-matematica/fimathe-40-mapeando-pontos-de-negociacao-com-marcelo-ferreira/124351048) | **Inacessível**: conteúdo exige assinatura | Não usada |
| [Canal oficial no YouTube](https://www.youtube.com/@MARCELOFERREIRAFIMATHE) | **Inacessível** pela leitura automática (sem títulos nem transcrições) | Não usada |
| [Aula 1 — Introdução (transcrição)](https://pdfcoffee.com/fimathe-curso-pdf-free.html) | Acessível | Definição de tendência, tempos gráficos |
| [Resumo "Fimathe.pdf"](https://pdfcoffee.com/fimathepdf-pdf-free.html) e [resumo "Modo de aplicação da técnica"](https://pdfcoffee.com/fimathe-modo-de-aplicaao-da-tecnica-pdf-free.html) | Acessíveis; resumos de alunos do curso | Subciclo, posição do stop, risco por operação |

Indicadores e robôs de terceiros (MQL5, blogs, um EA no GitHub) **não** foram usados como fonte de regras: cada um adapta a técnica de um jeito.

## Regras implementadas e suas fontes

| Regra | Fonte | Como foi implementada |
| --- | --- | --- |
| Análise no tempo maior, execução no menor ("No time maior faremos toda nossa análise. No time menor faremos nossa execução"; exemplo H1/M15) | Aula 3 | `fimathe-analysis-timeframe` (padrão 1h) e `timeframe` (padrão 15m). As barras de 1h são montadas com os candles de 15m, só barras fechadas |
| Marcação semanal (day trade, feita no domingo) ou na abertura (scalping) | Aula 3; resumo "Modo de aplicação" | `fimathe-mapping week` (padrão; a semana começa no domingo, 00:00 de `fimathe-timezone`) ou `day`. O mapeamento usa só as barras fechadas antes do início do período e vale por todo o período |
| O canal só é marcado dentro de uma tendência; tendência = topos e fundos ascendentes (ou descendentes) | Aula 3; Aula 1 | Alta: os dois últimos topos e os dois últimos fundos subindo; baixa: os dois caindo; fora disso, sem mapeamento no período |
| Canal de referência no último topo e no último fundo | Aula 3 | Do último fundo ao último topo do tempo de análise |
| "Se ficar muito largo marcamos no corpo, se ficar muito estreito marcamos na sombra"; "nem muito largo e nem muito estreito" | Aula 3 | `fimathe-mark auto` (padrão: pavio; corpo quando o canal de pavio passa do máximo), `wick` ou `body`; limites `fimathe-min-channel-atr`/`fimathe-max-channel-atr` |
| Zona neutra: projeção do canal (mesma altura), abaixo dele na alta e acima na baixa; nela não se opera | Aula 3 | Idem |
| Compra na alta com o preço abaixo dos 50% (ou na zona neutra): "apenas quando o preço voltar e romper os 50% do canal de referência"; "Se ele cair e ficar abaixo dos 50% então a compra será rompendo esse nível novamente" | Aula 3 | Armada nos 50%; entrada no fechamento acima deles |
| Compra com o preço acima dos 50%: rompimento do canal (exemplo resolvido: compra após romper a resistência do canal) | Aula 3 (ver ambiguidade 1) | Armada na borda do canal; entrada no fechamento acima dela, enquanto o preço não fechar abaixo dos 50% |
| "Não vender na tendência de alta e não comprar na tendência de baixa" | Aula 3 | Só o lado da tendência do mapeamento |
| Venda só quando o preço "perder 100% da zona neutra" (zona de reversão) | Aula 3 | Um fechamento além da zona neutra encerra o mapeamento: nenhuma entrada até o próximo (ver pendência sobre a entrada de reversão) |
| Stop técnico, sem quantidade fixa de pontos; "dois níveis abaixo" de uma compra no canal; "abaixo da zona neutra" | Aula 3; resumo "Fimathe.pdf" | Stop além da zona neutra, com folga de `fimathe-stop-buffer-atr` ATR do tempo de execução. `stop-value`/`take-profit-value` são recusados |
| Subciclo: "o canal anterior ao candlestick de entrada", "projetado a partir da linha de entrada"; "quando ele perde os primeiros 50% eu travo meu stop no 0x0"; "perdendo mais 50% desço meu stop mais um ponto" | Aula 3; resumo "Fimathe.pdf" | Subciclo: do último fundo ao último topo dos candles de execução antes da entrada. `fimathe-trail surf` (padrão): 0x0 no primeiro passo e um passo a mais a cada passo; `lock`: só o 0x0; `off`. Passo `fimathe-trail-step` (0.5; o exemplo resolvido usa 1) |
| Alvo: "pegar os dois níveis anteriores e projetá-los … a partir da nossa entrada"; "Ou será take ou será stop" | Aula 3 | `fimathe-target-levels` (padrão 0; o documentado é 2) mede alturas do canal a partir da linha de entrada. Surfar o stop exclui o alvo |
| "No máximo 3% por operação" | Resumo "Modo de aplicação" | `max-risk-pct` (padrão 3 no `fimathe`; `fimathe-max-risk-pct` é o mesmo limite): recusa a entrada que perde mais que isso do saldo livre (margem livre) no stop |
| Automação da **primeira entrada** da técnica | Página oficial (Robô Fimathe 2.0) | Uma entrada por mapeamento e por ativo |

## Decisões de implementação

Pontos que as fontes deixam abertos e foram resolvidos assim, todos configuráveis exceto os marcados como fixos:

- **Rompimento:** "romper"/"perder" é o **fechamento** do candle do tempo de execução além do nível (fixo). A ordem sai a mercado nesse fechamento. As fontes não dizem se vale o toque. A execução intrabar ficou de fora: ordens stop pendentes exigiriam o controlador de ordens listar as pendentes depois de um reinício (mudança no núcleo), e sem isso uma ordem duplicada seria possível.
- **Topos e fundos:** pivôs de `fimathe-pivot` barras de cada lado (padrão 3), buscados nas últimas `fimathe-analysis-bars` barras (120). Para o subciclo, `fimathe-subcycle-pivot` (3) candles de execução.
- **Larguras do canal:** de 1 a 8 ATR(14) do tempo de análise, calculado só sobre as barras do seu aquecimento. Com isso o mapeamento é o mesmo qualquer que seja o histórico carregado.
- **Folga do stop:** 0,1 ATR além da zona neutra.
- **Preço fora do canal no mapeamento:** acima do canal (na alta), aguarda a volta ao canal antes de armar; na zona de reversão, o período fica sem mapeamento.
- **Passos do subciclo:** contados pelos fechamentos desde a entrada. O stop só sobe se o fechamento estiver além do novo nível, e nunca recua.
- **Período:** a semana começa no domingo, 00:00 UTC por padrão; uma posição que atravessa a virada continua gerida pelo mapeamento em que entrou.

## Retomada, duplicidade e registros

- **Estado reconstruído:** o estado é uma função dos candles. Depois de um reinício, o mapeamento e a máquina de estados do período são recalculados. Uma posição encontrada é seguida com a linha de entrada e o subciclo do mapeamento do período em que abriu.
- **Sem entrada duplicada:** uma entrada por mapeamento e por ativo. Depois de um reinício, as posições abertas no período pelas execuções anteriores contam (`PositionsReplayed`, das ordens guardadas). Limite: o armazenamento não diz quem abriu a posição, então uma operação manual no período também conta.
- **Log:** registra, uma vez por estado (prefixo `[FIMATHE]`):
  - o mapeamento ou o motivo de não haver;
  - o nível armado e a espera;
  - a invalidação;
  - as entradas com as condições (fechamento, nível rompido, canal, stop, alvo);
  - as recusas (risco, stop do lado errado);
  - cada movimento de stop (passo do subciclo) e a saída.

## Desenhos no gráfico

A estratégia publica a análise do ativo pelo mesmo mecanismo das marcações do agente LLM (`ui.AnalysisSource`). A legenda acima do gráfico diz "Fimathe 15m …".

- **Do mapeamento**, no timeframe de análise e com o período no rótulo:
  - zona do canal de referência ("— fora das regras" quando fora dos limites);
  - zona neutra;
  - linha dos 50%;
  - expansões de 100% e 200%;
  - linha da zona de reversão.
- **Antes da entrada:** a entrada planejada como intenção tracejada ("aguardando rompimento"), com o stop e o alvo. Ela não é uma ordem.
- **Depois da entrada:**
  - o marcador da entrada ("rompimento 50%" ou "rompimento borda");
  - a zona do subciclo e os níveis de seus passos, no timeframe de execução;
  - um marcador a cada movimento do stop ("Stop 0x0", "Stop +1", …).
- **Ordens e posição reais:** desenhadas como ordens, separadas dos níveis da estratégia.
- **Fim do mapeamento:** a invalidação encerra os desenhos no candle dela, com um marcador.

O seletor de timeframe do gráfico continua só de visualização: os desenhos se alinham aos candles do timeframe mostrado.

## Parâmetros

| Parâmetro | Padrão | Efeito |
| --- | --- | --- |
| `timeframe` | `15m` | Execução: entradas e passos do subciclo nos fechamentos |
| `fimathe-analysis-timeframe` | `1h` | Análise: tendência e canal; múltiplo do `timeframe`, divisor de um dia |
| `fimathe-mapping` | `week` | `week` (day trade) ou `day` (scalping) |
| `fimathe-timezone` | `UTC` | Meia-noite que inicia a semana (domingo) e o dia |
| `fimathe-pivot` | `3` | Barras de cada lado de um topo/fundo do tempo de análise |
| `fimathe-analysis-bars` | `120` | Barras de análise em que os topos e fundos são buscados |
| `fimathe-mark` | `auto` | `auto`, `wick` ou `body` |
| `fimathe-min-channel-atr` / `fimathe-max-channel-atr` | `1` / `8` | Larguras aceitas do canal, em ATR do tempo de análise |
| `fimathe-stop-buffer-atr` | `0.1` | Folga do stop além da zona neutra, em ATR da execução |
| `fimathe-target-levels` | `0` | Alvo em alturas do canal a partir da linha de entrada (0: sem alvo; documentado: 2) |
| `fimathe-trail` | `surf` | `surf`, `lock` ou `off` |
| `fimathe-trail-step` | `0.5` | Passo do subciclo (0.5 ou 1) |
| `fimathe-subcycle-pivot` | `3` | Candles de cada lado do topo/fundo do subciclo |
| `max-risk-pct` (ou `fimathe-max-risk-pct`) | `3` | Perda máxima no stop, em % do saldo livre (0: desligado); o parâmetro comum a todas as estratégias, com o padrão do Fimathe. Os dois nomes juntos são recusados |
| `max-bars`, `direction`, `lot`, `position-fraction`, `leverage`, `min-position-value` | do runner | Tempo máximo da operação, lado, tamanho e controles de risco comuns a todas as estratégias |

`stop-atr`, `take-profit-atr`, `trailing-atr` e `min-atr-pct` são recusados com `fimathe`. `stop-value` e `take-profit-value` também, porque o stop é técnico.

## Ambiguidades e pendências

Pontos sem informação verificável, que aguardam confirmação antes de virar regra:

1. **"perder os 100% do canal"** (caso do preço acima dos 50%): o texto é ambíguo. A implementação segue o exemplo resolvido da aula (rompimento da borda do canal).
2. **A que se referem os 50% e os 100%:** o texto alterna entre o canal e a zona neutra. A implementação usa o canal.
3. **Stop da entrada nos 50%:** "dois níveis abaixo" a partir dos 50% daria um ponto diferente de "abaixo da zona neutra". A implementação usa a zona neutra, que coincide com "dois níveis" para a entrada na borda.
4. **"mais um ponto"** no subciclo: implementado como um passo do subciclo.
5. **Entrada de reversão:** a venda ao perder a zona neutra (na alta) é documentada, mas o stop dela não. Não implementada: hoje a reversão só encerra o mapeamento.
6. **Novas entradas no mesmo período** (segunda entrada, ciclos seguintes, níveis de expansão como novos canais) e **remapeamento depois de uma invalidação:** sem regra acessível. Não implementados.
7. **Teoria dos Ciclos Fimathe, Linhas do Equador, teoria macro/micro, "stop fora da caixinha", agrupamento de preços:** só os nomes aparecem nas fontes acessíveis. Não implementados.
8. **Exaustão em 85% e alvo entre 80% e 100% da expansão:** aparecem só num resumo de aluno. Não implementados.
9. **Execução intrabar (toque):** ver as decisões de implementação.
