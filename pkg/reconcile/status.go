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

package reconcile

import (
	"context"
	"github.com/pulumi/pulumi/pkg/v3/engine"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	snbackend "github.com/streamnative/pulumi-controller-runtime/pkg/backend"
)

const (
	pulumiStackType = tokens.Type("pulumi:pulumi:Stack")
)

// StackChanges reflects the planned changes for status update purposes.
// This structure specifically represents changes to the stack outputs.
// One of the main tasks of reconciliation is to update the status block,
// such that it reflects current status +with respect to a particular generation+.
// The reflected generation is then stored in the `observedGeneration` field.
// There are two opportunities to update status - during planning, and after execution.
type StackChanges struct {
	// Planning indicates the planning stage.
	Planning bool

	// Indicates whether any resource changes are planned or executed.
	HasResourceChanges bool

	// Outputs contains the new stack output values.  During planning,
	// some values might be unknown.
	Outputs resource.PropertyMap
}

var _ snbackend.UpdateEventHandler = &StackChanges{}

func NewStackChanges(dryRun bool) StackChanges {
	return StackChanges{
		Planning: dryRun,
	}
}

// EngineEvent applies an event to the accumulated changes
func (b *StackChanges) EngineEvent(ctx context.Context, event engine.Event) {
	switch event.Type {
	case engine.ResourceOutputsEvent:
		p := event.Payload().(engine.ResourceOutputsEventPayload)
		if p.Metadata.Type == pulumiStackType {
			if p.Metadata.New != nil {
				b.Outputs = p.Metadata.New.Outputs
			}
		}
	case engine.SummaryEvent:
		p := event.Payload().(engine.SummaryEventPayload)
		b.HasResourceChanges = p.ResourceChanges.HasChanges()
	}
}
