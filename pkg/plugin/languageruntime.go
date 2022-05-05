// Copyright (c) 2020 StreamNative, Inc.. All Rights Reserved.

package plugin

import (
	"context"

	ot "github.com/opentracing/opentracing-go"
	otlog "github.com/opentracing/opentracing-go/log"
	"github.com/pkg/errors"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/plugin"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// LanguageRuntimeLoader loads language runtimes.
type LanguageRuntimeLoader interface {
	// LoadLanguageRuntime returns a language runtime for given name.
	// If no language runtime is available, returns nil.
	LoadLanguageRuntime(runtime string) (plugin.LanguageRuntime, error)
}

type SimpleLanguageRuntimeLoader map[string]plugin.LanguageRuntime

var _ LanguageRuntimeLoader = SimpleLanguageRuntimeLoader{}

func (s SimpleLanguageRuntimeLoader) LoadLanguageRuntime(runtime string) (plugin.LanguageRuntime, error) {
	return s[runtime], nil
}

// region embeddedLanguageRuntime

// embeddedLanguageRuntime implements an in-process language runtime based on the Pulumi Go SDK.
type embeddedLanguageRuntime struct {
	ctx             context.Context // the context for program execution
	requiredPlugins []workspace.PluginInfo
	program         pulumi.RunFunc
}

// NewEmbeddedLanguageRuntime makes an embedded language runtime for a Go function based on the Pulumi Go SDK.
func NewEmbeddedLanguageRuntime(ctx context.Context, program pulumi.RunFunc) plugin.LanguageRuntime {
	return &embeddedLanguageRuntime{
		ctx:     ctx,
		program: program,
	}
}

func (p *embeddedLanguageRuntime) Close() error {
	return nil
}

func (p *embeddedLanguageRuntime) GetPluginInfo() (workspace.PluginInfo, error) {
	return workspace.PluginInfo{Name: "embeddedLanguageRuntime"}, nil
}

func (p *embeddedLanguageRuntime) GetRequiredPlugins(_ plugin.ProgInfo) ([]workspace.PluginInfo, error) {
	return nil, nil
}

func (p *embeddedLanguageRuntime) Run(info plugin.RunInfo) (msg string, bail bool, err error) {
	span, ctx := ot.StartSpanFromContext(p.ctx, "Run")
	defer func() {
		var f []otlog.Field
		if msg != "" {
			f = append(f, otlog.String("pulumi.message", msg))
		}
		if err != nil {
			f = append(f, otlog.Error(err))
		}
		span.LogFields(f...)
		span.Finish()
	}()
	span.LogFields(otlog.String("pulumi.project", info.Project), otlog.String("pulumi.stack", info.Stack))

	// Convert the program configuration to the format used by the Pulumi Go SDK
	cfg := make(map[string]string, len(info.Config))
	for k, v := range info.Config {
		cfg[k.String()] = v
	}
	cfgSecretKeys := make([]string, len(info.ConfigSecretKeys))
	for i, k := range info.ConfigSecretKeys {
		cfgSecretKeys[i] = k.String()
	}

	// Run the supplied program using the Pulumi Go SDK
	done := make(chan error)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				if e, ok := r.(error); ok {
					done <- e
				} else if s, ok := r.(string); ok {
					done <- errors.New(s)
				} else {
					panic(r)
				}
			}
		}()

		// Create a Pulumi SDK context
		runCtx, err := pulumi.NewContext(ctx, pulumi.RunInfo{
			Project:          info.Project,
			Stack:            info.Stack,
			Config:           cfg,
			ConfigSecretKeys: cfgSecretKeys,
			Parallel:         info.Parallel,
			DryRun:           info.DryRun,
			MonitorAddr:      info.MonitorAddress,
		})
		if err != nil {
			done <- errors.Wrapf(err, "creating Pulumi SDK context")
			return
		}
		defer func() { _ = runCtx.Close() }()

		err = pulumi.RunWithContext(runCtx, p.program)
		done <- err
		return
	}()
	if progerr := <-done; progerr != nil {
		return progerr.Error(), false, nil
	}
	return "", false, nil
}

// endregion
