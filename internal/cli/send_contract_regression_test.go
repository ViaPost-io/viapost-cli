package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	viapost "github.com/ViaPost-io/viapost-go"
)

func sendContractArgs(baseURL string) []string {
	return []string{
		"--allow-custom-base-url", "--base-url", baseURL,
		"send", "--from", "sender@example.test", "--to", "recipient@example.test",
		"--subject", "contract", "--text", "body", "--idempotency-key", "send-contract-1",
	}
}

func contractDependencies() Dependencies {
	return Dependencies{Getenv: func(key string) string {
		if key == "VIAPOST_API_KEY" {
			return contractTestAPIKey
		}
		return ""
	}}
}

func decodeErrorOutput(t *testing.T, stderr string) map[string]any {
	t.Helper()
	start := strings.Index(stderr, `{"error":`)
	if start < 0 {
		t.Fatalf("missing JSON error envelope: %q", stderr)
	}
	var envelope struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal([]byte(stderr[start:]), &envelope); err != nil {
		t.Fatalf("decode error envelope: %v; stderr=%q", err, stderr)
	}
	return envelope.Error
}

func TestSendPublishedHTTPFailuresNeverClaimAcceptanceOrRetry(t *testing.T) {
	cases := []struct {
		name   string
		status int
		code   string
	}{
		{"validation", http.StatusBadRequest, "validation_error"},
		{"unauthorized", http.StatusUnauthorized, "unauthorized"},
		{"missing_scope", http.StatusForbidden, "forbidden"},
		{"conflicting_idempotency_key", http.StatusConflict, "conflict"},
		{"rate_limited", http.StatusTooManyRequests, "rate_limited"},
		{"quota_exceeded", http.StatusTooManyRequests, "quota_exceeded"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var requests, sleeps int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodPost || r.URL.Path != "/v1/send" {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer "+contractTestAPIKey {
					t.Errorf("Authorization = %q", got)
				}
				if got := r.Header.Get("Idempotency-Key"); got != "send-contract-1" {
					t.Errorf("Idempotency-Key = %q", got)
				}
				w.Header().Set("X-Request-ID", "req-contract-error")
				if tc.status == http.StatusTooManyRequests {
					w.Header().Set("Retry-After", "1")
				}
				writeContractJSON(t, w, tc.status, map[string]any{
					"error": map[string]string{"code": tc.code, "message": "request not accepted"},
				})
			}))
			defer server.Close()

			dependencies := contractDependencies()
			dependencies.Sleep = func(context.Context, time.Duration) error {
				sleeps++
				return nil
			}
			code, stdout, stderr := executeForTest(t, sendContractArgs(server.URL), dependencies)
			if code != ExitAPI || stdout != "" || requests != 1 || sleeps != 0 {
				t.Fatalf("exit=%d stdout=%q requests=%d sleeps=%d stderr=%q", code, stdout, requests, sleeps, stderr)
			}
			diagnostic := decodeErrorOutput(t, stderr)
			if diagnostic["status"] != float64(tc.status) || diagnostic["code"] != tc.code || diagnostic["request_id"] != "req-contract-error" {
				t.Fatalf("diagnostic=%v", diagnostic)
			}
		})
	}
}

func TestSendPublishedPartialAcceptancePreservesRecipientResults(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/send" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		var request struct {
			To []string `json:"to"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode send request: %v", err)
		} else if len(request.To) != 2 || request.To[0] != "recipient@example.test" || request.To[1] != "rejected@example.test" {
			t.Errorf("recipients = %v", request.To)
		}
		writeContractJSON(t, w, http.StatusAccepted, map[string]any{
			"accepted": []map[string]string{{"message_id": "00000000-0000-0000-0000-000000000001", "to": "recipient@example.test"}},
			"rejected": []map[string]string{{"to": "rejected@example.test", "reason": "suppressed"}},
		})
	}))
	defer server.Close()

	args := sendContractArgs(server.URL)
	args = append(args, "--to", "rejected@example.test")
	code, stdout, stderr := executeForTest(t, args, contractDependencies())
	if code != ExitOK || requests != 1 {
		t.Fatalf("exit=%d requests=%d stderr=%q", code, requests, stderr)
	}
	var result struct {
		Accepted []acceptedOutput `json:"accepted"`
		Rejected []rejectedOutput `json:"rejected"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode result: %v; stdout=%q", err, stdout)
	}
	if len(result.Accepted) != 1 || result.Accepted[0].MessageID != "00000000-0000-0000-0000-000000000001" || result.Accepted[0].To != "recipient@example.test" || len(result.Rejected) != 1 || result.Rejected[0].To != "rejected@example.test" || result.Rejected[0].Reason != "suppressed" {
		t.Fatalf("partial result: %#v", result)
	}
}

type sequencedSendBackend struct {
	fakeBackend
	errors []error
	keys   []string
}

func (b *sequencedSendBackend) Send(_ context.Context, _ viapost.SendRequest, key string) (*viapost.SendResult, error) {
	b.keys = append(b.keys, key)
	err := b.errors[0]
	b.errors = b.errors[1:]
	return nil, err
}

func TestTimeoutThenManualRetryConflictRemainsUnresolved(t *testing.T) {
	backend := &sequencedSendBackend{errors: []error{
		context.DeadlineExceeded,
		&viapost.APIError{StatusCode: http.StatusConflict, Code: "conflict", Message: "idempotency key conflict"},
	}}
	dependencies := testDependencies(backend, nil)
	var sleeps int
	dependencies.Sleep = func(context.Context, time.Duration) error {
		sleeps++
		return nil
	}
	args := []string{
		"send", "--from", "sender@example.test", "--to", "recipient@example.test",
		"--subject", "contract", "--text", "body", "--idempotency-key", "send-contract-1",
	}

	code, stdout, stderr := executeForTest(t, args, dependencies)
	if code != ExitRuntime || stdout != "" || !strings.Contains(stderr, context.DeadlineExceeded.Error()) {
		t.Fatalf("unknown outcome: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = executeForTest(t, args, dependencies)
	if code != ExitAPI || stdout != "" || decodeErrorOutput(t, stderr)["status"] != float64(http.StatusConflict) {
		t.Fatalf("manual retry conflict: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if sleeps != 0 || len(backend.keys) != 2 || backend.keys[0] != "send-contract-1" || backend.keys[1] != "send-contract-1" {
		t.Fatalf("sleeps=%d keys=%v", sleeps, backend.keys)
	}
}

func TestSendHTTPConnectionDropAfterRequestNeverClaimsAcceptance(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/send" || r.Header.Get("Idempotency-Key") != "send-contract-1" {
			t.Errorf("request = %s %s, idempotency key = %q", r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"))
		}
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Errorf("read request body: %v", err)
		}
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack connection: %v", err)
			return
		}
		_ = connection.Close()
	}))
	defer server.Close()

	code, stdout, stderr := executeForTest(t, sendContractArgs(server.URL), contractDependencies())
	if code != ExitRuntime || stdout != "" || requests.Load() != 1 {
		t.Fatalf("exit=%d stdout=%q requests=%d stderr=%q", code, stdout, requests.Load(), stderr)
	}
	if strings.Contains(stderr, `"accepted"`) || strings.Contains(stderr, `"message_id"`) {
		t.Fatalf("unknown transport outcome falsely claimed acceptance: %q", stderr)
	}
}

func TestMessageNotFoundProducesNoMessageData(t *testing.T) {
	const messageID = "00000000-0000-0000-0000-000000000003"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/messages/"+messageID {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		writeContractJSON(t, w, http.StatusNotFound, map[string]any{
			"error": map[string]string{"code": "not_found", "message": "message not found"},
		})
	}))
	defer server.Close()

	args := []string{"--allow-custom-base-url", "--base-url", server.URL, "messages", "get", messageID}
	code, stdout, stderr := executeForTest(t, args, contractDependencies())
	if code != ExitAPI || stdout != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if diagnostic := decodeErrorOutput(t, stderr); diagnostic["status"] != float64(http.StatusNotFound) || diagnostic["code"] != "not_found" {
		t.Fatalf("diagnostic=%v", diagnostic)
	}
}
