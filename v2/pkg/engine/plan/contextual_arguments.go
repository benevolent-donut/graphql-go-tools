package plan

import (
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

// ContextualArgumentPlan binds a contextual argument of a field planned on this planner to its capture.
type ContextualArgumentPlan struct {
	FieldRef     int // consumer field ref in the client operation
	ArgumentName string
	// Capture is nil when no ancestor level can set the context; the argument is then rendered as null.
	Capture *resolve.ContextualArgumentCapture // VariableName is set by the data source planner
}
