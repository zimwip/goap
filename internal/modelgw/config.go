package modelgw

import (
	"context"

	"github.com/zimwip/goap/internal/platform"
)

// InitialConfig is the configuration the graph is seeded with when it holds no provider: the file
// (GOAP_MODELS_CONFIG) or, without one, the default (Anthropic when a key is available, the fake provider otherwise).
func InitialConfig(ctx context.Context, path string, secrets *platform.Secrets) (Config, error) {
	if path != "" {
		return LoadConfig(path)
	}
	key, _ := secrets.Get(ctx, "goap/modelgw#anthropic_api_key", "ANTHROPIC_API_KEY")
	return DefaultConfig(key != ""), nil
}
