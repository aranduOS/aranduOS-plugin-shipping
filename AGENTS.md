# AGENTS.md — plugin de frete

Template de parceiro para `shipping.quote`. A única dependência de plataforma
é `github.com/mira-dev-tech/commerce-ext`. O contrato de publicação é o mesmo
do
[mira-commerce-plugin-example](https://github.com/mira-dev-tech/mira-commerce-plugin-example/blob/main/AGENTS.md).
O que muda aqui é uma API externa dentro do tecto do hook.

## Regras deste domínio

1. A transportadora responde em menos de 300ms no caminho quente, com cache,
   timeout próprio e fallback. A razão: o hook segura o checkout. Estourar
   vira `plugin_failure` para o pedido inteiro.

2. Falha técnica não é deny. Devolve o fallback. A razão: a transportadora
   em baixo não pode impedir a venda. Deny fica para uma regra de negócio
   (não atendemos este CEP), com código estável.

3. Há breaker. A razão: sem ele, cada checkout espera o timeout da
   transportadora já caída.

4. O cache em memória deste exemplo morre no restart. Estado a sério vai
   para o schema `ext_<id>`, que o plugin não cria e cuja DSN ele não vê.
   A razão: quem liga ao Postgres é o host, com um role sem `BYPASSRLS`.

5. Não clone o monorepo, não use `MIRA_MANIFEST_PATH`, não peça DSN, não
   mande preço nem chave de idempotência, não grave a release com o token
   de parceiro e não marque o pin `active`. As razões estão no AGENTS.md do
   template genérico.

## Publicar

PR com `go test ./...` e o handshake verdes. Tag `vX.Y.Z`. O workflow da
Mirá constrói esse SHA e grava a release. A primeira versão de um id novo
espera review. Ligar a uma empresa é outro disparo, e só numa empresa que o
registo atribua a este parceiro.
