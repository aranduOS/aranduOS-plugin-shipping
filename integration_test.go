package exampleshipping_test

// Testes de INTEGRAÇÃO com o plugintest — o mini Extension Host em memória do
// SDK, que reproduz as semânticas do host real. Aqui provamos o que mais
// importa no domínio de frete: o plugin se comporta bem DENTRO da cadeia,
// inclusive quando a transportadora falha.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	commerceext "github.com/mira-dev-tech/commerce-ext"
	"github.com/mira-dev-tech/commerce-ext/plugintest"
	exampleshipping "github.com/aranduOS/aranduOS-plugin-shipping"
)

const payload = `{"options":[{"service":"expresso","price":29.9,"days_min":1,"days_max":2,"method_id":"11111111-1111-1111-1111-111111111111"}]}`

func hostWith(t *testing.T, baseURL string) *plugintest.Host {
	t.Helper()
	host := plugintest.NewHost()
	err := host.Install(context.Background(), exampleshipping.New(),
		plugintest.WithConfig(map[string]any{"carrier_base_url": baseURL}))
	if err != nil {
		t.Fatal(err)
	}
	return host
}

func quoteInput() commerceext.ShippingQuoteInput {
	return commerceext.ShippingQuoteInput{
		MemberID: "m1",
		Request:  map[string]any{"destination_postal_code": "01310-100"},
		Options: []commerceext.ShippingQuoteLine{
			{MethodID: "99999999-9999-9999-9999-999999999999", CarrierCode: "interna", Price: 25.0},
		},
	}
}

func TestCarrierOptionsReplaceInternalThroughHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(payload))
	}))
	defer srv.Close()

	out := hostWith(t, srv.URL).ShippingQuote(context.Background(), quoteInput())
	if !out.Allow || len(out.Options) != 1 || out.Options[0].Price != 29.9 {
		t.Fatalf("expected opção da transportadora pela cadeia, got %+v", out)
	}
}

// A prova de fogo do domínio: transportadora fora do ar → o core PRESERVA as
// opções internas e o comprador continua comprando.
func TestCarrierDownPreservesInternalOptionsThroughHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	in := quoteInput()
	out := hostWith(t, srv.URL).ShippingQuote(context.Background(), in)
	if !out.Allow {
		t.Fatalf("falha da transportadora não pode bloquear: %+v", out)
	}
	// O host preserva o conjunto corrente quando o plugin não devolve opções.
	if len(out.Options) != 1 || out.Options[0].CarrierCode != "interna" {
		t.Fatalf("esperava a tabela interna preservada, got %+v", out.Options)
	}
}

// Mostra o que acontece se você ignorar o padrão deste template e chamar a
// transportadora sem timeout próprio: o host corta em 300ms e a cotação inteira
// vira PLUGIN_FAILURE — o comprador fica SEM frete.
type naivePlugin struct{ delay time.Duration }

func (naivePlugin) Meta() commerceext.Meta {
	return commerceext.Meta{ID: "naive", Version: "1.0.0", CompatibleCore: "^0.2.0"}
}
func (naivePlugin) Init(context.Context, *commerceext.Runtime) error { return nil }
func (naivePlugin) Shutdown(context.Context) error                   { return nil }
func (p naivePlugin) Register(reg *commerceext.Registry) error {
	reg.OnShippingQuote(100, func(_ context.Context, in commerceext.ShippingQuoteInput) commerceext.ShippingQuoteResult {
		time.Sleep(p.delay) // ← chamada externa sem timeout próprio
		return commerceext.ShippingQuoteResult{Options: in.Options, Outcome: commerceext.Allowed()}
	})
	return nil
}

func TestNaiveExternalCallBlowsTheBudget(t *testing.T) {
	host := plugintest.NewHost()
	host.SetTimeout(50 * time.Millisecond) // acelera o teste; o core usa 300ms
	if err := host.Install(context.Background(), naivePlugin{delay: 200 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	out := host.ShippingQuote(context.Background(), quoteInput())
	if out.Allow {
		t.Fatal("o host deveria ter cortado o hook lento")
	}
	if out.Code != plugintest.CodePluginFailure {
		t.Fatalf("expected %s, got %+v", plugintest.CodePluginFailure, out)
	}
	// Moral: é isto que o cache + timeout próprio + fallback deste template evitam.
}
