package exampleshipping

// Testes UNITÁRIOS com uma transportadora FALSA (httptest) — é assim que se
// testa integração externa sem depender da rede do parceiro: você controla a
// latência, o erro e o payload.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	commerceext "github.com/mira-dev-tech/commerce-ext"
)

const okPayload = `{"options":[
  {"service":"expresso","price":39.9,"days_min":1,"days_max":2,"method_id":"11111111-1111-1111-1111-111111111111"},
  {"service":"economico","price":19.9,"days_min":5,"days_max":8,"method_id":"22222222-2222-2222-2222-222222222222"}
]}`

// carrier devolve um servidor falso + contador de chamadas.
func carrier(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func newInited(t *testing.T, baseURL string, cfg map[string]any) *Plugin {
	t.Helper()
	p := New()
	if cfg == nil {
		cfg = map[string]any{}
	}
	cfg["carrier_base_url"] = baseURL
	if err := p.Init(context.Background(), &commerceext.Runtime{
		Config:  cfg,
		Secrets: map[string]string{"carrier_api_key": "chave-de-teste"},
	}); err != nil {
		t.Fatal(err)
	}
	return p
}

func input() commerceext.ShippingQuoteInput {
	return commerceext.ShippingQuoteInput{
		MemberID: "m1",
		Request: map[string]any{
			"destination_postal_code": "01310-100",
			"items":                   []any{map[string]any{"sku": "SKU-001", "quantity": 2}},
		},
		Options: []commerceext.ShippingQuoteLine{
			{MethodID: "99999999-9999-9999-9999-999999999999", CarrierCode: "interna", Price: 25.0},
		},
	}
}

func TestQuoteFromCarrierReplacesOptions(t *testing.T) {
	srv, _ := carrier(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(okPayload))
	})
	p := newInited(t, srv.URL, nil)
	out := p.onShippingQuote(context.Background(), input())
	if !out.Allow || len(out.Options) != 2 {
		t.Fatalf("expected 2 opções da transportadora, got %+v", out)
	}
	if out.Options[0].Price != 39.9 || out.Options[0].Provider != "external" {
		t.Fatalf("opção mal convertida: %+v", out.Options[0])
	}
}

func TestCarrierAuthHeaderIsSent(t *testing.T) {
	var gotAuth string
	srv, _ := carrier(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(okPayload))
	})
	p := newInited(t, srv.URL, nil)
	p.onShippingQuote(context.Background(), input())
	if gotAuth != "Bearer chave-de-teste" {
		t.Fatalf("credencial do Secrets não chegou à transportadora: %q", gotAuth)
	}
}

// O teste mais importante do domínio: transportadora fora do ar NÃO pode
// bloquear o checkout — o comprador segue com a tabela interna.
func TestCarrierDownFallsBackWithoutBlocking(t *testing.T) {
	srv, _ := carrier(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	p := newInited(t, srv.URL, nil)
	out := p.onShippingQuote(context.Background(), input())
	if !out.Allow {
		t.Fatalf("falha da transportadora NUNCA pode bloquear a cotação: %+v", out)
	}
	if len(out.Options) != 0 {
		t.Fatalf("fallback devolve zero opções p/ o core preservar as internas, got %+v", out.Options)
	}
}

func TestSlowCarrierTimesOutAndFallsBack(t *testing.T) {
	srv, _ := carrier(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond) // mais lento que o nosso timeout
		w.Write([]byte(okPayload))
	})
	p := newInited(t, srv.URL, map[string]any{"carrier_timeout_ms": 50})
	start := time.Now()
	out := p.onShippingQuote(context.Background(), input())
	elapsed := time.Since(start)

	if !out.Allow || len(out.Options) != 0 {
		t.Fatalf("timeout deve cair no fallback, got %+v", out)
	}
	// O ponto: desistimos ANTES do orçamento de 300ms do hook.
	if elapsed > 250*time.Millisecond {
		t.Fatalf("nosso timeout não protegeu o orçamento do hook: %v", elapsed)
	}
}

func TestCacheAvoidsSecondCall(t *testing.T) {
	srv, calls := carrier(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(okPayload))
	})
	p := newInited(t, srv.URL, nil)
	p.onShippingQuote(context.Background(), input())
	p.onShippingQuote(context.Background(), input()) // mesma rota
	if calls.Load() != 1 {
		t.Fatalf("segunda cotação da mesma rota deveria vir do cache, chamadas=%d", calls.Load())
	}
}

func TestCacheExpires(t *testing.T) {
	srv, calls := carrier(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(okPayload))
	})
	p := newInited(t, srv.URL, map[string]any{"cache_ttl_seconds": 0.05})
	p.onShippingQuote(context.Background(), input())
	time.Sleep(80 * time.Millisecond)
	p.onShippingQuote(context.Background(), input())
	if calls.Load() != 2 {
		t.Fatalf("após o TTL deveria cotar de novo, chamadas=%d", calls.Load())
	}
}

func TestCircuitBreakerStopsCallingBrokenCarrier(t *testing.T) {
	srv, calls := carrier(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	p := newInited(t, srv.URL, nil)
	// 3 falhas seguidas abrem o circuito (defaultBreakerErrors).
	for i := 0; i < 5; i++ {
		out := p.onShippingQuote(context.Background(), input())
		if !out.Allow {
			t.Fatalf("fallback deve sempre permitir, got %+v", out)
		}
	}
	if calls.Load() > 3 {
		t.Fatalf("circuito aberto deveria parar de chamar, chamadas=%d", calls.Load())
	}
}

func TestNoCarrierConfiguredIsNoOp(t *testing.T) {
	p := New()
	if err := p.Init(context.Background(), &commerceext.Runtime{Config: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	out := p.onShippingQuote(context.Background(), input())
	if !out.Allow || len(out.Options) != 0 {
		t.Fatalf("sem transportadora o plugin é no-op, got %+v", out)
	}
}

func TestRegisterMatchesManifest(t *testing.T) {
	p := newInited(t, "http://exemplo", nil)
	reg := commerceext.NewRegistry(p.Meta().ID)
	if err := p.Register(reg); err != nil {
		t.Fatal(err)
	}
	if len(reg.Hooks()) != 1 || reg.Hooks()[0].HookID != commerceext.HookShippingQuote {
		t.Fatalf("hooks divergem do manifest: %+v", reg.Hooks())
	}
}
