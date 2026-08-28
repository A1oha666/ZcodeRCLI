package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/itwanger/zcoder-go/internal/agent"
)

type Server struct {
	addr         string
	apiKey       string
	agentFactory func() *agent.Agent
	mu           sync.Mutex
	threads      map[string]*Thread
}

type Thread struct {
	ID     string  `json:"id"`
	Events []Event `json:"events"`
}

type Event struct {
	Type string    `json:"type"`
	Time time.Time `json:"time"`
	Data any       `json:"data"`
}

func NewServer(addr, apiKey string, factory func() *agent.Agent) *Server {
	return &Server{addr: addr, apiKey: apiKey, agentFactory: factory, threads: map[string]*Thread{}}
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/threads", s.auth(s.threadsHandler))
	mux.HandleFunc("/v1/threads/", s.auth(s.threadHandler))
	server := &http.Server{Addr: s.addr, Handler: mux}
	errc := make(chan error, 1)
	go func() { errc <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		return nil
	case err := <-errc:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func (s *Server) threadsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	thread := &Thread{ID: "thread-" + time.Now().Format("20060102150405.000000000")}
	thread.Events = append(thread.Events, Event{Type: "thread.created", Time: time.Now(), Data: map[string]string{"id": thread.ID}})
	s.mu.Lock()
	s.threads[thread.ID] = thread
	s.mu.Unlock()
	writeJSON(w, thread)
}

func (s *Server) threadHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/threads/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	id, action := parts[0], parts[1]
	s.mu.Lock()
	thread := s.threads[id]
	s.mu.Unlock()
	if thread == nil {
		http.NotFound(w, r)
		return
	}
	switch action {
	case "turns":
		s.turnHandler(w, r, thread)
	case "events":
		s.eventsHandler(w, r, thread)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) turnHandler(w http.ResponseWriter, r *http.Request, thread *Thread) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Input string `json:"input"`
		Mode  string `json:"mode"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || strings.TrimSpace(req.Input) == "" {
		http.Error(w, "input is required", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Mode) != "" && !agent.IsRunMode(agent.RunMode(req.Mode)) {
		http.Error(w, "mode must be one of: react, plan, team", http.StatusBadRequest)
		return
	}
	turnID := "turn-" + time.Now().Format("20060102150405.000000000")
	s.appendEvent(thread, "turn.started", map[string]string{"id": turnID})
	go func() {
		var (
			answer string
			err    error
		)
		if strings.TrimSpace(req.Mode) != "" {
			answer, err = s.agentFactory().RunMode(context.Background(), agent.RunMode(req.Mode), req.Input)
		} else {
			answer, err = s.agentFactory().RunCommand(context.Background(), req.Input)
		}
		if err != nil {
			s.appendEvent(thread, "turn.failed", map[string]any{"id": turnID, "error": err.Error()})
			return
		}
		s.appendEvent(thread, "message.delta", map[string]string{"id": turnID, "delta": answer})
		s.appendEvent(thread, "turn.completed", map[string]string{"id": turnID})
	}()
	writeJSON(w, map[string]string{"id": turnID, "status": "running"})
}

func (s *Server) eventsHandler(w http.ResponseWriter, r *http.Request, thread *Thread) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	s.mu.Lock()
	events := append([]Event(nil), thread.Events...)
	s.mu.Unlock()
	for _, event := range events {
		b, _ := json.Marshal(event)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, b)
	}
}

func (s *Server) appendEvent(thread *Thread, typ string, data any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	thread.Events = append(thread.Events, Event{Type: typ, Time: time.Now(), Data: data})
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if auth == "" {
			auth = r.Header.Get("X-Zcoder-API-Key")
		}
		if auth != s.apiKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
