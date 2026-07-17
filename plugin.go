// Package exampleshipping é o template de plugin de LOGÍSTICA / FRETE do Mira
// Commerce. Ele resolve o caso mais pedido — e mais perigoso — do domínio:
// cotar frete numa TRANSPORTADORA EXTERNA durante o checkout.
//
// O PROBLEMA CENTRAL
//
// O hook shipping.quote é SÍNCRONO e tem 300ms de orçamento. Uma API de
// transportadora responde em 200ms num dia bom, 3s num dia ruim e 0 num
// incidente. Chamar a transportadora "direto" no hook significa:
//
//	transportadora lenta → hook estoura → PLUGIN_FAILURE → checkout SEM FRETE
//
// ou seja: a instabilidade do seu parceiro logístico vira perda de venda.
//
// O PADRÃO CORRETO (implementado aqui)
//
//  1. CACHE com TTL: a cotação é resolvida por (CEP, peso, itens); a mesma
//     rota se repete muito. Cache hit responde em microssegundos.
//  2. TIMEOUT PRÓPRIO, bem menor que o do hook: se a transportadora não
//     responde em ~150ms, desistimos NÓS — antes que o host desista de nós.
//  3. FALLBACK NÃO-BLOQUEANTE: no miss/erro/timeout devolvemos Allowed() SEM
//     opções → o core PRESERVA as opções que já tinha (tabela interna do
//     tenant). Frete externo é enriquecimento, não dependência.
//  4. CIRCUIT BREAKER: depois de N falhas seguidas, paramos de tentar por um
//     tempo — não adianta gastar 150ms de cada checkout numa API que caiu.
//
// A regra de ouro: NUNCA devolva Denied() porque a transportadora falhou.
// Deny bloqueia a cotação inteira do comprador. O parceiro estar fora do ar é
// problema seu, não do cliente que quer comprar.
//
// PRODUÇÃO: o cache em memória daqui é didático (morre no restart, não é
// compartilhado entre réplicas). Estado real de plugin vai para Postgres no
// schema ext_<plugin> ou um cache compartilhado — ver o guia.
//
// Guia do modelo de extensibilidade:
// https://github.com/mira-dev-tech/mira-commerce-plugin-example
package exampleshipping

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	commerceext "github.com/mira-dev-tech/commerce-ext"
)

const (
	// defaultCarrierTimeout é o NOSSO limite — deixa folga dentro dos 300ms do
	// hook para serialização, cache e o resto da cadeia de plugins.
	defaultCarrierTimeout = 150 * time.Millisecond
	defaultCacheTTL       = 10 * time.Minute
	defaultBreakerErrors  = 3                // falhas seguidas para abrir o circuito
	defaultBreakerCooloff = 30 * time.Second // tempo com o circuito aberto
)

// Plugin implementa commerceext.Plugin.
type Plugin struct {
	logger commerceext.Logger

	baseURL string
	apiKey  string
	timeout time.Duration
	ttl     time.Duration
	http    *http.Client

	mu      sync.Mutex
	cache   map[string]cacheEntry
	fails   int
	openTil time.Time
}

type cacheEntry struct {
	options []commerceext.ShippingQuoteLine
	expires time.Time
}

// New devolve a instância do plugin.
func New() *Plugin { return &Plugin{cache: map[string]cacheEntry{}} }

// Meta identifica o plugin.
func (p *Plugin) Meta() commerceext.Meta {
	return commerceext.Meta{
		ID:             "example-shipping",
		Version:        "1.0.0",
		CompatibleCore: "^0.2.0",
		Description:    "Template — cotação de frete em transportadora externa com cache, timeout e fallback",
	}
}

// Init lê config e monta o HTTP client. O client é reusado entre chamadas
// (criar um por request derrete o pool de conexões e o orçamento de latência).
func (p *Plugin) Init(_ context.Context, rt *commerceext.Runtime) error {
	p.logger = commerceext.NopLogger{}
	var cfg map[string]any
	var secrets map[string]string
	if rt != nil {
		if rt.Logger != nil {
			p.logger = rt.Logger
		}
		cfg = rt.Config
		secrets = rt.Secrets
	}
	p.baseURL = strings.TrimRight(configString(cfg, "carrier_base_url", "EXAMPLE_SHIPPING_BASE_URL", ""), "/")
	// Credencial vem de Secrets (nunca de Config, nunca hardcoded, nunca logada).
	p.apiKey = secrets["carrier_api_key"]
	if p.apiKey == "" {
		p.apiKey = os.Getenv("EXAMPLE_SHIPPING_API_KEY")
	}
	p.timeout = time.Duration(configFloat(cfg, "carrier_timeout_ms", "EXAMPLE_SHIPPING_TIMEOUT_MS",
		float32(defaultCarrierTimeout.Milliseconds()))) * time.Millisecond
	p.ttl = time.Duration(configFloat(cfg, "cache_ttl_seconds", "EXAMPLE_SHIPPING_CACHE_TTL_SECONDS",
		float32(defaultCacheTTL.Seconds()))) * time.Second
	p.http = &http.Client{Timeout: p.timeout}

	if p.baseURL == "" {
		// Sem transportadora configurada o plugin vira no-op — o core segue com
		// a tabela interna. Degradar é melhor que falhar no boot.
		p.logger.Warn("example-shipping sem carrier_base_url — plugin em no-op")
	}
	p.logger.Info("example-shipping init",
		"timeout_ms", p.timeout.Milliseconds(), "cache_ttl_s", int(p.ttl.Seconds()), "has_key", p.apiKey != "")
	return nil
}

// Register declara o hook.
func (p *Plugin) Register(reg *commerceext.Registry) error {
	reg.OnShippingQuote(300, p.onShippingQuote)
	return nil
}

// Shutdown libera recursos.
func (p *Plugin) Shutdown(context.Context) error {
	p.http.CloseIdleConnections()
	return nil
}

// onShippingQuote — hook shipping.quote.
//
// Semântica da cadeia (igual à do core):
//   - Cada handler recebe o INPUT ORIGINAL (não o resultado do anterior).
//   - Devolver Options não-vazias SUBSTITUI o conjunto corrente.
//   - Devolver Allowed() SEM opções PRESERVA o que já existe ← nosso fallback.
//   - Deny bloqueia a cotação inteira ← nunca use isso para falha técnica.
func (p *Plugin) onShippingQuote(ctx context.Context, in commerceext.ShippingQuoteInput) commerceext.ShippingQuoteResult {
	if p.baseURL == "" {
		return p.preserve() // no-op: sem transportadora configurada
	}
	key := cacheKey(in)

	if opts, ok := p.fromCache(key); ok {
		return commerceext.ShippingQuoteResult{Options: opts, Outcome: commerceext.Allowed()}
	}
	if p.breakerOpen() {
		// Circuito aberto: nem tentamos — economiza o orçamento do checkout.
		p.logger.Warn("carrier circuit open — usando tabela interna")
		return p.preserve()
	}

	opts, err := p.quoteFromCarrier(ctx, in)
	if err != nil {
		p.recordFailure()
		// A falha do parceiro NUNCA vira deny: o comprador segue com as opções
		// internas. Só o log conta a verdade técnica.
		p.logger.Warn("carrier quote failed — fallback para tabela interna", "err", err.Error())
		return p.preserve()
	}
	p.recordSuccess()
	if len(opts) == 0 {
		return p.preserve() // transportadora não atende esta rota
	}
	p.store(key, opts)
	return commerceext.ShippingQuoteResult{Options: opts, Outcome: commerceext.Allowed()}
}

// preserve devolve o "não mexi" — o core mantém as opções que já tinha.
func (p *Plugin) preserve() commerceext.ShippingQuoteResult {
	return commerceext.ShippingQuoteResult{Outcome: commerceext.Allowed()}
}

// quoteFromCarrier chama a API externa com o NOSSO timeout. O ctx do hook já
// tem o deadline do host; combinamos os dois — o que vencer primeiro manda.
func (p *Plugin) quoteFromCarrier(ctx context.Context, in commerceext.ShippingQuoteInput) ([]commerceext.ShippingQuoteLine, error) {
	reqCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	body, err := json.Marshal(map[string]any{
		"destination_postal_code": postalCode(in),
		"items":                   in.Request["items"],
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, p.baseURL+"/quotes", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	res, err := p.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("carrier HTTP %d", res.StatusCode)
	}

	var payload struct {
		Options []struct {
			Service  string  `json:"service"`
			Price    float32 `json:"price"`
			DaysMin  int     `json:"days_min"`
			DaysMax  int     `json:"days_max"`
			MethodID string  `json:"method_id"`
		} `json:"options"`
	}
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return nil, err
	}

	out := make([]commerceext.ShippingQuoteLine, 0, len(payload.Options))
	for _, o := range payload.Options {
		// MethodID tem de ser um método CONHECIDO do tenant — o core revalida e
		// descarta o que não reconhece. Um método inventado aqui some da lista.
		min, max := o.DaysMin, o.DaysMax
		out = append(out, commerceext.ShippingQuoteLine{
			MethodID:      o.MethodID,
			CarrierCode:   "example-carrier",
			CarrierName:   "Example Carrier",
			MethodCode:    o.Service,
			MethodName:    o.Service,
			TransportMode: "ground",
			Price:         o.Price,
			Currency:      "BRL",
			EtaDaysMin:    &min,
			EtaDaysMax:    &max,
			Provider:      "external",
		})
	}
	return out, nil
}

// ─── Cache ───────────────────────────────────────────────────────────────────

// cacheKey identifica a rota: mesma origem/destino/carga → mesma cotação.
func cacheKey(in commerceext.ShippingQuoteInput) string {
	return fmt.Sprintf("%s|%s|%v", in.MemberID, postalCode(in), in.Request["items"])
}

func postalCode(in commerceext.ShippingQuoteInput) string {
	if in.Request == nil {
		return ""
	}
	if v, ok := in.Request["destination_postal_code"].(string); ok {
		return v
	}
	return ""
}

func (p *Plugin) fromCache(key string) ([]commerceext.ShippingQuoteLine, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.cache[key]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.options, true
}

func (p *Plugin) store(key string, opts []commerceext.ShippingQuoteLine) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cache[key] = cacheEntry{options: opts, expires: time.Now().Add(p.ttl)}
}

// ─── Circuit breaker ─────────────────────────────────────────────────────────

func (p *Plugin) breakerOpen() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return time.Now().Before(p.openTil)
}

func (p *Plugin) recordFailure() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fails++
	if p.fails >= defaultBreakerErrors {
		p.openTil = time.Now().Add(defaultBreakerCooloff)
		p.fails = 0
	}
}

func (p *Plugin) recordSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fails = 0
}

// ─── Config ──────────────────────────────────────────────────────────────────

func configString(cfg map[string]any, key, envKey, def string) string {
	if cfg != nil {
		if v, ok := cfg[key].(string); ok && v != "" {
			return v
		}
	}
	if raw := strings.TrimSpace(os.Getenv(envKey)); raw != "" {
		return raw
	}
	return def
}

func configFloat(cfg map[string]any, key, envKey string, def float32) float32 {
	if cfg != nil {
		switch v := cfg[key].(type) {
		case float64:
			return float32(v)
		case float32:
			return v
		case int:
			return float32(v)
		case string:
			if f, err := strconv.ParseFloat(v, 32); err == nil {
				return float32(f)
			}
		}
	}
	if raw := os.Getenv(envKey); raw != "" {
		if f, err := strconv.ParseFloat(raw, 32); err == nil && f > 0 {
			return float32(f)
		}
	}
	return def
}
