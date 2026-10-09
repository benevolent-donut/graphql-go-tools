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
		organization: Organization
	}

	type Organization {
		id: ID!
		allQueues: [Queue!]!
	}

	interface Queue {
		id: ID!
		owner: User
	}

	type ModerationQueue implements Queue {
		id: ID!
		includeSuspended: Boolean!
		owner: User
		lead(active: Boolean): Person
	}

	type AppealQueue implements Queue {
		id: ID!
		includeSuspended: Boolean!
		appealNote: String
		owner: User
	}

	type Person {
		id: ID!
		name: String
	}

	type User {
		id: ID!
		name: String
		a: String
		b: String
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

// contextReuseDataSources gives User.a and User.b one context each, set by ModerationQueue with the given selections
// on a data source that flattens fragments, so both consumers search for their selection after the rewrite.
func contextReuseDataSources(firstSelection, secondSelection string) []DataSource {
	moderation := dsb().Hash(1).Id("moderation").
		WithBehavior(DataSourcePlanningBehavior{AlwaysFlattenFragments: true, MergeAliasedRootNodes: true, AllowPlanningTypeName: true}).
		Schema(`
			type Query { queues: [Queue!]! }
			interface Queue { id: ID! owner: User }
			type ModerationQueue implements Queue { id: ID! owner: User lead(active: Boolean): Person }
			type AppealQueue implements Queue { id: ID! owner: User }
			type Person { id: ID! name: String }
			type User { id: ID! }
		`).
		RootNode("Query", "queues").
		RootNode("ModerationQueue", "id", "owner", "lead").
		RootNode("AppealQueue", "id", "owner").
		RootNode("User", "id").
		ChildNode("Queue", "id", "owner").
		ChildNode("Person", "id", "name").
		KeysMetadata(FederationFieldConfigurations{
			{TypeName: "ModerationQueue", SelectionSet: "id"},
			{TypeName: "AppealQueue", SelectionSet: "id"},
			{TypeName: "User", SelectionSet: "id", DisableEntityResolver: true},
		}).
		DS()

	users := dsb().Hash(2).Id("users").
		WithBehavior(DataSourcePlanningBehavior{MergeAliasedRootNodes: true, AllowPlanningTypeName: true}).
		Schema(`type User { id: ID! a(x: String): String b(y: String): String }`).
		RootNode("User", "id", "a", "b").
		KeysMetadata(FederationFieldConfigurations{{TypeName: "User", SelectionSet: "id"}}).
		WithMetadata(func(data *FederationMetaData) {
			data.ContextualArguments = ContextualArgumentConfigurations{
				{TypeName: "User", FieldName: "a", ArgumentName: "x", ContextName: "first", Setters: []ContextSetterConfiguration{{TypeName: "ModerationQueue", SelectionSet: firstSelection}}},
				{TypeName: "User", FieldName: "b", ArgumentName: "y", ContextName: "second", Setters: []ContextSetterConfiguration{{TypeName: "ModerationQueue", SelectionSet: secondSelection}}},
			}
		}).
		DS()

	return []DataSource{moderation, users}
}

// contextSharedOwnerDataSources resolve Organization.allQueues in moderation and in appeals, which only defines
// AppealQueue, so a query that needs both selects the owner field on both data sources.
func contextSharedOwnerDataSources() []DataSource {
	moderation := dsb().Hash(1).Id("moderation").
		WithBehavior(DataSourcePlanningBehavior{MergeAliasedRootNodes: true, AllowPlanningTypeName: true}).
		Schema(`
			type Query { organization: Organization }
			type Organization { id: ID! allQueues: [Queue!]! }
			interface Queue { id: ID! owner: User includeSuspended: Boolean! }
			type ModerationQueue implements Queue { id: ID! owner: User includeSuspended: Boolean! }
			type AppealQueue implements Queue { id: ID! owner: User includeSuspended: Boolean! }
			type User { id: ID! }
		`).
		RootNode("Query", "organization").
		RootNode("Organization", "id", "allQueues").
		RootNode("User", "id").
		ChildNode("Queue", "id", "owner", "includeSuspended").
		ChildNode("ModerationQueue", "id", "owner", "includeSuspended").
		ChildNode("AppealQueue", "id", "owner", "includeSuspended").
		KeysMetadata(FederationFieldConfigurations{
			{TypeName: "Organization", SelectionSet: "id"},
			{TypeName: "User", SelectionSet: "id", DisableEntityResolver: true},
		}).
		DS()

	appeals := dsb().Hash(3).Id("appeals").
		WithBehavior(DataSourcePlanningBehavior{MergeAliasedRootNodes: true, AllowPlanningTypeName: true}).
		Schema(`
			type Organization { id: ID! allQueues: [Queue!]! }
			interface Queue { id: ID! includeSuspended: Boolean! }
			type AppealQueue implements Queue { id: ID! includeSuspended: Boolean! appealNote: String }
		`).
		RootNode("Organization", "id", "allQueues").
		ChildNode("Queue", "id", "includeSuspended").
		ChildNode("AppealQueue", "id", "includeSuspended", "appealNote").
		KeysMetadata(FederationFieldConfigurations{{TypeName: "Organization", SelectionSet: "id"}}).
		DS()

	users := dsb().Hash(2).Id("users").
		WithBehavior(DataSourcePlanningBehavior{MergeAliasedRootNodes: true, AllowPlanningTypeName: true}).
		Schema(`type User { id: ID! a(x: Boolean): String }`).
		RootNode("User", "id", "a").
		KeysMetadata(FederationFieldConfigurations{{TypeName: "User", SelectionSet: "id"}}).
		WithMetadata(func(data *FederationMetaData) {
			data.ContextualArguments = ContextualArgumentConfigurations{
				{
					TypeName:     "User",
					FieldName:    "a",
					ArgumentName: "x",
					ContextName:  "queueScope",
					Setters: []ContextSetterConfiguration{
						{TypeName: "AppealQueue", SelectionSet: "includeSuspended"},
						{TypeName: "ModerationQueue", SelectionSet: "includeSuspended"},
					},
				},
			}
		}).
		DS()

	return []DataSource{moderation, appeals, users}
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

// contextConsumerRequirements returns the requirements of the consumer fields left in the operation, keyed by the
// type condition of the inline fragment around their parent field.
func contextConsumerRequirements(t *testing.T, result *NodeSelectionResult, operation *ast.Document, consumerName string) map[string][]contextualArgumentRequirement {
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
		consumerRef := contextFieldInSelectionSet(t, operation, operation.Fields[ownerRef].SelectionSet, consumerName)

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

		requirements := contextConsumerRequirements(t, result, operation, "name")
		assert.Equal(t, staticModerationQueueLevel, requirements["ModerationQueue"])
		assert.Equal(t, noCandidate, requirements["AppealQueue"])
	})

	t.Run("ancestor rewritten before and after the injection keeps a single context selection", func(t *testing.T) {
		builder := NewNodeSelectionBuilder(&Configuration{DataSources: contextNodeSelectionDataSources(true, true)})
		result, operation := contextSelectNodes(t, builder, `query Q { queues { ... on ModerationQueue { owner { name } } } }`)

		assert.Equal(t, 1, strings.Count(contextPrintOperation(t, operation), contextAliasPrefix))
		assert.Equal(t, staticModerationQueueLevel, contextConsumerRequirements(t, result, operation, "name")["ModerationQueue"])
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

		requirements := contextConsumerRequirements(t, result, operation, "name")
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
		assert.Equal(t, noCandidate, contextConsumerRequirements(t, result, operation, "name")["AppealQueue"])
	})

	t.Run("data sources without contextual arguments record nothing", func(t *testing.T) {
		builder := NewNodeSelectionBuilder(&Configuration{DataSources: contextNodeSelectionDataSources(false, false)})
		result, operation := contextSelectNodes(t, builder, `query Q { queues { ... on ModerationQueue { owner { name } } } }`)

		assert.NotContains(t, contextPrintOperation(t, operation), contextAliasPrefix)
		assert.Nil(t, result.fieldContextRequirements)
		assert.Nil(t, builder.nodeSelectionsVisitor.visitedFieldsContextChecks)
		assert.Nil(t, builder.nodeSelectionsVisitor.contextKeyBoundaries)
	})

	moderationQueueLevel := func(argumentName string, path ...string) []contextualArgumentRequirement {
		return []contextualArgumentRequirement{
			{
				argumentName: argumentName,
				levels: []resolve.ContextualArgumentLevel{
					{Depth: 1, Setters: []resolve.ContextualArgumentSetter{{TypeName: "ModerationQueue", Path: path}}},
				},
			},
		}
	}

	t.Run("a reused context field selects the same nested field", func(t *testing.T) {
		builder := NewNodeSelectionBuilder(&Configuration{DataSources: contextReuseDataSources("lead { id }", "lead { name }")})
		result, operation := contextSelectNodes(t, builder, `query Q { queues { owner { a b } } }`)

		assert.Equal(t, contextExpectedOperation(t, `
			query Q {
				queues {
					... on AppealQueue {
						owner {
							a
							b
							__typename
							id
						}
						__typename
					}
					... on ModerationQueue {
						owner {
							a
							b
							__typename
							id
						}
						__typename
						__context_0: lead {
							id
						}
						__context_1: lead {
							name
						}
					}
				}
			}`), contextPrintOperation(t, operation))

		assert.Equal(t, map[string][]contextualArgumentRequirement{
			"AppealQueue":     {{argumentName: "x"}},
			"ModerationQueue": moderationQueueLevel("x", "__context_0", "id"),
		}, contextConsumerRequirements(t, result, operation, "a"))
		assert.Equal(t, map[string][]contextualArgumentRequirement{
			"AppealQueue":     {{argumentName: "y"}},
			"ModerationQueue": moderationQueueLevel("y", "__context_1", "name"),
		}, contextConsumerRequirements(t, result, operation, "b"))
	})

	t.Run("a reused context field has the same arguments", func(t *testing.T) {
		builder := NewNodeSelectionBuilder(&Configuration{DataSources: contextReuseDataSources("lead(active: true) { id }", "lead(active: false) { id }")})
		result, operation := contextSelectNodes(t, builder, `query Q { queues { owner { a b } } }`)

		assert.Equal(t, contextExpectedOperation(t, `
			query Q {
				queues {
					... on AppealQueue {
						owner {
							a
							b
							__typename
							id
						}
						__typename
					}
					... on ModerationQueue {
						owner {
							a
							b
							__typename
							id
						}
						__typename
						__context_0: lead(active: true) {
							id
						}
						__context_1: lead(active: false) {
							id
						}
					}
				}
			}`), contextPrintOperation(t, operation))

		assert.Equal(t, map[string][]contextualArgumentRequirement{
			"AppealQueue":     {{argumentName: "x"}},
			"ModerationQueue": moderationQueueLevel("x", "__context_0", "id"),
		}, contextConsumerRequirements(t, result, operation, "a"))
		assert.Equal(t, map[string][]contextualArgumentRequirement{
			"AppealQueue":     {{argumentName: "y"}},
			"ModerationQueue": moderationQueueLevel("y", "__context_1", "id"),
		}, contextConsumerRequirements(t, result, operation, "b"))
	})

	t.Run("an owner field selected on two data sources only yields the types both define", func(t *testing.T) {
		builder := NewNodeSelectionBuilder(&Configuration{DataSources: contextSharedOwnerDataSources()})
		result, operation := contextSelectNodes(t, builder, `query Q { organization { allQueues { owner { a } ... on AppealQueue { appealNote } } } }`)

		organizationRef := contextFieldInSelectionSet(t, operation, operation.OperationDefinitions[0].SelectionSet, "organization")
		allQueuesRef := contextFieldInSelectionSet(t, operation, operation.Fields[organizationRef].SelectionSet, "allQueues")
		require.True(t, result.nodeSuggestions.IsSelectedOnDataSource(allQueuesRef, 1))
		require.True(t, result.nodeSuggestions.IsSelectedOnDataSource(allQueuesRef, 3))

		assert.Equal(t, contextExpectedOperation(t, `
			query Q {
				organization {
					allQueues {
						owner {
							a
							__typename
							id
						}
						... on AppealQueue {
							appealNote
						}
						__typename
						... on AppealQueue {
							__context_0: includeSuspended
						}
					}
					__typename
					id
				}
			}`), contextPrintOperation(t, operation))

		consumerRef := contextFieldInSelectionSet(t, operation, operation.Fields[contextFieldInSelectionSet(t, operation, operation.Fields[allQueuesRef].SelectionSet, "owner")].SelectionSet, "a")
		assert.Equal(t, []contextualArgumentRequirement{
			{
				argumentName: "x",
				levels: []resolve.ContextualArgumentLevel{
					{Depth: 2, MatchByTypename: true, Setters: []resolve.ContextualArgumentSetter{{TypeName: "AppealQueue", Path: []string{"__context_0"}}}},
				},
			},
		}, result.fieldContextRequirements[fieldIndexKey{fieldRef: consumerRef, dsHash: 2}])
	})

	t.Run("requirements of a consumer replaced by a rewrite are dropped", func(t *testing.T) {
		builder := NewNodeSelectionBuilder(&Configuration{DataSources: contextNodeSelectionDataSources(true, true)})
		result, operation := contextSelectNodes(t, builder, `query Q { queues { owner { name } } }`)

		requirements := contextConsumerRequirements(t, result, operation, "name")
		require.Len(t, requirements, 2)
		assert.Len(t, result.fieldContextRequirements, len(requirements))
		assert.Len(t, builder.nodeSelectionsVisitor.visitedFieldsContextChecks, len(requirements))
	})
}
