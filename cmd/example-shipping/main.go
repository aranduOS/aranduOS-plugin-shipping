// Command example-shipping empacota o plugin como BINÁRIO EXTERNO (go-plugin):
// o Extension Host do core sobe este processo e conversa com ele por RPC.
//
// Build:  go build -o bin/example-shipping ./cmd/example-shipping
// Teste:  ./bin/example-shipping --commerce-ext-handshake   → "commerce-ext-ok"
package main

import (
	commerceext "github.com/mira-dev-tech/commerce-ext"

	exampleshipping "github.com/mira-dev-tech/mira-commerce-plugin-shipping"
)

func main() {
	commerceext.Serve(exampleshipping.New())
}
