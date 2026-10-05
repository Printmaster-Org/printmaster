package updatepolicy

import "context"

type explicitChannelKey struct{}

// WithExplicitChannel marks an operation-scoped selection. Server defaults must
// not replace a channel the operator explicitly selected for a manual install.
func WithExplicitChannel(ctx context.Context) context.Context {
	return context.WithValue(ctx, explicitChannelKey{}, true)
}

func HasExplicitChannel(ctx context.Context) bool {
	explicit, _ := ctx.Value(explicitChannelKey{}).(bool)
	return explicit
}
