package proxyhttp

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"grok-desktop/internal/store"
	"grok-desktop/internal/upstream"
)

// Server is a local OpenAI-compatible reverse proxy using multi-account store.
type Server struct {
	mu       sync.Mutex
	store    *store.Store
	upstream *upstream.Client
	ensure   func(ctx context.Context) (token string, account *store.Account, settings store.Settings, err error)
	srv      *http.Server
	ln       net.Listener
	addr     string
}

func New(
	st *store.Store,
	up *upstream.Client,
	ensure func(ctx context.Context) (string, *store.Account, store.Settings, error),
) *Server {
	return &Server{store: st, upstream: up, ensure: ensure}
}

func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

func (s *Server) Start(listen string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv != nil {
		return nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/models", s.handleModels)
	mux.HandleFunc("/v1/chat/completions", s.handleChat)
	mux.HandleFunc("/chat/completions", s.handleChat)
	mux.HandleFunc("/v1/responses", s.handleResponses)
	mux.HandleFunc("/responses", s.handleResponses)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":    "grok-desktop-proxy",
				"endpoints": []string{"/v1/models", "/v1/chat/completions", "/v1/responses"},
			})
			return
		}
		http.NotFound(w, r)
	})

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	s.ln = ln
	s.addr = ln.Addr().String()
	s.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 30 * time.Second,
	}
	go func() {
		if err := s.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("proxyhttp: %v", err)
		}
	}()
	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv == nil {
		return nil
	}
	err := s.srv.Shutdown(ctx)
	s.srv = nil
	s.ln = nil
	s.addr = ""
	return err
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	acc, ok := s.store.ActiveAccount()
	email := ""
	if ok {
		email = acc.Email
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  "ok",
		"addr":    s.Addr(),
		"account": email,
	})
}

func (s *Server) gate(r *http.Request) bool {
	key := s.store.Settings().ProxyAPIKey
	if key == "" {
		return true
	}
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") && strings.TrimSpace(auth[7:]) == key {
		return true
	}
	return r.Header.Get("X-API-Key") == key
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if !s.gate(r) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	token, _, settings, err := s.ensure(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	models, err := s.upstream.ListModels(r.Context(), token, settings)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	data := make([]map[string]any, 0, len(models))
	for _, m := range models {
		data = append(data, map[string]any{
			"id": m.ID, "object": "model", "owned_by": "xAI",
			"name": m.Name, "description": m.Description, "api_mode": m.APIMode, "root": m.Root,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	s.proxyUpstream(w, r, "/chat/completions")
}

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	s.proxyUpstream(w, r, "/responses")
}

func (s *Server) proxyUpstream(w http.ResponseWriter, r *http.Request, path string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.gate(r) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	token, _, settings, err := s.ensure(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// normalize effort / previous id aliases lightly
	var m map[string]any
	if json.Unmarshal(body, &m) == nil {
		if _, ok := m["reasoning_effort"]; !ok {
			if settings.ReasoningEffort != "" {
				m["reasoning_effort"] = settings.ReasoningEffort
			}
		}
		if _, ok := m["model"]; !ok || m["model"] == "" {
			m["model"] = settings.DefaultModel
		}
		// alias last_response_id
		if prev, ok := m["last_response_id"].(string); ok && prev != "" {
			m["previous_response_id"] = prev
			delete(m, "last_response_id")
		}
		if path == "/responses" {
			if settings.StoreResponses {
				if _, ok := m["store"]; !ok {
					m["store"] = true
				}
			}
			if _, ok := m["reasoning"]; !ok {
				if eff, _ := m["reasoning_effort"].(string); eff != "" {
					m["reasoning"] = map[string]any{"effort": eff, "summary": "auto"}
				}
			}
		}
		body, _ = json.Marshal(m)
	}

	url := strings.TrimRight(settings.UpstreamBase, "/") + path
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-grok-client-version", settings.ClientVersion)
	if v := r.Header.Get("Accept"); v != "" {
		req.Header.Set("Accept", v)
	} else {
		req.Header.Set("Accept", "text/event-stream, application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vv := range resp.Header {
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
