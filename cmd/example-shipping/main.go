// Command example-shipping é o processo do plugin de parceiro.
// Não é filho da API e não se carrega com MIRA_MANIFEST_PATH.
// No cluster a única env é COMMERCE_EXT_HOST; o host disca o RPC.
//
// Build:  go build -o bin/example-shipping ./cmd/example-shipping
// Prova:  ./bin/example-shipping --commerce-ext-handshake   → "commerce-ext-ok"
package main

import (
	commerceext "github.com/mira-dev-tech/commerce-ext"

	exampleshipping "github.com/mira-dev-tech/mira-commerce-plugin-shipping"
)

func main() {
	commerceext.Serve(exampleshipping.New())
}
