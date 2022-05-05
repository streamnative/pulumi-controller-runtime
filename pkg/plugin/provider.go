/*
Copyright 2021.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

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
