package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	viapost "github.com/ViaPost-io/viapost-go"
)

const contractTestAPIKey = "vp_test_contract_key"

func TestSDKBackendMatchesPublishedSendMessagesAndUsageContract(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+contractTestAPIKey {
			t.Errorf("Authorization = %q", got)
		}

		switch r.URL.Path {
		case "/v1/send":
			if r.Method != http.MethodPost {
				t.Errorf("send method = %s", r.Method)
			}
			if got := r.Header.Get("Idempotency-Key"); got != "send-contract-1" {
				t.Errorf("Idempotency-Key = %q", got)
			}
			var request struct {
				From    string   `json:"from"`
				To      []string `json:"to"`
				Subject string   `json:"subject"`
				Text    string   `json:"text"`
				Stream  string   `json:"stream"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode send request: %v", err)
			}
			if request.From != "sender@example.test" || len(request.To) != 1 || request.To[0] != "recipient@example.test" || request.Subject != "contract" || request.Text != "body" || request.Stream != "transactional" {
				t.Errorf("unexpected send request: %#v", request)
			}
			writeContractJSON(t, w, http.StatusAccepted, map[string]any{
				"accepted": []map[string]string{{"message_id": "00000000-0000-0000-0000-000000000001", "to": "recipient@example.test"}},
				"rejected": []any{},
			})
		case "/v1/messages":
			if r.Method != http.MethodGet {
				t.Errorf("messages method = %s", r.Method)
			}
			query := r.URL.Query()
			if query.Get("limit") != "10" || query.Get("status") != "delivered" || query.Get("search") != "invoice" || query.Get("period") != "7d" || query.Get("api_key_id") != "00000000-0000-0000-0000-000000000002" || query.Get("cursor") != "2026-09-01T00:00:00Z" {
				t.Errorf("unexpected messages query: %s", query.Encode())
			}
			writeContractJSON(t, w, http.StatusOK, map[string]any{"messages": []map[string]any{contractMessage("00000000-0000-0000-0000-000000000003")}})
		case "/v1/messages/00000000-0000-0000-0000-000000000003":
			if r.Method != http.MethodGet {
				t.Errorf("message detail method = %s", r.Method)
			}
			writeContractJSON(t, w, http.StatusOK, contractMessage("00000000-0000-0000-0000-000000000003"))
		case "/v1/usage":
			if r.Method != http.MethodGet {
				t.Errorf("usage method = %s", r.Method)
			}
			writeContractJSON(t, w, http.StatusOK, map[string]any{
				"period":    map[string]string{"start": "2026-09-01T00:00:00Z", "end": "2026-10-01T00:00:00Z", "timezone": "UTC"},
				"used":      42,
				"limit":     100,
				"remaining": 58,
				"unlimited": false,
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	backend, err := newSDKBackend(ClientConfig{APIKey: contractTestAPIKey, BaseURL: server.URL, Timeout: time.Second})
	if err != nil {
		t.Fatalf("new SDK backend: %v", err)
	}

	ctx := context.Background()
	sent, err := backend.Send(ctx, viapost.SendRequest{From: "sender@example.test", To: []string{"recipient@example.test"}, Subject: "contract", Text: "body", Stream: viapost.StreamTransactional}, "send-contract-1")
	if err != nil || len(sent.Accepted) != 1 || sent.Accepted[0].MessageID != "00000000-0000-0000-0000-000000000001" {
		t.Fatalf("send result = %#v, err = %v", sent, err)
	}

	cursor := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	messages, err := backend.ListMessages(ctx, viapost.MessageListOptions{Cursor: &cursor, Limit: 10, Status: "delivered", Search: "invoice", Period: viapost.MessagePeriod7Days, APIKeyID: "00000000-0000-0000-0000-000000000002"})
	if err != nil || len(messages) != 1 || messages[0].ID != "00000000-0000-0000-0000-000000000003" {
		t.Fatalf("messages = %#v, err = %v", messages, err)
	}

	message, err := backend.GetMessage(ctx, "00000000-0000-0000-0000-000000000003")
	if err != nil || message == nil || message.Status != "delivered" {
		t.Fatalf("message = %#v, err = %v", message, err)
	}

	usage, err := backend.GetUsage(ctx)
	if err != nil || usage == nil || usage.Used != 42 || usage.Remaining == nil || *usage.Remaining != 58 {
		t.Fatalf("usage = %#v, err = %v", usage, err)
	}
}

func TestSDKBackendPreservesPublishedAPIErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/usage" {
			http.NotFound(w, r)
			return
		}
		writeContractJSON(t, w, http.StatusForbidden, map[string]any{"error": map[string]string{"code": "missing_scope", "message": "messages:read is required", "request_id": "req-contract-1"}})
	}))
	defer server.Close()

	backend, err := newSDKBackend(ClientConfig{APIKey: contractTestAPIKey, BaseURL: server.URL, Timeout: time.Second})
	if err != nil {
		t.Fatalf("new SDK backend: %v", err)
	}

	_, err = backend.GetUsage(context.Background())
	var apiError *viapost.APIError
	if !errors.As(err, &apiError) || apiError.StatusCode != http.StatusForbidden || apiError.Code != "missing_scope" || apiError.RequestID != "req-contract-1" {
		t.Fatalf("API error = %#v, err = %v", apiError, err)
	}
}

func contractMessage(id string) map[string]any {
	return map[string]any{
		"id":               id,
		"status":           "delivered",
		"stream":           "transactional",
		"from_address":     "sender@example.test",
		"to_address":       "recipient@example.test",
		"recipient_domain": "example.test",
		"created_at":       "2026-09-01T00:00:00Z",
	}
}

func writeContractJSON(t *testing.T, w http.ResponseWriter, status int, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("encode contract response: %v", err)
	}
}
