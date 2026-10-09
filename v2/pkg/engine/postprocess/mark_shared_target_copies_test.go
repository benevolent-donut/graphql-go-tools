package postprocess

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

func TestMarkSharedTargetCopies(t *testing.T) {
	capture := func(depths ...int) resolve.ContextualArgumentCapture {
		levels := make([]resolve.ContextualArgumentLevel, len(depths))
		for i, depth := range depths {
			levels[i] = resolve.ContextualArgumentLevel{Depth: depth, Setters: []resolve.ContextualArgumentSetter{{TypeName: "ModerationQueue", Path: []string{"__context_0"}}}}
		}
		return resolve.ContextualArgumentCapture{VariableName: "contextualArgument_0", Levels: levels}
	}
	rootFetch := func() *resolve.FetchItem {
		return &resolve.FetchItem{Fetch: &resolve.SingleFetch{
			FetchConfiguration: resolve.FetchConfiguration{Input: `{"method":"POST","url":"http://moderation","body":{"query":"{moderationQueues {id}}"}}`},
		}}
	}
	// entityFetch is an entity fetch of the users subgraph; a non-nil capture makes it contextual.
	entityFetch := func(fetchID int, batch bool, capture *resolve.ContextualArgumentCapture, path ...resolve.FetchItemPathElement) *resolve.FetchItem {
		query := `{"method":"POST","url":"http://users","body":{"query":"{f` + strconv.Itoa(fetchID) + `}","variables":{"representations":[$$0$$]`
		input := query + `}}}`
		variables := resolve.NewVariables(resolve.NewResolvableObjectVariable(&resolve.Object{}))
		if capture != nil {
			input = query + `,"contextualArgument_0":$$1$$}}}`
			variables = append(variables, &resolve.ContextualArgumentVariable{Capture: *capture})
		}
		fetch := &resolve.SingleFetch{
			FetchDependencies: resolve.FetchDependencies{FetchID: fetchID, DependsOnFetchIDs: []int{0}},
			FetchConfiguration: resolve.FetchConfiguration{
				Input:                                 input,
				Variables:                             variables,
				RequiresEntityBatchFetch:              batch,
				RequiresEntityFetch:                   !batch,
				SetTemplateOutputToNullOnVariableNull: true,
			},
		}
		return resolve.FetchItemWithPath(fetch, "", path...)
	}
	queues := resolve.ArrayPath("moderationQueues")
	flaggedUsers := resolve.ArrayPath("flaggedUsers")
	friends := resolve.ArrayPath("friends")
	organization := resolve.ObjectPath("organization")

	process := func(t *testing.T, items ...*resolve.FetchItem) *resolve.FetchTreeNode {
		t.Helper()
		p := &plan.SynchronousResponsePlan{Response: &resolve.GraphQLResponse{RawFetches: items, Data: &resolve.Object{}}}
		NewProcessor().Process(p)
		return p.Response.Fetches
	}

	t.Run("a batch fetch between a level and a contextual fetch copies its shared targets", func(t *testing.T) {
		queueCapture := capture(1)
		tree := process(t, rootFetch(),
			entityFetch(1, true, nil, queues, flaggedUsers),
			entityFetch(2, true, &queueCapture, queues, flaggedUsers, friends),
		)

		assert.True(t, copiesSharedTargets(t, tree, 1))
		assert.False(t, copiesSharedTargets(t, tree, 2))
	})

	t.Run("fetches above every level or on another path merge without copies", func(t *testing.T) {
		queueCapture := capture(2)
		tree := process(t, rootFetch(),
			entityFetch(1, true, nil, organization),
			entityFetch(2, true, nil, organization, resolve.ArrayPath("queues")),
			entityFetch(3, true, nil, organization, resolve.ArrayPath("members")),
			entityFetch(4, true, &queueCapture, organization, resolve.ArrayPath("queues"), flaggedUsers),
		)

		assert.False(t, copiesSharedTargets(t, tree, 1))
		assert.True(t, copiesSharedTargets(t, tree, 2))
		assert.False(t, copiesSharedTargets(t, tree, 3))
	})

	t.Run("a level on the root data never tells positions apart", func(t *testing.T) {
		rootCapture := capture(0)
		tree := process(t, rootFetch(),
			entityFetch(1, true, nil, queues, flaggedUsers),
			entityFetch(2, true, &rootCapture, queues, flaggedUsers, friends),
		)

		assert.False(t, copiesSharedTargets(t, tree, 1))
	})

	t.Run("a contextual fetch above another contextual fetch copies its shared targets", func(t *testing.T) {
		queueCapture := capture(1)
		tree := process(t, rootFetch(),
			entityFetch(1, true, &queueCapture, queues, flaggedUsers),
			entityFetch(2, true, &queueCapture, queues, flaggedUsers, friends),
		)

		assert.True(t, copiesSharedTargets(t, tree, 1))
		assert.False(t, copiesSharedTargets(t, tree, 2))
	})

	t.Run("plans without contextual fetches are unchanged", func(t *testing.T) {
		tree := process(t, rootFetch(),
			entityFetch(1, true, nil, queues, flaggedUsers),
			entityFetch(2, true, nil, queues, flaggedUsers, friends),
		)

		assert.False(t, copiesSharedTargets(t, tree, 1))
		assert.False(t, copiesSharedTargets(t, tree, 2))
	})

	t.Run("a deferred contextual fetch flags a fetch of the initial response", func(t *testing.T) {
		queueCapture := capture(1)
		deferred := entityFetch(2, true, &queueCapture, queues, flaggedUsers, friends)
		deferred.Fetch.(*resolve.SingleFetch).DeferID = 1
		p := &plan.DeferResponsePlan{Response: &resolve.GraphQLDeferResponse{
			Response: &resolve.GraphQLResponse{
				RawFetches: []*resolve.FetchItem{rootFetch(), entityFetch(1, true, nil, queues, flaggedUsers), deferred},
				Data:       &resolve.Object{},
			},
			DeferDescriptors: map[int]resolve.DeferDescriptor{1: {ID: 1}},
		}}

		NewProcessor().Process(p)

		require.NotNil(t, p.Response.DeferTree)
		assert.True(t, copiesSharedTargets(t, p.Response.Response.Fetches, 1))
		assert.False(t, copiesSharedTargets(t, p.Response.DeferTree.Item.Fetches, 2))
	})
}

func TestMarkSharedTargetCopies_MultiEntityFetchEntries(t *testing.T) {
	const source = `query($representations: [_Any!]!){_entities(representations: $representations){... on User {__typename friends {id}}}}`
	member := func(fetchID int, mergePath string) *resolve.FetchItem {
		item := buildMergeMember(t, mergeMemberSpec{
			fetchID: fetchID, deps: []int{0}, batch: true, mergePath: mergePath, source: source,
			input:        `{"method":"POST","url":"http://x","body":{"query":"` + source + `","variables":{"representations":[$$0$$]}}}`,
			fragments:    []resolve.SubgraphVariable{{Name: "representations", Value: []byte("[$$0$$]")}},
			variables:    resolve.NewVariables(resolve.NewResolvableObjectVariable(&resolve.Object{})),
			responsePath: "moderationQueues.@.flaggedUsers",
		})
		item.FetchPath = []resolve.FetchItemPathElement{resolve.ArrayPath("moderationQueues"), resolve.ArrayPath("flaggedUsers")}
		return item
	}
	contextual := &resolve.SingleFetch{
		FetchDependencies: resolve.FetchDependencies{FetchID: 3, DependsOnFetchIDs: []int{1}},
		FetchConfiguration: resolve.FetchConfiguration{
			Input: `{"method":"POST","url":"http://users","body":{"query":"{}","variables":{"representations":[$$0$$],"contextualArgument_0":$$1$$}}}`,
			Variables: resolve.NewVariables(resolve.NewResolvableObjectVariable(&resolve.Object{}), &resolve.ContextualArgumentVariable{Capture: resolve.ContextualArgumentCapture{
				VariableName: "contextualArgument_0",
				Levels:       []resolve.ContextualArgumentLevel{{Depth: 1, Setters: []resolve.ContextualArgumentSetter{{TypeName: "ModerationQueue", Path: []string{"__context_0"}}}}},
			}}),
			RequiresEntityBatchFetch: true,
		},
	}
	p := &plan.SynchronousResponsePlan{Response: &resolve.GraphQLResponse{
		RawFetches: []*resolve.FetchItem{
			buildMergeNonCandidate(0, nil, `{"q":"0"}`),
			member(1, "a"),
			member(2, "b"),
			resolve.FetchItemWithPath(contextual, "", resolve.ArrayPath("moderationQueues"), resolve.ArrayPath("flaggedUsers"), resolve.ArrayPath("friends")),
		},
		Data: &resolve.Object{},
	}}

	NewProcessor(EnableMultiFetch()).Process(p)

	multi := findMultiEntityFetch(p.Response.Fetches)
	require.NotNil(t, multi)
	require.Len(t, multi.Input.Entries, 2)
	for _, entry := range multi.Input.Entries {
		assert.True(t, entry.DeepCopyForSharedTargets)
	}
}

// copiesSharedTargets returns the DeepCopyForSharedTargets flag of the batch entity fetch with fetchID.
func copiesSharedTargets(t *testing.T, tree *resolve.FetchTreeNode, fetchID int) bool {
	t.Helper()
	var found *resolve.BatchEntityFetch
	visitFetchItems(tree, func(item *resolve.FetchItem) {
		if fetch, ok := item.Fetch.(*resolve.BatchEntityFetch); ok && fetch.FetchID == fetchID {
			found = fetch
		}
	})
	require.NotNil(t, found, "no batch entity fetch with id %d", fetchID)
	return found.DeepCopyForSharedTargets
}
