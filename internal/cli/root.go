package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	viapost "github.com/ViaPost-io/viapost-go"
	"github.com/spf13/cobra"
)

const (
	ExitOK      = 0
	ExitUsage   = 2
	ExitConfig  = 3
	ExitAPI     = 4
	ExitRuntime = 5
)

// Version is replaced from the release tag at build time.
var Version = "0.1.0"

type ClientConfig struct {
	APIKey  string
	BaseURL string
	Timeout time.Duration
}

type Backend interface {
	Send(context.Context, viapost.SendRequest, string) (*viapost.SendResult, error)
	ListMessages(context.Context, viapost.MessageListOptions) ([]viapost.Message, error)
	GetMessage(context.Context, string) (*viapost.Message, error)
	GetUsage(context.Context) (*viapost.MonthlyUsage, error)
}

type BackendFactory func(ClientConfig) (Backend, error)

type Dependencies struct {
	Version    string
	Getenv     func(string) string
	NewBackend BackendFactory
	Sleep      func(context.Context, time.Duration) error
	ReadFile   func(string) ([]byte, error)
	Stdin      io.Reader
}

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

type commandState struct {
	dependencies Dependencies
	stdout       io.Writer
	baseURL      string
	timeout      time.Duration
	pretty       bool
	envError     error
}

type sendOutput struct {
	Accepted []acceptedOutput `json:"accepted"`
	Rejected []rejectedOutput `json:"rejected"`
}

type acceptedOutput struct {
	MessageID string `json:"message_id"`
	To        string `json:"to"`
}

type rejectedOutput struct {
	To     string `json:"to"`
	Reason string `json:"reason"`
}

type sendJSON struct {
	From        string               `json:"from"`
	FromName    string               `json:"from_name"`
	ReplyTo     string               `json:"reply_to"`
	To          []string             `json:"to"`
	CC          []string             `json:"cc"`
	BCC         []string             `json:"bcc"`
	Subject     string               `json:"subject"`
	HTML        string               `json:"html"`
	Text        string               `json:"text"`
	Stream      viapost.Stream       `json:"stream"`
	Tags        []string             `json:"tags"`
	Metadata    map[string]any       `json:"metadata"`
	TemplateID  *string              `json:"template_id"`
	Variables   map[string]any       `json:"variables"`
	Attachments []viapost.Attachment `json:"attachments"`
}

func Execute(ctx context.Context, args []string, stdout, stderr io.Writer, dependencies Dependencies) int {
	dependencies = withDefaults(dependencies)
	root := newRootCommand(dependencies, stdout)
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(io.Discard)
	root.SetContext(ctx)

	err := root.Execute()
	if err == nil {
		return ExitOK
	}
	code := ExitUsage
	var typed *exitError
	if errors.As(err, &typed) {
		code = typed.code
	}
	message := redact(err.Error(), dependencies.Getenv("VIAPOST_API_KEY"))
	payload := map[string]any{"error": map[string]any{"message": message}}
	var apiError *viapost.APIError
	if errors.As(err, &apiError) {
		payload["error"] = map[string]any{
			"message":    message,
			"status":     apiError.StatusCode,
			"code":       redact(apiError.Code, dependencies.Getenv("VIAPOST_API_KEY")),
			"request_id": redact(apiError.RequestID, dependencies.Getenv("VIAPOST_API_KEY")),
		}
	}
	_ = writeJSON(stderr, payload, false)
	return code
}

func withDefaults(dependencies Dependencies) Dependencies {
	if dependencies.Version == "" {
		dependencies.Version = Version
	}
	if dependencies.Getenv == nil {
		dependencies.Getenv = os.Getenv
	}
	if dependencies.NewBackend == nil {
		dependencies.NewBackend = newSDKBackend
	}
	if dependencies.Sleep == nil {
		dependencies.Sleep = sleepContext
	}
	if dependencies.ReadFile == nil {
		dependencies.ReadFile = os.ReadFile
	}
	if dependencies.Stdin == nil {
		dependencies.Stdin = os.Stdin
	}
	return dependencies
}

func newRootCommand(dependencies Dependencies, stdout io.Writer) *cobra.Command {
	baseURL := dependencies.Getenv("VIAPOST_BASE_URL")
	if baseURL == "" {
		baseURL = viapost.DefaultBaseURL
	}
	timeout := viapost.DefaultTimeout
	var envError error
	if raw := dependencies.Getenv("VIAPOST_TIMEOUT"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			timeout = parsed
		} else {
			envError = fmt.Errorf("VIAPOST_TIMEOUT must be a positive Go duration")
		}
	}
	state := &commandState{dependencies: dependencies, stdout: stdout, baseURL: baseURL, timeout: timeout, envError: envError}
	root := &cobra.Command{
		Use:           "viapost",
		Short:         "ViaPost command-line interface",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.PersistentFlags().StringVar(&state.baseURL, "base-url", state.baseURL, "ViaPost API base URL")
	root.PersistentFlags().DurationVar(&state.timeout, "timeout", state.timeout, "request timeout")
	root.PersistentFlags().BoolVar(&state.pretty, "pretty", false, "indent JSON output")
	root.AddCommand(newVersionCommand(state), newSendCommand(state), newMessagesCommand(state), newUsageCommand(state))
	return root
}

func newVersionCommand(state *commandState) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the CLI version",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return writeJSON(state.stdout, map[string]string{"version": state.dependencies.Version}, state.pretty)
		},
	}
}

func newSendCommand(state *commandState) *cobra.Command {
	var request viapost.SendRequest
	request.Stream = viapost.StreamTransactional
	var idempotencyKey string
	var dataSource, textSource, htmlSource string
	command := &cobra.Command{
		Use:   "send",
		Short: "Queue an email",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := loadSendInput(state.dependencies, command, &request, dataSource, textSource, htmlSource); err != nil {
				return err
			}
			if request.From == "" || len(request.To) == 0 {
				return usageError("send requires --from and at least one --to")
			}
			if request.Stream != viapost.StreamTransactional && request.Stream != viapost.StreamMarketing {
				return usageError("--stream must be transactional or marketing")
			}
			backend, err := state.backend()
			if err != nil {
				return err
			}
			result, err := backend.Send(command.Context(), request, idempotencyKey)
			if err != nil {
				return classifyBackendError(err)
			}
			output := sendOutput{
				Accepted: make([]acceptedOutput, 0, len(result.Accepted)),
				Rejected: make([]rejectedOutput, 0, len(result.Rejected)),
			}
			for _, accepted := range result.Accepted {
				output.Accepted = append(output.Accepted, acceptedOutput{MessageID: accepted.MessageID, To: accepted.To})
			}
			for _, rejected := range result.Rejected {
				output.Rejected = append(output.Rejected, rejectedOutput{To: rejected.To, Reason: rejected.Reason})
			}
			return writeJSON(state.stdout, output, state.pretty)
		},
	}
	flags := command.Flags()
	flags.StringVar(&request.From, "from", "", "sender email address")
	flags.StringVar(&request.FromName, "from-name", "", "sender display name")
	flags.StringVar(&request.ReplyTo, "reply-to", "", "reply-to email address")
	flags.StringSliceVar(&request.To, "to", nil, "recipient email address (repeatable)")
	flags.StringSliceVar(&request.CC, "cc", nil, "CC recipient (repeatable)")
	flags.StringSliceVar(&request.BCC, "bcc", nil, "BCC recipient (repeatable)")
	flags.StringVar(&request.Subject, "subject", "", "email subject")
	flags.StringVar(&request.Text, "text", "", "plain text body")
	flags.StringVar(&request.HTML, "html", "", "HTML body")
	flags.StringVar(&dataSource, "data", "", "JSON request source: @file or - for stdin")
	flags.StringVar(&textSource, "text-file", "", "plain text body source: file or - for stdin")
	flags.StringVar(&htmlSource, "html-file", "", "HTML body source: file or - for stdin")
	flags.StringSliceVar(&request.Tags, "tag", nil, "message tag (repeatable)")
	flags.Var((*streamValue)(&request.Stream), "stream", "transactional or marketing")
	flags.StringVar(&idempotencyKey, "idempotency-key", "", "stable key for one logical send")
	return command
}

const maxSendInputBytes = 8 * 1024 * 1024

func loadSendInput(dependencies Dependencies, command *cobra.Command, request *viapost.SendRequest, dataSource, textSource, htmlSource string) error {
	if dataSource != "" {
		for _, name := range []string{"from", "from-name", "reply-to", "to", "cc", "bcc", "subject", "text", "html", "text-file", "html-file", "tag", "stream"} {
			if command.Flags().Changed(name) {
				return usageError("--data cannot be combined with individual message fields")
			}
		}
		if dataSource != "-" && !strings.HasPrefix(dataSource, "@") {
			return usageError("--data requires @file or - for stdin")
		}
		contents, err := readSendInput(dependencies, strings.TrimPrefix(dataSource, "@"))
		if err != nil {
			return runtimeError(fmt.Sprintf("unable to read --data source: %v", err))
		}
		if err := decodeSendJSON(contents, request); err != nil {
			return usageError("--data must contain a valid ViaPost send JSON object")
		}
		return nil
	}

	if textSource != "" {
		contents, err := readSendInput(dependencies, textSource)
		if err != nil {
			return runtimeError(fmt.Sprintf("unable to read --text-file: %v", err))
		}
		request.Text = string(contents)
	}
	if htmlSource != "" {
		if textSource == "-" && htmlSource == "-" {
			return usageError("stdin can supply only one message body")
		}
		contents, err := readSendInput(dependencies, htmlSource)
		if err != nil {
			return runtimeError(fmt.Sprintf("unable to read --html-file: %v", err))
		}
		request.HTML = string(contents)
	}
	return nil
}

func decodeSendJSON(contents []byte, request *viapost.SendRequest) error {
	input := sendJSON{Stream: viapost.StreamTransactional}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("JSON source must contain exactly one object")
	}
	*request = viapost.SendRequest{
		From: input.From, FromName: input.FromName, ReplyTo: input.ReplyTo,
		To: input.To, CC: input.CC, BCC: input.BCC, Subject: input.Subject,
		HTML: input.HTML, Text: input.Text, Stream: input.Stream, Tags: input.Tags,
		Metadata: input.Metadata, TemplateID: input.TemplateID, Variables: input.Variables,
		Attachments: input.Attachments,
	}
	return nil
}

func readSendInput(dependencies Dependencies, source string) ([]byte, error) {
	var contents []byte
	var err error
	if source == "-" {
		contents, err = io.ReadAll(io.LimitReader(dependencies.Stdin, maxSendInputBytes+1))
	} else {
		contents, err = dependencies.ReadFile(source)
	}
	if err != nil {
		return nil, err
	}
	if len(contents) > maxSendInputBytes {
		return nil, fmt.Errorf("input exceeds %d bytes", maxSendInputBytes)
	}
	return contents, nil
}

type streamValue viapost.Stream

func (s *streamValue) String() string { return string(*s) }
func (s *streamValue) Set(value string) error {
	*s = streamValue(value)
	return nil
}
func (*streamValue) Type() string { return "stream" }

func newMessagesCommand(state *commandState) *cobra.Command {
	messages := &cobra.Command{Use: "messages", Short: "Inspect outbound messages"}
	messages.AddCommand(newMessagesListCommand(state), newMessagesGetCommand(state))
	return messages
}

func newMessagesListCommand(state *commandState) *cobra.Command {
	var limit int
	var status, search, period, apiKeyID, cursor string
	command := &cobra.Command{
		Use:   "list",
		Short: "List outbound messages",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if limit < 0 || limit > 100 {
				return usageError("--limit must be between 0 and 100")
			}
			if period != "" && period != "24h" && period != "7d" && period != "14d" && period != "30d" {
				return usageError("--period must be 24h, 7d, 14d, or 30d")
			}
			options := viapost.MessageListOptions{Limit: limit, Status: status, Search: search, Period: viapost.MessagePeriod(period), APIKeyID: apiKeyID}
			if cursor != "" {
				parsed, err := time.Parse(time.RFC3339, cursor)
				if err != nil {
					return usageError("--cursor must be RFC3339")
				}
				options.Cursor = &parsed
			}
			backend, err := state.backend()
			if err != nil {
				return err
			}
			result, err := backend.ListMessages(command.Context(), options)
			if err != nil {
				return classifyBackendError(err)
			}
			return writeJSON(state.stdout, result, state.pretty)
		},
	}
	flags := command.Flags()
	flags.IntVar(&limit, "limit", 0, "maximum number of messages")
	flags.StringVar(&status, "status", "", "filter by delivery status")
	flags.StringVar(&search, "search", "", "search messages")
	flags.StringVar(&period, "period", "", "relative window: 24h, 7d, 14d, or 30d")
	flags.StringVar(&apiKeyID, "api-key-id", "", "filter by API key ID")
	flags.StringVar(&cursor, "cursor", "", "pagination cursor in RFC3339 format")
	return command
}

func newMessagesGetCommand(state *commandState) *cobra.Command {
	return &cobra.Command{
		Use:   "get MESSAGE_ID",
		Short: "Get one outbound message",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			backend, err := state.backend()
			if err != nil {
				return err
			}
			result, err := backend.GetMessage(command.Context(), args[0])
			if err != nil {
				return classifyBackendError(err)
			}
			return writeJSON(state.stdout, result, state.pretty)
		},
	}
}

func newUsageCommand(state *commandState) *cobra.Command {
	return &cobra.Command{
		Use:   "usage",
		Short: "Show monthly usage and quota",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			backend, err := state.backend()
			if err != nil {
				return err
			}
			result, err := backend.GetUsage(command.Context())
			if err != nil {
				return classifyBackendError(err)
			}
			return writeJSON(state.stdout, result, state.pretty)
		},
	}
}

func (s *commandState) backend() (Backend, error) {
	if s.envError != nil {
		return nil, &exitError{code: ExitConfig, err: s.envError}
	}
	if s.timeout <= 0 {
		return nil, usageError("timeout must be greater than zero")
	}
	apiKey := s.dependencies.Getenv("VIAPOST_API_KEY")
	if apiKey == "" || apiKey != strings.TrimSpace(apiKey) {
		return nil, &exitError{code: ExitConfig, err: errors.New("set VIAPOST_API_KEY to a server-side ViaPost API key")}
	}
	backend, err := s.dependencies.NewBackend(ClientConfig{APIKey: apiKey, BaseURL: s.baseURL, Timeout: s.timeout})
	if err != nil {
		return nil, &exitError{code: ExitConfig, err: err}
	}
	return &retryBackend{next: backend, sleep: s.dependencies.Sleep, attempts: 3}, nil
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func usageError(message string) error {
	return &exitError{code: ExitUsage, err: errors.New(message)}
}

func runtimeError(message string) error {
	return &exitError{code: ExitRuntime, err: errors.New(message)}
}

func classifyBackendError(err error) error {
	var apiError *viapost.APIError
	if errors.As(err, &apiError) {
		return &exitError{code: ExitAPI, err: err}
	}
	return &exitError{code: ExitRuntime, err: err}
}

func redact(message, secret string) string {
	if secret == "" {
		return message
	}
	return strings.ReplaceAll(message, secret, "[REDACTED]")
}

func writeJSON(writer io.Writer, value any, pretty bool) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	if pretty {
		encoder.SetIndent("", "  ")
	}
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("encode JSON output: %w", err)
	}
	return nil
}
