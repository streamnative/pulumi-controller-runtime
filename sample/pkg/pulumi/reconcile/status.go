package reconcile

import (
	"context"
	"github.com/pulumi/pulumi/pkg/v3/engine"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
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

type builder struct {
	changes StackChanges
}

func (b *builder) ApplyPlanning(planning bool) {
	b.changes.Planning = planning
}

func (b *builder) ApplyEngineEvent(ctx context.Context, event engine.Event) error {
	switch event.Type {
	case engine.ResourceOutputsEvent:
		p := event.Payload().(engine.ResourceOutputsEventPayload)
		if p.Metadata.Type == pulumiStackType {
			b.changes.Outputs = p.Metadata.New.Outputs
		}
	case engine.SummaryEvent:
		p := event.Payload().(engine.SummaryEventPayload)
		b.changes.HasResourceChanges = p.ResourceChanges.HasChanges()
	}
	return nil
}

func (b *builder) Build() StackChanges {
	return b.changes
}


