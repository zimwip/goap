package modelgw

import (
	"context"

	"github.com/zimwip/goap/internal/graphsvc"
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

// BootConfig is InitialConfig as the model section of the platform bootstrap (graphsvc.Boot).
func BootConfig(ctx context.Context, path string, secrets *platform.Secrets) (graphsvc.ModelsConfig, error) {
	cfg, err := InitialConfig(ctx, path, secrets)
	if err != nil {
		return graphsvc.ModelsConfig{}, err
	}
	provs, models, aliases, err := cfg.Objects()
	if err != nil {
		return graphsvc.ModelsConfig{}, err
	}
	return graphsvc.ModelsConfig{Providers: provs, Models: models, Aliases: aliases}, nil
}
