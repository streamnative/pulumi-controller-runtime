// Copyright (c) 2020 StreamNative, Inc.. All Rights Reserved.

package plugin

import (
	"github.com/blang/semver"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/plugin"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
)

// ProviderLoader loads resource providers.
type ProviderLoader interface {
	// LoadProvider returns a provider for the given package and (optional) version.
	// If no provider is available, returns nil.
	LoadProvider(host plugin.Host, ctx *plugin.Context, pkg tokens.Package, version *semver.Version) (plugin.Provider, error)
}

// region pulumiProviderLoader

// pulumiProviderLoader loads a resource provider for the given package name and version.
// This interface adapts the standard Pulumi plugin loader.
type pulumiProviderLoader struct {
	// runtime options for the loaded provider
	runtimeOptions map[string]interface{}
}

// NewPulumiProviderLoader returns a loader for installed Pulumi plugins.
func NewPulumiProviderLoader(runtimeOptions map[string]interface{}) *pulumiProviderLoader {
	return &pulumiProviderLoader{
		runtimeOptions: runtimeOptions,
	}
}

var _ ProviderLoader = &pulumiProviderLoader{}

func (p *pulumiProviderLoader) LoadProvider(host plugin.Host, ctx *plugin.Context, pkg tokens.Package, version *semver.Version) (plugin.Provider, error) {
	// Try to load and bind to a plugin.
	pp, err := plugin.NewProvider(host, ctx, pkg, version, p.runtimeOptions, false)
	return pp, err
}

// endregion
