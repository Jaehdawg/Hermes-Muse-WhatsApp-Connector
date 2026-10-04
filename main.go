// Hermes WhatsApp native bot transport.
//
// WhatsApp's Muse bot requires the WASA encrypted request path, which Baileys
// does not implement. This small loopback-only sidecar uses whatsmeow
// for @bot traffic and deliberately owns a separate companion-device session.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

type server struct {
	client         *whatsmeow.Client
	mu             sync.Mutex
	lastPairCode   string
	lastPairCodeAt time.Time
	inbound        []map[string]any
}

type sendRequest struct {
	ChatID  string `json:"chatId"`
	Message string `json:"message"`
}

type pairRequest struct {
	Phone string `json:"phone"`
}

func main() {
	port := env("HERMES_NATIVE_BOT_PORT", "3001")
	dbPath := env("HERMES_NATIVE_BOT_DB", filepath.Join(os.Getenv("HOME"), ".hermes", "whatsapp", "native-bot", "store.db"))
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	container, err := sqlstore.New(ctx, "sqlite3", "file:"+dbPath+"?_foreign_keys=on", waLog.Stdout("NativeBotDB", "WARN", true))
	if err != nil {
		log.Fatalf("open native bot store: %v", err)
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		log.Fatalf("load native bot device: %v", err)
	}
	client := whatsmeow.NewClient(device, waLog.Stdout("NativeBot", "WARN", true))
	s := &server{client: client}
	client.AddEventHandler(s.handleEvent)
	if err := client.Connect(); err != nil {
		log.Fatalf("connect native bot session: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /messages", s.messages)
	mux.HandleFunc("POST /send", s.send)
	mux.HandleFunc("POST /pair-code", s.pairCode)
	h := &http.Server{Addr: "127.0.0.1:" + port, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("native Muse transport listening on %s", h.Addr)
	log.Fatal(h.ListenAndServe())
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"connected": s.client.IsConnected(), "paired": s.client.Store.ID != nil})
}

func (s *server) messages(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.inbound
	s.inbound = nil
	writeJSON(w, http.StatusOK, items)
}

func (s *server) handleEvent(raw any) {
	event, ok := raw.(*events.Message)
	if !ok || event.Info.IsFromMe || !strings.HasSuffix(event.Info.Chat.String(), "@bot") {
		return
	}
	text := event.Message.GetConversation()
	if text == "" {
		text = event.Message.GetExtendedTextMessage().GetText()
	}
	if text == "" {
		text = richResponseText(event.Message.GetRichResponseMessage().GetUnifiedResponse().GetData())
	}
	if text == "" {
		return
	}
	s.mu.Lock()
	s.inbound = append(s.inbound, map[string]any{
		"chatId": event.Info.Chat.String(), "messageId": event.Info.ID,
		"sender": event.Info.Sender.String(), "text": text, "timestamp": event.Info.Timestamp,
	})
	s.mu.Unlock()
}

func richResponseText(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return findText(value)
}

func findText(value any) string {
	switch v := value.(type) {
	case map[string]any:
		if text, ok := v["text"].(string); ok && text != "" {
			return text
		}
		for _, child := range v {
			if text := findText(child); text != "" {
				return text
			}
		}
	case []any:
		for _, child := range v {
			if text := findText(child); text != "" {
				return text
			}
		}
	}
	return ""
}

func (s *server) send(w http.ResponseWriter, r *http.Request) {
	var req sendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if !strings.HasSuffix(req.ChatID, "@bot") {
		fail(w, http.StatusBadRequest, errors.New("native transport accepts only @bot chats"))
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		fail(w, http.StatusBadRequest, errors.New("message is required"))
		return
	}
	if s.client.Store.ID == nil || !s.client.IsConnected() {
		fail(w, http.StatusServiceUnavailable, errors.New("native bot transport is not paired and connected"))
		return
	}
	chat, err := types.ParseJID(req.ChatID)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	resp, err := s.client.SendMessage(r.Context(), chat, &waE2E.Message{Conversation: proto.String(req.Message)})
	if err != nil {
		fail(w, http.StatusBadGateway, fmt.Errorf("send Muse message: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "messageId": resp.ID, "timestamp": resp.Timestamp})
}

func (s *server) pairCode(w http.ResponseWriter, r *http.Request) {
	var req pairRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	phone := strings.TrimPrefix(strings.ReplaceAll(strings.TrimSpace(req.Phone), " ", ""), "+")
	if phone == "" {
		fail(w, http.StatusBadRequest, errors.New("phone is required in E.164 digits"))
		return
	}
	if s.client.Store.ID != nil {
		fail(w, http.StatusConflict, errors.New("native bot transport is already paired"))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	code, err := s.client.PairPhone(r.Context(), phone, false, whatsmeow.PairClientChrome, "Chrome (Linux)")
	if err != nil {
		fail(w, http.StatusBadGateway, fmt.Errorf("create pairing code: %w", err))
		return
	}
	s.lastPairCode, s.lastPairCodeAt = code, time.Now()
	writeJSON(w, http.StatusOK, map[string]any{"code": code, "expiresInSeconds": 160})
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func fail(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"success": false, "error": err.Error()})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
