package imwrap

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/roberChen/crush-sdk/config"
)

// ModelInfo describes one selectable model.
type ModelInfo struct {
	// Provider is the provider key/id used in config.
	Provider string
	// ProviderName is the provider's display name.
	ProviderName string
	// ID is the model id as used by the provider API.
	ID string
	// Name is the human-readable model name.
	Name string
	// ContextWindow is the model's context window in tokens.
	ContextWindow int64
	// Current marks the workspace's currently selected model.
	Current bool
}

// sortModelsByID returns the models sorted by id.
func sortModelsByID(models []catwalk.Model) []catwalk.Model {
	out := append([]catwalk.Model(nil), models...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Spec returns the canonical "provider/model" selector.
func (m ModelInfo) Spec() string {
	return m.Provider + "/" + m.ID
}

// ListModels lists the models available to the chat's workspace
// (providers and their models from the effective config), marking
// the currently selected one.
func (w *Wrapper) ListModels(ctx context.Context, chatID string) ([]ModelInfo, error) {
	wsID := w.chatWorkspace(chatID)
	return w.listModels(ctx, wsID)
}

func (w *Wrapper) listModels(ctx context.Context, wsID string) ([]ModelInfo, error) {
	cfg, err := w.client.GetConfig(ctx, wsID)
	if err != nil {
		return nil, fmt.Errorf("failed to get config: %w", err)
	}
	current := config.SelectedModel{}
	if cfg.Models != nil {
		current = cfg.Models[config.SelectedModelTypeLarge]
	}

	var out []ModelInfo
	if cfg.Providers != nil {
		for providerID, pc := range cfg.Providers.Copy() {
			if pc.Disable {
				continue
			}
			for _, m := range sortModelsByID(pc.Models) {
				out = append(out, ModelInfo{
					Provider:      providerID,
					ProviderName:  pc.Name,
					ID:            m.ID,
					Name:          m.Name,
					ContextWindow: m.ContextWindow,
					Current:       providerID == current.Provider && m.ID == current.Model,
				})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// currentModel returns the workspace's currently selected large
// model. The zero value means "unset, server default applies".
func (w *Wrapper) currentModel(ctx context.Context, wsID string) (config.SelectedModel, error) {
	cfg, err := w.client.GetConfig(ctx, wsID)
	if err != nil {
		return config.SelectedModel{}, fmt.Errorf("failed to get config: %w", err)
	}
	if cfg.Models == nil {
		return config.SelectedModel{}, nil
	}
	return cfg.Models[config.SelectedModelTypeLarge], nil
}

// SetModel changes the chat's workspace default model. The spec is
// "provider/model" or a bare model id (resolved to the unique
// provider offering it).
func (w *Wrapper) SetModel(ctx context.Context, chatID, spec string) (ModelInfo, error) {
	wsID := w.chatWorkspace(chatID)
	sel, err := w.resolveModelSpec(ctx, wsID, spec)
	if err != nil {
		return ModelInfo{}, err
	}
	if err := w.client.UpdatePreferredModel(ctx, wsID, config.ScopeWorkspace, config.SelectedModelTypeLarge, sel); err != nil {
		return ModelInfo{}, fmt.Errorf("failed to update preferred model: %w", err)
	}
	return ModelInfo{
		Provider: sel.Provider,
		ID:       sel.Model,
		Name:     sel.Model,
		Current:  true,
	}, nil
}

// resolveModelSpec resolves a user-supplied model selector to a
// SelectedModel against the workspace's configured providers.
func (w *Wrapper) resolveModelSpec(ctx context.Context, wsID, spec string) (config.SelectedModel, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return config.SelectedModel{}, fmt.Errorf("model spec is empty")
	}

	provider, model, hasSlash := strings.Cut(spec, "/")
	if !hasSlash {
		model = provider
		provider = ""
	}

	models, err := w.listModels(ctx, wsID)
	if err != nil {
		return config.SelectedModel{}, err
	}
	lower := strings.ToLower(model)
	var matches []ModelInfo
	for _, m := range models {
		if provider != "" && m.Provider != provider {
			continue
		}
		if strings.ToLower(m.ID) == lower || strings.EqualFold(m.Name, model) {
			matches = append(matches, m)
		}
	}
	switch len(matches) {
	case 1:
		return config.SelectedModel{Provider: matches[0].Provider, Model: matches[0].ID}, nil
	case 0:
		if provider != "" {
			return config.SelectedModel{}, fmt.Errorf("provider %q has no model %q", provider, model)
		}
		return config.SelectedModel{}, fmt.Errorf("no model matches %q (use provider/model)", model)
	default:
		var specs []string
		for _, m := range matches {
			specs = append(specs, m.Spec())
		}
		return config.SelectedModel{}, fmt.Errorf("%q matches multiple models: %s", model, strings.Join(specs, ", "))
	}
}
