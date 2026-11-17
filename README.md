# mira-commerce-plugin-shipping

Template de **plugin de logística / frete** do
[Mira Commerce](https://mira-dev.tech): cotar frete numa **transportadora
externa** durante o checkout — o caso mais pedido e o mais fácil de fazer
errado.

```bash
go test ./...   # funciona logo após o clone — SDK vendorado, transportadora falsa via httptest
```

> Novo no modelo de plugins? Comece pelo template genérico
> [`mira-commerce-plugin-example`](https://github.com/mira-dev-tech/mira-commerce-plugin-example)
> — ele ensina o contrato e o ciclo de vida. Aqui focamos no que é
> **específico de frete**.

## O problema (leia antes do código)

O hook `shipping.quote` é **síncrono e tem 300ms**. Uma API de transportadora
responde em 200ms num dia bom, 3s num dia ruim, e nada num incidente. Chamar a
transportadora "direto" no hook produz isto:

```text
transportadora lenta → hook estoura 300ms → PLUGIN_FAILURE → checkout SEM FRETE
```

Ou seja: **a instabilidade do seu parceiro logístico vira perda de venda na
sua loja**. O teste `TestNaiveExternalCallBlowsTheBudget` demonstra o
comportamento, para você ver o problema antes que ele apareça numa Black
Friday.

## O padrão correto (o que este template implementa)

| Peça | Por quê |
|------|---------|
| **Cache com TTL** por (tenant, CEP, itens) | A mesma rota se repete muito; hit responde em microssegundos |
| **Timeout próprio (~150ms)**, menor que o do hook | Desistimos **nós**, antes que o host desista de nós — sobra orçamento para o resto da cadeia |
| **Fallback não-bloqueante** | No miss/erro/timeout devolvemos `Allowed()` **sem opções** → o core **preserva** a tabela interna do tenant |
| **Circuit breaker** | Depois de N falhas seguidas, para de tentar por um tempo — não gaste 150ms de cada checkout numa API que caiu |

### A regra de ouro

**Nunca devolva `Denied()` porque a transportadora falhou.** Deny bloqueia a
cotação inteira do comprador. O parceiro estar fora do ar é problema seu, não
de quem está tentando comprar. O teste
`TestCarrierDownPreservesInternalOptionsThroughHost` trava esse comportamento:
transportadora em 503 → comprador segue com as opções internas.

## Semântica de cadeia do `shipping.quote`

Vários plugins podem cotar frete. Prioridade menor roda primeiro.

- Cada handler recebe o **input original** — não o resultado do anterior.
- Devolver `Options` **não-vazias** → **substitui** o conjunto corrente.
- Devolver `Allowed()` **sem opções** → **preserva** o que já existe (o nosso
  fallback).
- `Denied()` → bloqueia a cotação (use só para regra de negócio real, ex.:
  "não entregamos nesta região", nunca para falha técnica).
- O `MethodID` devolvido tem de ser um **método conhecido do tenant** — o core
  revalida e descarta o que não reconhece (frete adulterado no front não passa).

## Configuração

| Chave (Config) | Env | Default |
|----------------|-----|---------|
| `carrier_base_url` | `EXAMPLE_SHIPPING_BASE_URL` | — (vazio = plugin vira no-op) |
| `carrier_timeout_ms` | `EXAMPLE_SHIPPING_TIMEOUT_MS` | `150` |
| `cache_ttl_seconds` | `EXAMPLE_SHIPPING_CACHE_TTL_SECONDS` | `600` |

A credencial da transportadora vem de **`Runtime.Secrets["carrier_api_key"]`**
— nunca de Config, nunca no código, nunca logada. Sem `carrier_base_url` o
plugin degrada para no-op (o core segue com a tabela interna) em vez de falhar
no boot.

## Testando

| Nível | Arquivo | O que prova |
|-------|---------|-------------|
| Unitário | [`plugin_test.go`](plugin_test.go) | Transportadora falsa (`httptest`): conversão de opções, header de auth, fallback em 5xx, timeout, cache (hit/expiração), circuit breaker, no-op |
| Integração | [`integration_test.go`](integration_test.go) | Cadeia real via `plugintest`: opções externas substituindo as internas, preservação no fallback, e o contra-exemplo do hook lento |

`httptest` é o jeito certo de testar integração externa: você controla
latência, erro e payload — sem depender da rede do parceiro no CI.

## Produção

- **Cache em memória é didático**: morre no restart e não é compartilhado entre
  réplicas. Em produção use Postgres no schema `ext_<plugin>` (ADR 0004) ou um
  cache compartilhado — dimensione o TTL pela volatilidade da tabela de frete.
- **Observe o fallback**: uma métrica/alerta de "cotações servidas pelo
  fallback" avisa que o parceiro degradou **antes** do time comercial reclamar.
- **Métodos**: alinhe os `method_id` com os métodos cadastrados no tenant, ou
  o core descarta silenciosamente as opções.

## Como o binário sobe

A API não arranca este processo e `MIRA_MANIFEST_PATH` não é o deploy. O
`main` é `commerceext.Serve`. O handshake é a prova do CI:

```bash
go build -o bin/example-shipping ./cmd/example-shipping
./bin/example-shipping --commerce-ext-handshake   # commerce-ext-ok
```

Publicar é a tag `vX.Y.Z` no commit verde. A Mirá constrói esse SHA e grava a release. O caminho completo está no [`AGENTS.md`](AGENTS.md) e no [template genérico](https://github.com/mira-dev-tech/mira-commerce-plugin-example#colocando-em-produ%C3%A7%C3%A3o).

## Ecossistema Mirá Commerce

📚 **Documentação pública: [docs.mira-dev.tech](https://docs.mira-dev.tech)**

| Repo | O que é |
|---|---|
| [mira-commerce-sdk-js](https://github.com/mira-dev-tech/mira-commerce-sdk-js) | SDKs oficiais JS/TS — `client-sdk` (API tipada) e `checkout-sdk` (checkout headless) |
| [mira-commerce-storefront-guide](https://github.com/mira-dev-tech/mira-commerce-storefront-guide) | Guia oficial para construir um storefront (vitrine + checkout) sobre as APIs |
| [commerce-ext](https://github.com/mira-dev-tech/commerce-ext) | SDK público de plugins — Extension Protocol |
| [mira-commerce-plugin-example](https://github.com/mira-dev-tech/mira-commerce-plugin-example) | Template de plugin — exemplo didático (hooks, eventos, manifest, go-plugin) |
| [mira-commerce-plugin-payment](https://github.com/mira-dev-tech/mira-commerce-plugin-payment) | Template de plugin de pagamento e antifraude (roteamento, 3DS, velocity) |
| [mira-commerce-plugin-shipping](https://github.com/mira-dev-tech/mira-commerce-plugin-shipping) | Template de plugin de logística/frete (cotação externa, cache, circuit breaker) |
