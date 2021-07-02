// Copyright (c) 2020 StreamNative, Inc.. All Rights Reserved.

package plugin

import (
	"sync"

	"github.com/hashicorp/go-multierror"
	"github.com/opentracing/opentracing-go"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/logging"

	"github.com/blang/semver"
	"github.com/pkg/errors"
	"github.com/pulumi/pulumi/sdk/v3/go/common/diag"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/plugin"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
)

// host implements the plugin.Host interface as needed by the Pulumi engine.
// This includes resolving the following types of plugins:
// - language runtimes (which produce resource graphs)
// - resource providers (which implement resource types)
// - analyzers (which implement policies and verify resource graphs)

type host struct {
	sink                    diag.Sink
	statusSink              diag.Sink
	ctx                     *plugin.Context
	providerLoader          ProviderLoader
	languageRuntimeLoader   LanguageRuntimeLoader
	languagePlugins         map[string]*languagePlugin          // a cache of language plugins and their processes.
	resourcePlugins         map[plugin.Provider]*resourcePlugin // the set of loaded resource plugins.
	reportedResourcePlugins map[string]struct{}                 // the set of unique resource plugins we'll report.
	plugins                 []workspace.PluginInfo              // a list of plugins allocated by this host.
	server                  *hostServer                         // the server's RPC machinery.
	m                       sync.Mutex
}

func NewHost(sink, statusSink diag.Sink, tracingSpan opentracing.Span,
	languageRuntimeLoader LanguageRuntimeLoader,
	providerLoader ProviderLoader) (*host, error) {

	host := &host{
		sink:                    sink,
		statusSink:              statusSink,
		languageRuntimeLoader:   languageRuntimeLoader,
		providerLoader:          providerLoader,
		languagePlugins:         make(map[string]*languagePlugin),
		resourcePlugins:         make(map[plugin.Provider]*resourcePlugin),
		reportedResourcePlugins: make(map[string]struct{}),
	}

	// Fire up a gRPC server to listen for requests.  This acts as a RPC interface that plugins can use
	// to "phone home" in case there are things the host must do on behalf of the plugins (like log, etc).
	svr, err := newHostServer(host, tracingSpan)
	if err != nil {
		return nil, err
	}
	host.server = svr

	return host, nil
}

type languagePlugin struct {
	Plugin plugin.LanguageRuntime
	Info   workspace.PluginInfo
}
type resourcePlugin struct {
	Plugin plugin.Provider
	Info   workspace.PluginInfo
}

func (host *host) SetPluginContext(ctx *plugin.Context) {
	host.ctx = ctx
}

func (host *host) SetLanguageRuntimeLoader(loader LanguageRuntimeLoader) {
	host.languageRuntimeLoader = loader
}

func (host *host) SetProviderLoader(providerLoader ProviderLoader) {
	host.providerLoader = providerLoader
}

var _ plugin.Host = &host{}

func (host *host) SignalCancellation() error {
	host.m.Lock()
	defer host.m.Unlock()
	var result error
	for _, plug := range host.resourcePlugins {
		if err := plug.Plugin.SignalCancellation(); err != nil {
			result = multierror.Append(result, errors.Wrapf(err,
				"Error signaling cancellation to resource provider '%s'", plug.Info.Name))
		}
	}
	return result
}

func (host *host) Close() error {
	host.m.Lock()
	defer host.m.Unlock()

	// Close all plugins.
	for _, plug := range host.resourcePlugins {
		if err := plug.Plugin.Close(); err != nil {
			logging.V(5).Infof("Error closing '%s' resource plugin during shutdown; ignoring: %v", plug.Info.Name, err)
		}
	}
	for _, plug := range host.languagePlugins {
		if err := plug.Plugin.Close(); err != nil {
			logging.V(5).Infof("Error closing '%s' language plugin during shutdown; ignoring: %v", plug.Info.Name, err)
		}
	}

	// Empty out all maps.
	host.languagePlugins = make(map[string]*languagePlugin)
	host.resourcePlugins = make(map[plugin.Provider]*resourcePlugin)

	// Finally, shut down the host's gRPC server.
	return host.server.Cancel()
}

func (host *host) ServerAddr() string {
	return host.server.Address()
}

// region Logging

func (host *host) Log(sev diag.Severity, urn resource.URN, msg string, streamID int32) {
	host.sink.Logf(sev, diag.StreamMessage(urn, msg, streamID))
}

func (host *host) LogStatus(sev diag.Severity, urn resource.URN, msg string, streamID int32) {
	host.statusSink.Logf(sev, diag.StreamMessage(urn, msg, streamID))
}

// endregion

// region Providers

func (host *host) Provider(pkg tokens.Package, version *semver.Version) (plugin.Provider, error) {
	host.m.Lock()
	defer host.m.Unlock()

	// Try to load and bind to a plugin.
	plug, err := host.providerLoader.LoadProvider(host, host.ctx, pkg, version)
	if err == nil && plug != nil {
		info, infoerr := plug.GetPluginInfo()
		if infoerr != nil {
			return nil, infoerr
		}

		// Record the result and add the plugin's info to our list of loaded plugins if it's the first copy of its
		// kind.
		key := info.Name
		if info.Version != nil {
			key += info.Version.String()
		}
		_, alreadyReported := host.reportedResourcePlugins[key]
		if !alreadyReported {
			host.reportedResourcePlugins[key] = struct{}{}
			host.plugins = append(host.plugins, info)
		}
		host.resourcePlugins[plug] = &resourcePlugin{Plugin: plug, Info: info}
	}

	return plug, err
}

func (host *host) CloseProvider(provider plugin.Provider) error {
	host.m.Lock()
	defer host.m.Unlock()
	if err := provider.Close(); err != nil {
		return err
	}
	delete(host.resourcePlugins, provider)
	return nil
}

// endregion

// region LanguageRuntimes

func (host *host) LanguageRuntime(runtime string) (plugin.LanguageRuntime, error) {
	host.m.Lock()
	defer host.m.Unlock()

	// First see if we already loaded this plugin.
	if plug, has := host.languagePlugins[runtime]; has {
		contract.Assert(plug != nil)
		return plug.Plugin, nil
	}

	// If not, allocate a new one.
	plug, err := host.languageRuntimeLoader.LoadLanguageRuntime(runtime)
	if err == nil && plug != nil {
		info, infoerr := plug.GetPluginInfo()
		if infoerr != nil {
			return nil, infoerr
		}

		// Memoize the result.
		host.plugins = append(host.plugins, info)
		host.languagePlugins[runtime] = &languagePlugin{Plugin: plug, Info: info}
	}

	return plug, err
}

// endregion

// region Analyzers

func (host *host) Analyzer(_ tokens.QName) (plugin.Analyzer, error) {
	return nil, errors.New("unsupported: Host::Analyzer")
}

func (host *host) PolicyAnalyzer(_ tokens.QName, _ string,
	_ *plugin.PolicyAnalyzerOptions) (plugin.Analyzer, error) {
	return nil, errors.New("unsupported: Host::PolicyAnalyzer")
}

func (host *host) ListAnalyzers() []plugin.Analyzer {
	return nil
}

// endregion

// region Plugins

func (host *host) ListPlugins() []workspace.PluginInfo {
	return host.plugins
}

func (host *host) EnsurePlugins(plugins []workspace.PluginInfo, kinds plugin.Flags) error {
	// Use a multieerror to track failures so we can return one big list of all failures at the end.
	var result error
	for _, p := range plugins {
		switch p.Kind {
		case workspace.AnalyzerPlugin:
			if kinds&plugin.AnalyzerPlugins != 0 {
				if _, err := host.Analyzer(tokens.QName(p.Name)); err != nil {
					result = multierror.Append(result,
						errors.Wrapf(err, "failed to load analyzer plugin %s", p.Name))
				}
			}
		case workspace.LanguagePlugin:
			if kinds&plugin.LanguagePlugins != 0 {
				if _, err := host.LanguageRuntime(p.Name); err != nil {
					result = multierror.Append(result,
						errors.Wrapf(err, "failed to load language plugin %s", p.Name))
				}
			}
		case workspace.ResourcePlugin:
			if kinds&plugin.ResourcePlugins != 0 {
				if _, err := host.Provider(tokens.Package(p.Name), p.Version); err != nil {
					result = multierror.Append(result,
						errors.Wrapf(err, "failed to load resource plugin %s", p.Name))
				}
			}
		default:
			contract.Failf("unexpected plugin kind: %s", p.Kind)
		}
	}
	return result
}

// endregion
