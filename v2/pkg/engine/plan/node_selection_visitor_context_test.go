package plan

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/astnormalization"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/astprinter"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/operationreport"
)

const contextNodeSelectionDefinition = `
	type Query {
		queues: [Queue!]!
	}

	interface Queue {
		id: ID!
		owner: User
	}

	type ModerationQueue implements Queue {
		id: ID!
		includeSuspended: Boolean!
		owner: User
	}

	type AppealQueue implements Queue {
		id: ID!
		owner: User
	}

	type User {
		id: ID!
		name: String
	}
`

func contextNodeSelectionDataSources(flattenFragments bool, withContextualArguments bool) []DataSource {
	moderation := dsb().Hash(1).Id("moderation").
		WithBehavior(DataSourcePlanningBehavior{AlwaysFlattenFragments: flattenFragments, MergeAliasedRootNodes: true, AllowPlanningTypeName: true}).
		Schema(`
			type Query { queues: [Queue!]! }
			interface Queue { id: ID! owner: User }
			type ModerationQueue implements Queue { id: ID! includeSuspended: Boolean! owner: User }
			type AppealQueue implements Queue { id: ID! owner: User }
			type User { id: ID! }
		`).
		RootNode("Query", "queues").
		RootNode("ModerationQueue", "id", "includeSuspended", "owner").
		RootNode("AppealQueue", "id", "owner").
		RootNode("User", "id").
		ChildNode("Queue", "id", "owner").
		KeysMetadata(FederationFieldConfigurations{
			{TypeName: "ModerationQueue", SelectionSet: "id"},
			{TypeName: "AppealQueue", SelectionSet: "id"},
			{TypeName: "User", SelectionSet: "id", DisableEntityResolver: true},
		}).
		DS()

	users := dsb().Hash(2).Id("users").
		WithBehavior(DataSourcePlanningBehavior{MergeAliasedRootNodes: true, AllowPlanningTypeName: true}).
		Schema(`
			type ModerationQueue { id: ID! includeSuspended: Boolean! }
			type User { id: ID! name(includeSuspended: Boolean): String }
		`).
		RootNode("ModerationQueue", "id").
		AddRootNodeExternalFieldNames("ModerationQueue", "includeSuspended").
		RootNode("User", "id", "name").
		KeysMetadata(FederationFieldConfigurations{
			{TypeName: "ModerationQueue", SelectionSet: "id"},
			{TypeName: "User", SelectionSet: "id"},
		}).
		WithMetadata(func(data *FederationMetaData) {
			if !withContextualArguments {
				return
			}
			data.ContextualArguments = ContextualArgumentConfigurations{
				{
					TypeName:     "User",
					FieldName:    "name",
					ArgumentName: "includeSuspended",
					ContextName:  "userScope",
					Setters:      []ContextSetterConfiguration{{TypeName: "ModerationQueue", SelectionSet: "includeSuspended"}},
				},
			}
		}).
		DS()

	return []DataSource{moderation, users}
}

func contextSelectNodes(t *testing.T, builder *NodeSelectionBuilder, operation string) (*NodeSelectionResult, *ast.Document) {
	t.Helper()

	definition := unsafeparser.ParseGraphqlDocumentStringWithBaseSchema(contextNodeSelectionDefinition)
	op := unsafeparser.ParseGraphqlDocumentString(operation)
	report := &operationreport.Report{}
	astnormalization.NewWithOpts(
		astnormalization.WithExtractVariables(),
		astnormalization.WithInlineFragmentSpreads(),
		astnormalization.WithRemoveFragmentDefinitions(),
		astnormalization.WithRemoveUnusedVariables(),
	).NormalizeOperation(&op, &definition, report)
	require.False(t, report.HasErrors(), report.Error())

	builder.SetOperationName("Q")
	builder.ResetSkipFieldRefs()
	result := builder.SelectNodes(&op, &definition, report)
	require.False(t, report.HasErrors(), report.Error())

	return result, &op
}

func contextPrintOperation(t *testing.T, operation *ast.Document) string {
	t.Helper()

	printed, err := astprinter.PrintStringIndent(operation, "  ")
	require.NoError(t, err)
	return printed
}

func contextExpectedOperation(t *testing.T, operation string) string {
	t.Helper()

	expected := unsafeparser.ParseGraphqlDocumentString(operation)
	return contextPrintOperation(t, &expected)
}

// contextConsumerRequirements returns the requirements of the name fields left in the operation, keyed by the
// type condition of the inline fragment around their parent field.
func contextConsumerRequirements(t *testing.T, result *NodeSelectionResult, operation *ast.Document) map[string][]contextualArgumentRequirement {
	t.Helper()

	out := make(map[string][]contextualArgumentRequirement)
	queuesSelectionSet := operation.Fields[contextFieldInSelectionSet(t, operation, operation.OperationDefinitions[0].SelectionSet, "queues")].SelectionSet
	for _, selectionRef := range operation.SelectionSets[queuesSelectionSet].SelectionRefs {
		selection := operation.Selections[selectionRef]
		if selection.Kind != ast.SelectionKindInlineFragment {
			continue
		}
		fragmentSelectionSet := operation.InlineFragments[selection.Ref].SelectionSet
		if exists, _ := operation.SelectionSetHasFieldSelectionWithExactName(fragmentSelectionSet, []byte("owner")); !exists {
			continue
		}
		ownerRef := contextFieldInSelectionSet(t, operation, fragmentSelectionSet, "owner")
		consumerRef := contextFieldInSelectionSet(t, operation, operation.Fields[ownerRef].SelectionSet, "name")

		requirements, ok := result.fieldContextRequirements[fieldIndexKey{fieldRef: consumerRef, dsHash: 2}]
		require.True(t, ok, "the consumer under %s has no requirement", operation.InlineFragmentTypeConditionNameString(selection.Ref))
		out[operation.InlineFragmentTypeConditionNameString(selection.Ref)] = requirements
	}
	return out
}

func contextFieldInSelectionSet(t *testing.T, operation *ast.Document, selectionSetRef int, fieldName string) int {
	t.Helper()

	for _, selectionRef := range operation.SelectionSets[selectionSetRef].SelectionRefs {
		selection := operation.Selections[selectionRef]
		if selection.Kind == ast.SelectionKindField && operation.FieldNameString(selection.Ref) == fieldName {
			return selection.Ref
		}
	}
	require.Failf(t, "field not found", "no field %s in the selection set", fieldName)
	return ast.InvalidRef
}

func TestNodeSelectionVisitor_ContextualArguments(t *testing.T) {
	staticModerationQueueLevel := []contextualArgumentRequirement{
		{
			argumentName: "includeSuspended",
			levels: []resolve.ContextualArgumentLevel{
				{Depth: 1, Setters: []resolve.ContextualArgumentSetter{{TypeName: "ModerationQueue", Path: []string{"__context_0"}}}},
			},
		},
	}
	noCandidate := []contextualArgumentRequirement{{argumentName: "includeSuspended"}}

	t.Run("injection at an abstract level is reused after the flattening rewrite", func(t *testing.T) {
		builder := NewNodeSelectionBuilder(&Configuration{DataSources: contextNodeSelectionDataSources(true, true)})
		result, operation := contextSelectNodes(t, builder, `query Q { queues { owner { name } } }`)

		assert.Equal(t, contextExpectedOperation(t, `
			query Q {
				queues {
					... on AppealQueue {
						owner {
							name
							__typename
							id
						}
						__typename
					}
					... on ModerationQueue {
						owner {
							name
							__typename
							id
						}
						__typename
						__context_0: includeSuspended
					}
				}
			}`), contextPrintOperation(t, operation))

		requirements := contextConsumerRequirements(t, result, operation)
		assert.Equal(t, staticModerationQueueLevel, requirements["ModerationQueue"])
		assert.Equal(t, noCandidate, requirements["AppealQueue"])
	})

	t.Run("ancestor rewritten before and after the injection keeps a single context selection", func(t *testing.T) {
		builder := NewNodeSelectionBuilder(&Configuration{DataSources: contextNodeSelectionDataSources(true, true)})
		result, operation := contextSelectNodes(t, builder, `query Q { queues { ... on ModerationQueue { owner { name } } } }`)

		assert.Equal(t, 1, strings.Count(contextPrintOperation(t, operation), contextAliasPrefix))
		assert.Equal(t, staticModerationQueueLevel, contextConsumerRequirements(t, result, operation)["ModerationQueue"])
	})

	t.Run("consumer depends on every injected field that is left in the operation", func(t *testing.T) {
		builder := NewNodeSelectionBuilder(&Configuration{DataSources: contextNodeSelectionDataSources(true, true)})
		result, operation := contextSelectNodes(t, builder, `query Q { queues { owner { name } } }`)

		queuesSelectionSet := operation.Fields[contextFieldInSelectionSet(t, operation, operation.OperationDefinitions[0].SelectionSet, "queues")].SelectionSet
		for _, selectionRef := range operation.SelectionSets[queuesSelectionSet].SelectionRefs {
			fragmentRef := operation.Selections[selectionRef].Ref
			if operation.InlineFragmentTypeConditionNameString(fragmentRef) != "ModerationQueue" {
				continue
			}
			fragmentSelectionSet := operation.InlineFragments[fragmentRef].SelectionSet
			contextFieldRef := contextFieldInSelectionSet(t, operation, fragmentSelectionSet, "includeSuspended")
			consumerRef := contextFieldInSelectionSet(t, operation, operation.Fields[contextFieldInSelectionSet(t, operation, fragmentSelectionSet, "owner")].SelectionSet, "name")

			assert.Contains(t, result.fieldDependsOn[fieldIndexKey{fieldRef: consumerRef, dsHash: 2}], contextFieldRef)
			assert.Equal(t, fieldDependencyKindContext, result.fieldDependencyKind[fieldDependencyKey{field: consumerRef, dependsOn: contextFieldRef}])
			assert.Contains(t, result.skipFieldsRefs, contextFieldRef)
		}
	})

	t.Run("context alias skips names used in sibling fragments of the level", func(t *testing.T) {
		builder := NewNodeSelectionBuilder(&Configuration{DataSources: contextNodeSelectionDataSources(false, true)})
		result, operation := contextSelectNodes(t, builder, `query Q { queues { ... on AppealQueue { __context_0: id } ... on ModerationQueue { owner { name } } } }`)

		assert.Equal(t, contextExpectedOperation(t, `
			query Q {
				queues {
					... on AppealQueue {
						__context_0: id
					}
					... on ModerationQueue {
						owner {
							name
							__typename
							id
						}
						__context_0_1: includeSuspended
					}
				}
			}`), contextPrintOperation(t, operation))

		requirements := contextConsumerRequirements(t, result, operation)
		assert.Equal(t, []string{"__context_0_1"}, requirements["ModerationQueue"][0].levels[0].Setters[0].Path)
	})

	t.Run("alias counter starts again for every operation", func(t *testing.T) {
		builder := NewNodeSelectionBuilder(&Configuration{DataSources: contextNodeSelectionDataSources(false, true)})

		for range 2 {
			_, operation := contextSelectNodes(t, builder, `query Q { queues { ... on ModerationQueue { owner { name } } } }`)
			assert.Contains(t, contextPrintOperation(t, operation), "__context_0: includeSuspended")
			assert.NotContains(t, contextPrintOperation(t, operation), "__context_1")
		}
	})

	t.Run("consumer without a candidate level records the null decision and no injection", func(t *testing.T) {
		builder := NewNodeSelectionBuilder(&Configuration{DataSources: contextNodeSelectionDataSources(false, true)})
		result, operation := contextSelectNodes(t, builder, `query Q { queues { ... on AppealQueue { owner { name } } } }`)

		assert.NotContains(t, contextPrintOperation(t, operation), contextAliasPrefix)
		assert.Equal(t, noCandidate, contextConsumerRequirements(t, result, operation)["AppealQueue"])
	})

	t.Run("data sources without contextual arguments record nothing", func(t *testing.T) {
		builder := NewNodeSelectionBuilder(&Configuration{DataSources: contextNodeSelectionDataSources(false, false)})
		result, operation := contextSelectNodes(t, builder, `query Q { queues { ... on ModerationQueue { owner { name } } } }`)

		assert.NotContains(t, contextPrintOperation(t, operation), contextAliasPrefix)
		assert.Nil(t, result.fieldContextRequirements)
		assert.Nil(t, builder.nodeSelectionsVisitor.visitedFieldsContextChecks)
		assert.Nil(t, builder.nodeSelectionsVisitor.contextKeyBoundaries)
	})
}
