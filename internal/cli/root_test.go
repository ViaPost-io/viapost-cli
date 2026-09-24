package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	viapost "github.com/ViaPost-io/viapost-go"
)

type fakeBackend struct {
	sendRequest      viapost.SendRequest
	idempotencyKey   string
	messageOptions   viapost.MessageListOptions
	requestedMessage string
	sendResult       *viapost.SendResult
	messages         []viapost.Message
	message          *viapost.Message
	usage            *viapost.MonthlyUsage
	err              error
	sendCalls        int
	listCalls        int
	listErrors       []error
}

type countingReadCloser struct {
	remaining int64
	read      int64
}

func (r *countingReadCloser) Read(buffer []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := int64(len(buffer))
	if n > r.remaining {
		n = r.remaining
	}
	r.remaining -= n
	r.read += n
	return int(n), nil
}

func (*countingReadCloser) Close() error { return nil }

func (f *fakeBackend) Send(_ context.Context, request viapost.SendRequest, idempotencyKey string) (*viapost.SendResult, error) {
	f.sendCalls++
	f.sendRequest = request
	f.idempotencyKey = idempotencyKey
	return f.sendResult, f.err
}

func (f *fakeBackend) ListMessages(_ context.Context, options viapost.MessageListOptions) ([]viapost.Message, error) {
	f.listCalls++
	f.messageOptions = options
	if len(f.listErrors) > 0 {
		err := f.listErrors[0]
		f.listErrors = f.listErrors[1:]
		if err != nil {
			return nil, err
		}
	}
	return f.messages, f.err
}

func (f *fakeBackend) GetMessage(_ context.Context, id string) (*viapost.Message, error) {
	f.requestedMessage = id
	return f.message, f.err
}

func (f *fakeBackend) GetUsage(context.Context) (*viapost.MonthlyUsage, error) {
	return f.usage, f.err
}

func testDependencies(backend Backend, observed *ClientConfig) Dependencies {
	return Dependencies{
		Version: "0.1.0-test",
		Getenv: func(key string) string {
			if key == "VIAPOST_API_KEY" {
				return "vp_test_secret"
			}
			return ""
		},
		NewBackend: func(config ClientConfig) (Backend, error) {
			if observed != nil {
				*observed = config
			}
			return backend, nil
		},
		Sleep: func(context.Context, time.Duration) error { return nil },
	}
}

func executeForTest(t *testing.T, args []string, dependencies Dependencies) (int, string, string) {
	t.Helper()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Execute(context.Background(), args, &stdout, &stderr, dependencies)
	return code, stdout.String(), stderr.String()
}

func TestVersionDoesNotRequireCredentials(t *testing.T) {
	dependencies := testDependencies(nil, nil)
	dependencies.Getenv = func(string) string { return "" }
	dependencies.NewBackend = func(ClientConfig) (Backend, error) {
		t.Fatal("version must not create an API client")
		return nil, nil
	}

	code, stdout, stderr := executeForTest(t, []string{"version"}, dependencies)

	if code != ExitOK || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	var output map[string]string
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatal(err)
	}
	if output["version"] != "0.1.0-test" {
		t.Fatalf("unexpected output: %s", stdout)
	}
}

func TestAuthenticatedCommandRequiresAPIKey(t *testing.T) {
	dependencies := testDependencies(&fakeBackend{}, nil)
	dependencies.Getenv = func(string) string { return "" }

	code, _, stderr := executeForTest(t, []string{"usage"}, dependencies)

	if code != ExitConfig || !strings.Contains(stderr, "VIAPOST_API_KEY") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestSendForwardsPayloadAndIdempotencyKey(t *testing.T) {
	backend := &fakeBackend{sendResult: &viapost.SendResult{
		Accepted: []viapost.AcceptedMessage{{MessageID: "msg-1", To: "person@example.com"}},
		Rejected: []viapost.RejectedMessage{},
	}}
	observed := ClientConfig{}

	code, stdout, stderr := executeForTest(t, []string{
		"--allow-custom-base-url",
		"--base-url", "https://api.example.test",
		"--timeout", "12s",
		"send",
		"--from", "hello@example.com",
		"--to", "person@example.com",
		"--subject", "Hello",
		"--text", "Hello there",
		"--stream", "transactional",
		"--idempotency-key", "order-123",
	}, testDependencies(backend, &observed))

	if code != ExitOK || !strings.Contains(stderr, "https://api.example.test") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if observed.APIKey != "vp_test_secret" || observed.BaseURL != "https://api.example.test" || observed.Timeout != 12*time.Second {
		t.Fatalf("unexpected config: %#v", observed)
	}
	if backend.sendRequest.From != "hello@example.com" || len(backend.sendRequest.To) != 1 || backend.idempotencyKey != "order-123" {
		t.Fatalf("unexpected send: %#v / %q", backend.sendRequest, backend.idempotencyKey)
	}
	if !strings.Contains(stdout, `"message_id":"msg-1"`) {
		t.Fatalf("unexpected output: %s", stdout)
	}
}

func TestAuthenticatedCommandRejectsCustomBaseURLWithoutExplicitOptIn(t *testing.T) {
	var requests int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if authorization := r.Header.Get("Authorization"); authorization != "" {
			t.Errorf("Authorization = %q, want empty", authorization)
		}
	}))
	defer server.Close()

	dependencies := testDependencies(&fakeBackend{}, nil)
	dependencies.Getenv = func(key string) string {
		switch key {
		case "VIAPOST_API_KEY":
			return "vp_test_secret"
		case "VIAPOST_BASE_URL":
			return server.URL
		default:
			return ""
		}
	}
	dependencies.NewBackend = func(ClientConfig) (Backend, error) {
		t.Fatal("untrusted base URL must be rejected before client creation")
		return nil, nil
	}

	code, _, stderr := executeForTest(t, []string{"usage"}, dependencies)

	if code != ExitConfig || !strings.Contains(stderr, "--allow-custom-base-url") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if requests != 0 {
		t.Fatalf("untrusted server requests = %d, want 0", requests)
	}
}

func TestAuthenticatedCommandAllowsCustomBaseURLWithExplicitOptIn(t *testing.T) {
	observed := ClientConfig{}
	dependencies := testDependencies(&fakeBackend{}, &observed)
	dependencies.Getenv = func(key string) string {
		switch key {
		case "VIAPOST_API_KEY":
			return "vp_test_secret"
		case "VIAPOST_BASE_URL":
			return "https://api.example.test"
		default:
			return ""
		}
	}

	code, _, stderr := executeForTest(t, []string{
		"--allow-custom-base-url", "usage",
	}, dependencies)

	if code != ExitOK || !strings.Contains(stderr, "https://api.example.test") || observed.BaseURL != "https://api.example.test" {
		t.Fatalf("code=%d stderr=%q config=%#v", code, stderr, observed)
	}
}

func TestCustomBaseURLAcknowledgementIsSanitized(t *testing.T) {
	observed := ClientConfig{}
	dependencies := testDependencies(&fakeBackend{}, &observed)
	dependencies.Getenv = func(key string) string {
		if key == "VIAPOST_API_KEY" {
			return "vp_test_secret"
		}
		return ""
	}

	code, stdout, stderr := executeForTest(t, []string{
		"--allow-custom-base-url", "--base-url", "https://api.example.test:8443/private/path?token=not-for-output#fragment", "usage",
	}, dependencies)

	if code != ExitOK || stdout == "" || !strings.Contains(stderr, "https://api.example.test:8443") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	for _, forbidden := range []string{"/private/path", "token=", "fragment", "vp_test_secret"} {
		if strings.Contains(stderr, forbidden) {
			t.Fatalf("stderr leaked %q: %q", forbidden, stderr)
		}
	}
}

func TestSanitizedDestinationExcludesSensitiveURLComponents(t *testing.T) {
	destination, err := sanitizedDestination("https://user:password@api.example.test:8443/private/path?token=secret#fragment")

	if err != nil || destination != "https://api.example.test:8443" {
		t.Fatalf("destination=%q err=%v", destination, err)
	}
}

func TestCustomBaseURLDoesNotFollowRedirectsWithBearerCredentials(t *testing.T) {
	var targetRequests int
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetRequests++
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer vp_test_secret" {
			t.Errorf("source Authorization = %q", got)
		}
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	code, _, stderr := executeForTest(t, []string{
		"--allow-custom-base-url", "--base-url", source.URL, "usage",
	}, Dependencies{Getenv: func(key string) string {
		if key == "VIAPOST_API_KEY" {
			return "vp_test_secret"
		}
		return ""
	}})

	if code != ExitAPI || stderr == "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if targetRequests != 0 {
		t.Fatalf("redirect target requests = %d, want 0", targetRequests)
	}
}

func TestSendLoadsJSONFromFileWithoutPuttingContentInArguments(t *testing.T) {
	backend := &fakeBackend{sendResult: &viapost.SendResult{}}
	dependencies := testDependencies(backend, nil)
	dependencies.OpenFile = func(path string) (io.ReadCloser, error) {
		if path != "request.json" {
			t.Fatalf("unexpected path: %s", path)
		}
		return io.NopCloser(strings.NewReader(`{"from":"hello@example.com","to":["person@example.com"],"subject":"Private subject","text":"Private body","stream":"transactional"}`)), nil
	}

	code, _, stderr := executeForTest(t, []string{"send", "--data", "@request.json"}, dependencies)

	if code != ExitOK || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if backend.sendRequest.Subject != "Private subject" || backend.sendRequest.Text != "Private body" {
		t.Fatalf("unexpected send request: %#v", backend.sendRequest)
	}
}

func TestSendLoadsTemplateOnlyJSONUsingOpenAPIFieldNames(t *testing.T) {
	backend := &fakeBackend{sendResult: &viapost.SendResult{}}
	dependencies := testDependencies(backend, nil)
	dependencies.OpenFile = func(string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(`{"from":"hello@example.com","from_name":"ViaPost","reply_to":"reply@example.com","to":["person@example.com"],"template_id":"11111111-1111-1111-1111-111111111111","variables":{"name":"Ada"}}`)), nil
	}

	code, _, stderr := executeForTest(t, []string{"send", "--data", "@template.json"}, dependencies)

	if code != ExitOK || stderr != "" || backend.sendRequest.TemplateID == nil {
		t.Fatalf("code=%d stderr=%q request=%#v", code, stderr, backend.sendRequest)
	}
	if backend.sendRequest.FromName != "ViaPost" || backend.sendRequest.ReplyTo != "reply@example.com" || backend.sendRequest.Variables["name"] != "Ada" {
		t.Fatalf("OpenAPI fields were not decoded: %#v", backend.sendRequest)
	}
}

func TestSendLoadsBodyFromStdin(t *testing.T) {
	backend := &fakeBackend{sendResult: &viapost.SendResult{}}
	dependencies := testDependencies(backend, nil)
	dependencies.Stdin = strings.NewReader("Private body from stdin")

	code, _, stderr := executeForTest(t, []string{
		"send", "--from", "hello@example.com", "--to", "person@example.com", "--subject", "Private", "--text-file", "-",
	}, dependencies)

	if code != ExitOK || stderr != "" || backend.sendRequest.Text != "Private body from stdin" {
		t.Fatalf("code=%d stderr=%q request=%#v", code, stderr, backend.sendRequest)
	}
}

func TestMessagesListForwardsFilters(t *testing.T) {
	backend := &fakeBackend{messages: []viapost.Message{{ID: "msg-1", Status: "delivered"}}}

	code, stdout, stderr := executeForTest(t, []string{
		"messages", "list", "--limit", "10", "--status", "delivered", "--search", "invoice", "--period", "7d",
	}, testDependencies(backend, nil))

	if code != ExitOK || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if backend.messageOptions.Limit != 10 || backend.messageOptions.Status != "delivered" || backend.messageOptions.Search != "invoice" || backend.messageOptions.Period != viapost.MessagePeriod7Days {
		t.Fatalf("unexpected options: %#v", backend.messageOptions)
	}
	if !strings.Contains(stdout, `"id":"msg-1"`) {
		t.Fatalf("unexpected output: %s", stdout)
	}
}

func TestMessagesListLoadsSearchFromFile(t *testing.T) {
	backend := &fakeBackend{}
	dependencies := testDependencies(backend, nil)
	dependencies.OpenFile = func(path string) (io.ReadCloser, error) {
		if path != "private-search.txt" {
			t.Fatalf("path = %q", path)
		}
		return io.NopCloser(strings.NewReader("private invoice\n")), nil
	}

	code, _, stderr := executeForTest(t, []string{"messages", "list", "--search-file", "private-search.txt"}, dependencies)

	if code != ExitOK || stderr != "" || backend.messageOptions.Search != "private invoice" {
		t.Fatalf("code=%d stderr=%q options=%#v", code, stderr, backend.messageOptions)
	}
}

func TestMessagesListLoadsSearchFromStdin(t *testing.T) {
	backend := &fakeBackend{}
	dependencies := testDependencies(backend, nil)
	dependencies.Stdin = strings.NewReader("private invoice")

	code, _, stderr := executeForTest(t, []string{"messages", "list", "--search-file", "-"}, dependencies)

	if code != ExitOK || stderr != "" || backend.messageOptions.Search != "private invoice" {
		t.Fatalf("code=%d stderr=%q options=%#v", code, stderr, backend.messageOptions)
	}
}

func TestMessagesListRejectsEmptySearchFile(t *testing.T) {
	backend := &fakeBackend{}
	dependencies := testDependencies(backend, nil)
	dependencies.Stdin = strings.NewReader(" \r\n")

	code, stdout, stderr := executeForTest(t, []string{"messages", "list", "--search-file", "-"}, dependencies)

	if code != ExitUsage || stdout != "" || !strings.Contains(stderr, "must not be empty") || backend.listCalls != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q calls=%d", code, stdout, stderr, backend.listCalls)
	}
}

func TestMessagesListRejectsOversizedSearchFile(t *testing.T) {
	backend := &fakeBackend{}
	dependencies := testDependencies(backend, nil)
	reader := &countingReadCloser{remaining: maxSearchInputBytes + 2}
	dependencies.OpenFile = func(string) (io.ReadCloser, error) { return reader, nil }

	code, stdout, stderr := executeForTest(t, []string{"messages", "list", "--search-file", "large-search.txt"}, dependencies)

	if code != ExitRuntime || stdout != "" || !strings.Contains(stderr, "input exceeds") || backend.listCalls != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q calls=%d", code, stdout, stderr, backend.listCalls)
	}
	if reader.read != maxSearchInputBytes+1 || reader.remaining != 1 {
		t.Fatalf("read=%d remaining=%d, want bounded read", reader.read, reader.remaining)
	}
}

func TestMessagesListRejectsInlineAndFileSearchTogether(t *testing.T) {
	code, _, stderr := executeForTest(t, []string{"messages", "list", "--search", "inline", "--search-file", "-"}, testDependencies(&fakeBackend{}, nil))

	if code != ExitUsage || !strings.Contains(stderr, "cannot be combined") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestSendInputRejectsFileOverLimitBeforeReadingItAll(t *testing.T) {
	dependencies := testDependencies(&fakeBackend{sendResult: &viapost.SendResult{}}, nil)
	reader := &countingReadCloser{remaining: maxSendInputBytes + 2}
	dependencies.OpenFile = func(string) (io.ReadCloser, error) {
		return reader, nil
	}

	code, _, stderr := executeForTest(t, []string{"send", "--data", "@too-large.json"}, dependencies)

	if code != ExitRuntime || !strings.Contains(stderr, "input exceeds") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if reader.read != maxSendInputBytes+1 || reader.remaining != 1 {
		t.Fatalf("read=%d remaining=%d, want bounded read", reader.read, reader.remaining)
	}
}

func TestMessagesGetAndUsage(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	backend := &fakeBackend{
		message: &viapost.Message{ID: "msg-2", Status: "queued"},
		usage:   &viapost.MonthlyUsage{Period: viapost.UsagePeriod{Start: now}, Used: 42},
	}
	dependencies := testDependencies(backend, nil)

	code, stdout, stderr := executeForTest(t, []string{"messages", "get", "msg-2"}, dependencies)
	if code != ExitOK || stderr != "" || backend.requestedMessage != "msg-2" || !strings.Contains(stdout, `"status":"queued"`) {
		t.Fatalf("message code=%d stdout=%q stderr=%q id=%q", code, stdout, stderr, backend.requestedMessage)
	}

	code, stdout, stderr = executeForTest(t, []string{"usage"}, dependencies)
	if code != ExitOK || stderr != "" || !strings.Contains(stdout, `"used":42`) {
		t.Fatalf("usage code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestErrorsHaveStableExitCodeAndRedactAPIKey(t *testing.T) {
	secret := "vp_live_never_print_me"
	backend := &fakeBackend{err: errors.New("upstream failed with " + secret)}
	dependencies := testDependencies(backend, nil)
	dependencies.Getenv = func(key string) string {
		if key == "VIAPOST_API_KEY" {
			return secret
		}
		return ""
	}

	code, _, stderr := executeForTest(t, []string{"usage"}, dependencies)

	if code != ExitRuntime || strings.Contains(stderr, secret) || !strings.Contains(stderr, "[REDACTED]") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestInvalidTimeoutIsUsageError(t *testing.T) {
	code, _, stderr := executeForTest(t, []string{"--timeout", "0s", "usage"}, testDependencies(&fakeBackend{}, nil))
	if code != ExitUsage || !strings.Contains(stderr, "timeout") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestCompletionDoesNotRequireCredentials(t *testing.T) {
	dependencies := testDependencies(nil, nil)
	dependencies.Getenv = func(string) string { return "" }
	dependencies.NewBackend = func(ClientConfig) (Backend, error) {
		t.Fatal("completion must not create an API client")
		return nil, nil
	}

	code, stdout, stderr := executeForTest(t, []string{"completion", "bash"}, dependencies)
	if code != ExitOK || stderr != "" || !strings.Contains(stdout, "bash completion") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestSafeReadsRetryTransientAPIErrors(t *testing.T) {
	backend := &fakeBackend{
		messages: []viapost.Message{{ID: "msg-after-retry"}},
		listErrors: []error{
			&viapost.APIError{StatusCode: 503, Code: "unavailable"},
			&viapost.APIError{StatusCode: 429, Code: "rate_limited"},
			nil,
		},
	}
	var delays []time.Duration
	dependencies := testDependencies(backend, nil)
	dependencies.Sleep = func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	}

	code, stdout, stderr := executeForTest(t, []string{"messages", "list"}, dependencies)

	if code != ExitOK || stderr != "" || !strings.Contains(stdout, "msg-after-retry") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if backend.listCalls != 3 || len(delays) != 2 || delays[0] <= 0 || delays[1] <= delays[0] {
		t.Fatalf("calls=%d delays=%v", backend.listCalls, delays)
	}
}

func TestMutatingSendIsNeverRetried(t *testing.T) {
	backend := &fakeBackend{err: &viapost.APIError{StatusCode: 503, Code: "unavailable"}}

	code, _, _ := executeForTest(t, []string{
		"send", "--from", "from@example.com", "--to", "to@example.com", "--subject", "hello", "--text", "body",
	}, testDependencies(backend, nil))

	if code != ExitAPI || backend.sendCalls != 1 {
		t.Fatalf("code=%d send calls=%d", code, backend.sendCalls)
	}
}

func TestAPIErrorOutputIncludesStableDiagnosticFields(t *testing.T) {
	backend := &fakeBackend{err: &viapost.APIError{
		StatusCode: 429,
		Code:       "rate_limited",
		Message:    "try later",
		RequestID:  "req-123",
	}}

	code, _, stderr := executeForTest(t, []string{"usage"}, testDependencies(backend, nil))

	if code != ExitAPI || !strings.Contains(stderr, `"status":429`) || !strings.Contains(stderr, `"code":"rate_limited"`) || !strings.Contains(stderr, `"request_id":"req-123"`) {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestAPIErrorRedactsKeyFromEveryDiagnosticField(t *testing.T) {
	backend := &fakeBackend{err: &viapost.APIError{
		StatusCode: 400,
		Code:       "bad-vp_test_secret",
		Message:    "bad vp_test_secret",
		RequestID:  "req-vp_test_secret",
	}}

	code, _, stderr := executeForTest(t, []string{"usage"}, testDependencies(backend, nil))

	if code != ExitAPI || strings.Contains(stderr, "vp_test_secret") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestInvalidTimeoutEnvironmentIsAConfigError(t *testing.T) {
	dependencies := testDependencies(&fakeBackend{}, nil)
	dependencies.Getenv = func(key string) string {
		switch key {
		case "VIAPOST_API_KEY":
			return "vp_test_secret"
		case "VIAPOST_TIMEOUT":
			return "not-a-duration"
		default:
			return ""
		}
	}

	code, _, stderr := executeForTest(t, []string{"usage"}, dependencies)

	if code != ExitConfig || !strings.Contains(stderr, "VIAPOST_TIMEOUT") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}
