package interaction

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/riverfjs/aevitas/internal/config"
)

const (
	defaultListenAddr = "127.0.0.1:18901"
	defaultInbound    = "/interaction/inbound"
	defaultOutbound   = "http://127.0.0.1:18902/interaction/outbound"
	defaultTimeoutSec = 15
)

type HTTPTransport struct {
	cfg        config.InteractionConfig
	httpClient *http.Client
	server     *http.Server
	onInbound  func(InboundEvent)
}

func NewHTTPTransport(cfg config.InteractionConfig, onInbound func(InboundEvent)) (*HTTPTransport, error) {
	cfg.OutboundURL = outboundURL(cfg)
	return &HTTPTransport{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: time.Duration(timeoutSec(cfg)) * time.Second,
		},
		onInbound: onInbound,
	}, nil
}

func (t *HTTPTransport) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc(inboundPath(t.cfg), t.handleInbound)
	t.server = &http.Server{Addr: listenAddr(t.cfg), Handler: mux}
	go func() {
		_ = t.server.ListenAndServe()
	}()
	go func() {
		<-ctx.Done()
		_ = t.server.Close()
	}()
	return nil
}

func (t *HTTPTransport) Stop() error {
	if t.server != nil {
		return t.server.Close()
	}
	return nil
}

func (t *HTTPTransport) Send(ctx context.Context, action OutboundAction) error {
	_, err := t.SendWithAck(ctx, action)
	return err
}

func (t *HTTPTransport) SendWithAck(ctx context.Context, action OutboundAction) (map[string]any, error) {
	if strings.TrimSpace(action.RequestID) == "" {
		action.RequestID = strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	body, err := json.Marshal(action)
	if err != nil {
		return nil, fmt.Errorf("marshal outbound action: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, actionOutboundURL(action, t.cfg), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create outbound request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token := strings.TrimSpace(t.cfg.AuthToken); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send outbound action: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("outbound status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, nil
	}
	var ack map[string]any
	if err := json.Unmarshal(raw, &ack); err != nil {
		return nil, fmt.Errorf("decode outbound ack: %w", err)
	}
	return ack, nil
}

func (t *HTTPTransport) handleInbound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !t.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	var evt InboundEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if t.onInbound != nil {
		t.onInbound(evt)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(Ack{Accepted: true, EventID: evt.EventID})
}

func (t *HTTPTransport) authorized(r *http.Request) bool {
	token := strings.TrimSpace(t.cfg.AuthToken)
	if token == "" {
		return true
	}
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	const prefix = "Bearer "
	return strings.HasPrefix(header, prefix) && strings.TrimSpace(strings.TrimPrefix(header, prefix)) == token
}

func listenAddr(cfg config.InteractionConfig) string {
	if v := strings.TrimSpace(cfg.ListenAddr); v != "" {
		return v
	}
	return defaultListenAddr
}

func inboundPath(cfg config.InteractionConfig) string {
	v := strings.TrimSpace(cfg.InboundPath)
	if v == "" {
		return defaultInbound
	}
	if strings.HasPrefix(v, "/") {
		return v
	}
	return "/" + v
}

func timeoutSec(cfg config.InteractionConfig) int {
	if cfg.RequestTimeoutSec > 0 {
		return cfg.RequestTimeoutSec
	}
	return defaultTimeoutSec
}

func outboundURL(cfg config.InteractionConfig) string {
	if v := strings.TrimSpace(cfg.OutboundURL); v != "" {
		return v
	}
	return defaultOutbound
}

func actionOutboundURL(action OutboundAction, cfg config.InteractionConfig) string {
	if action.Metadata != nil {
		if v, ok := action.Metadata["outbound_url"].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		if v, ok := action.Metadata["outboundUrl"].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return outboundURL(cfg)
}
