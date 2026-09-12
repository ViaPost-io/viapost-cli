package cli

import (
	"context"

	viapost "github.com/ViaPost-io/viapost-go"
)

type sdkBackend struct{ client *viapost.Client }

func newSDKBackend(config ClientConfig) (Backend, error) {
	client, err := viapost.NewClient(
		config.APIKey,
		viapost.WithBaseURL(config.BaseURL),
		viapost.WithTimeout(config.Timeout),
		viapost.WithUserAgent("viapost-cli/"+Version),
	)
	if err != nil {
		return nil, err
	}
	return &sdkBackend{client: client}, nil
}

func (b *sdkBackend) Send(ctx context.Context, request viapost.SendRequest, idempotencyKey string) (*viapost.SendResult, error) {
	if idempotencyKey == "" {
		return b.client.Email.Send(ctx, request)
	}
	return b.client.Email.Send(ctx, request, viapost.WithIdempotencyKey(idempotencyKey))
}

func (b *sdkBackend) ListMessages(ctx context.Context, options viapost.MessageListOptions) ([]viapost.Message, error) {
	return b.client.Messages.List(ctx, options)
}

func (b *sdkBackend) GetMessage(ctx context.Context, id string) (*viapost.Message, error) {
	return b.client.Messages.Get(ctx, id)
}

func (b *sdkBackend) GetUsage(ctx context.Context) (*viapost.MonthlyUsage, error) {
	return b.client.Usage.Get(ctx)
}
