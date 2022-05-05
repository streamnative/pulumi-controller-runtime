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
	"fmt"
	"sync/atomic"

	"github.com/opentracing/opentracing-go"

	pbempty "github.com/golang/protobuf/ptypes/empty"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
	"google.golang.org/grpc"

	"github.com/pulumi/pulumi/sdk/v3/go/common/diag"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/plugin"

	"github.com/pulumi/pulumi/sdk/v3/go/common/util/rpcutil"
	pulumirpc "github.com/pulumi/pulumi/sdk/v3/proto/go"
)

// hostServer is a server for engine callbacks made by the resource provider plugins.
// This includes log events and resource stuff.
type hostServer struct {
	host   plugin.Host // the host for this RPC server.
	addr   string      // the address the host is listening on.
	cancel chan bool   // a channel that can cancel the server.
	done   chan error  // a channel that resolves when the server completes.

	// hostServer contains little bits of state that can't be saved in the language host.
	rootUrn atomic.Value // a root resource URN that has been saved via SetRootResource
}

// newHostServer creates a new host server wired up to the given host.
func newHostServer(host plugin.Host, tracingSpan opentracing.Span) (*hostServer, error) {
	// New up an engine RPC server.
	engine := &hostServer{
		host:   host,
		cancel: make(chan bool),
	}

	// Fire up a gRPC server and start listening for incoming requests.
	port, done, err := rpcutil.Serve(0, engine.cancel, []func(*grpc.Server) error{
		func(srv *grpc.Server) error {
			pulumirpc.RegisterEngineServer(srv, engine)
			return nil
		},
	}, tracingSpan)
	if err != nil {
		return nil, err
	}

	engine.addr = fmt.Sprintf("127.0.0.1:%d", port)
	engine.done = done
	engine.rootUrn.Store("")

	return engine, nil
}

// Address returns the address at which the engine's RPC server may be reached.
func (eng *hostServer) Address() string {
	return eng.addr
}

// Cancel signals that the engine should be terminated, awaits its termination, and returns any errors that result.
func (eng *hostServer) Cancel() error {
	eng.cancel <- true
	return <-eng.done
}

// Log logs a global message in the engine, including errors and warnings.
func (eng *hostServer) Log(ctx context.Context, req *pulumirpc.LogRequest) (*pbempty.Empty, error) {
	var sev diag.Severity
	switch req.Severity {
	case pulumirpc.LogSeverity_DEBUG:
		sev = diag.Debug
	case pulumirpc.LogSeverity_INFO:
		sev = diag.Info
	case pulumirpc.LogSeverity_WARNING:
		sev = diag.Warning
	case pulumirpc.LogSeverity_ERROR:
		sev = diag.Error
	default:
		return nil, errors.Errorf("Unrecognized logging severity: %v", req.Severity)
	}

	if req.Ephemeral {
		eng.host.LogStatus(sev, resource.URN(req.Urn), req.Message, req.StreamId)
	} else {
		eng.host.Log(sev, resource.URN(req.Urn), req.Message, req.StreamId)
	}
	return &pbempty.Empty{}, nil
}

// GetRootResource returns the current root resource's URN, which will serve as the parent of resources that are
// otherwise left unparented.
func (eng *hostServer) GetRootResource(ctx context.Context,
	req *pulumirpc.GetRootResourceRequest) (*pulumirpc.GetRootResourceResponse, error) {
	var response pulumirpc.GetRootResourceResponse
	response.Urn = eng.rootUrn.Load().(string)
	return &response, nil
}

// SetRootResource sets the current root resource's URN. Generally only called on startup when the Stack resource is
// registered.
func (eng *hostServer) SetRootResource(ctx context.Context,
	req *pulumirpc.SetRootResourceRequest) (*pulumirpc.SetRootResourceResponse, error) {
	var response pulumirpc.SetRootResourceResponse
	eng.rootUrn.Store(req.GetUrn())
	return &response, nil
}
