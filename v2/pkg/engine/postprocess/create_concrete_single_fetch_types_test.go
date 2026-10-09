package postprocess

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

func TestCreateConcreteSingleFetchTypes_ContextualArguments(t *testing.T) {
	queueCapture := resolve.ContextualArgumentCapture{
		VariableName: "contextualArgument_0",
		Levels: []resolve.ContextualArgumentLevel{
			{Depth: 1, Setters: []resolve.ContextualArgumentSetter{{TypeName: "ModerationQueue", Path: []string{"__context_0"}}}},
		},
	}
	policyCapture := resolve.ContextualArgumentCapture{
		VariableName: "contextualArgument_1",
		Levels: []resolve.ContextualArgumentLevel{
			{Depth: 1, Setters: []resolve.ContextualArgumentSetter{{TypeName: "ModerationQueue", Path: []string{"__context_1"}}}},
		},
	}
	contextualSegment := func(variableName string) resolve.TemplateSegment {
		return resolve.TemplateSegment{
			SegmentType:        resolve.VariableSegmentType,
			VariableKind:       resolve.ContextualArgumentVariableKind,
			VariableSourcePath: []string{variableName},
		}
	}
	staticSegment := func(data string) resolve.TemplateSegment {
		return resolve.TemplateSegment{SegmentType: resolve.StaticSegmentType, Data: []byte(data)}
	}

	process := func(t *testing.T, batch bool, variables string, contextualVariables ...resolve.Variable) resolve.Fetch {
		t.Helper()
		rootFetch := &resolve.SingleFetch{
			FetchDependencies: resolve.FetchDependencies{FetchID: 0},
			FetchConfiguration: resolve.FetchConfiguration{
				Input:     `{"method":"POST","url":"http://moderation","body":{"query":"query($a: ID!){moderationQueue(id: $a){flaggedUsers {id __typename}}}","variables":{"a":$$0$$}}}`,
				Variables: resolve.NewVariables(&resolve.ContextVariable{Path: []string{"a"}, Renderer: resolve.NewJSONVariableRenderer()}),
			},
		}
		entityFetch := &resolve.SingleFetch{
			FetchDependencies: resolve.FetchDependencies{FetchID: 1, DependsOnFetchIDs: []int{0}},
			FetchConfiguration: resolve.FetchConfiguration{
				Input:                                 `{"method":"POST","url":"http://users","body":{"query":"{}","variables":{"representations":[$$0$$]` + variables + `}}}`,
				Variables:                             append(resolve.NewVariables(resolve.NewResolvableObjectVariable(&resolve.Object{})), contextualVariables...),
				RequiresEntityBatchFetch:              batch,
				RequiresEntityFetch:                   !batch,
				SetTemplateOutputToNullOnVariableNull: true,
			},
		}
		p := &plan.SynchronousResponsePlan{
			Response: &resolve.GraphQLResponse{
				RawFetches: []*resolve.FetchItem{
					{Fetch: rootFetch},
					resolve.FetchItemWithPath(entityFetch, "moderationQueue.flaggedUsers", resolve.ObjectPath("moderationQueue"), resolve.ArrayPath("flaggedUsers")),
				},
				Data: &resolve.Object{},
			},
		}

		NewProcessor().Process(p)

		require.Len(t, p.Response.Fetches.ChildNodes, 2)
		root, ok := p.Response.Fetches.ChildNodes[0].Item.Fetch.(*resolve.SingleFetch)
		require.True(t, ok)
		require.Nil(t, root.Variables)
		return p.Response.Fetches.ChildNodes[1].Item.Fetch
	}

	t.Run("batch entity fetch copies the captures in variable order, not template order", func(t *testing.T) {
		fetch := process(t, true, `,"contextualArgument_0":$$2$$,"contextualArgument_1":$$1$$`,
			&resolve.ContextualArgumentVariable{Capture: policyCapture},
			&resolve.ContextualArgumentVariable{Capture: queueCapture},
		)

		batch, ok := fetch.(*resolve.BatchEntityFetch)
		require.True(t, ok)
		assert.Equal(t, []resolve.ContextualArgumentCapture{policyCapture, queueCapture}, batch.ContextualArguments)
		assert.Equal(t, []resolve.TemplateSegment{
			staticSegment(`],"contextualArgument_0":`),
			contextualSegment("contextualArgument_0"),
			staticSegment(`,"contextualArgument_1":`),
			contextualSegment("contextualArgument_1"),
			staticSegment(`}}}`),
		}, batch.Input.Footer.Segments)
		assert.True(t, batch.Input.Footer.SetTemplateOutputToNullOnVariableNull)
	})

	t.Run("entity fetch copies the capture", func(t *testing.T) {
		fetch := process(t, false, `,"contextualArgument_0":$$1$$`, &resolve.ContextualArgumentVariable{Capture: queueCapture})

		entity, ok := fetch.(*resolve.EntityFetch)
		require.True(t, ok)
		assert.Equal(t, []resolve.ContextualArgumentCapture{queueCapture}, entity.ContextualArguments)
		assert.Equal(t, []resolve.TemplateSegment{
			staticSegment(`],"contextualArgument_0":`),
			contextualSegment("contextualArgument_0"),
			staticSegment(`}}}`),
		}, entity.Input.Footer.Segments)
	})

	t.Run("entity fetches without contextual arguments have no captures", func(t *testing.T) {
		batch, ok := process(t, true, "").(*resolve.BatchEntityFetch)
		require.True(t, ok)
		assert.Nil(t, batch.ContextualArguments)

		entity, ok := process(t, false, "").(*resolve.EntityFetch)
		require.True(t, ok)
		assert.Nil(t, entity.ContextualArguments)
	})
}
