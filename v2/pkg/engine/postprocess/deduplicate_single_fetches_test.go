package postprocess

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

func TestDeduplicateSingleFetches_ProcessFetchTree(t *testing.T) {

	t.Run("no duplicates", func(t *testing.T) {
		input := &resolve.FetchTreeNode{
			ChildNodes: []*resolve.FetchTreeNode{
				{
					Kind: resolve.FetchTreeNodeKindSingle,
					Item: &resolve.FetchItem{
						FetchPath: nil,
						Fetch:     &resolve.SingleFetch{FetchConfiguration: resolve.FetchConfiguration{Input: "a"}},
					},
				},
				{
					Kind: resolve.FetchTreeNodeKindSingle,
					Item: &resolve.FetchItem{
						FetchPath: nil,
						Fetch:     &resolve.SingleFetch{FetchConfiguration: resolve.FetchConfiguration{Input: "b"}},
					},
				},
			},
		}

		dedup := &deduplicateSingleFetches{}
		dedup.ProcessFetchTree(input)
		assert.Equal(t, input, input)
	})

	t.Run("same path, same input", func(t *testing.T) {
		input := &resolve.FetchTreeNode{
			ChildNodes: []*resolve.FetchTreeNode{
				{
					Kind: resolve.FetchTreeNodeKindSingle,
					Item: &resolve.FetchItem{
						FetchPath: []resolve.FetchItemPathElement{{Kind: resolve.FetchItemPathElementKindObject, Path: []string{"a"}}},
						Fetch:     &resolve.SingleFetch{FetchConfiguration: resolve.FetchConfiguration{Input: "a"}},
					},
				},
				{
					Kind: resolve.FetchTreeNodeKindSingle,
					Item: &resolve.FetchItem{
						FetchPath: []resolve.FetchItemPathElement{{Kind: resolve.FetchItemPathElementKindObject, Path: []string{"a"}}},
						Fetch:     &resolve.SingleFetch{FetchConfiguration: resolve.FetchConfiguration{Input: "a"}},
					},
				},
			},
		}

		output := &resolve.FetchTreeNode{
			ChildNodes: []*resolve.FetchTreeNode{
				{
					Kind: resolve.FetchTreeNodeKindSingle,
					Item: &resolve.FetchItem{
						FetchPath: []resolve.FetchItemPathElement{{Kind: resolve.FetchItemPathElementKindObject, Path: []string{"a"}}},
						Fetch:     &resolve.SingleFetch{FetchConfiguration: resolve.FetchConfiguration{Input: "a"}},
					},
				},
			},
		}

		dedup := &deduplicateSingleFetches{}
		dedup.ProcessFetchTree(input)

		assert.Equal(t, output, input)
	})

	t.Run("same path, same input, different fetch id, different defer id", func(t *testing.T) {
		input := &resolve.FetchTreeNode{
			ChildNodes: []*resolve.FetchTreeNode{
				{
					Kind: resolve.FetchTreeNodeKindSingle,
					Item: &resolve.FetchItem{
						FetchPath: []resolve.FetchItemPathElement{{Kind: resolve.FetchItemPathElementKindObject, Path: []string{"a"}}},
						Fetch:     &resolve.SingleFetch{FetchConfiguration: resolve.FetchConfiguration{Input: "a"}, FetchDependencies: resolve.FetchDependencies{FetchID: 1, DeferID: 1}},
					},
				},
				{
					Kind: resolve.FetchTreeNodeKindSingle,
					Item: &resolve.FetchItem{
						FetchPath: []resolve.FetchItemPathElement{{Kind: resolve.FetchItemPathElementKindObject, Path: []string{"a"}}},
						Fetch:     &resolve.SingleFetch{FetchConfiguration: resolve.FetchConfiguration{Input: "a"}, FetchDependencies: resolve.FetchDependencies{FetchID: 2, DeferID: 2}},
					},
				},
			},
		}

		output := &resolve.FetchTreeNode{
			ChildNodes: []*resolve.FetchTreeNode{
				{
					Kind: resolve.FetchTreeNodeKindSingle,
					Item: &resolve.FetchItem{
						FetchPath: []resolve.FetchItemPathElement{{Kind: resolve.FetchItemPathElementKindObject, Path: []string{"a"}}},
						Fetch:     &resolve.SingleFetch{FetchConfiguration: resolve.FetchConfiguration{Input: "a"}, FetchDependencies: resolve.FetchDependencies{FetchID: 1, DeferID: 1}},
					},
				},
				{
					Kind: resolve.FetchTreeNodeKindSingle,
					Item: &resolve.FetchItem{
						FetchPath: []resolve.FetchItemPathElement{{Kind: resolve.FetchItemPathElementKindObject, Path: []string{"a"}}},
						Fetch:     &resolve.SingleFetch{FetchConfiguration: resolve.FetchConfiguration{Input: "a"}, FetchDependencies: resolve.FetchDependencies{FetchID: 2, DeferID: 2}},
					},
				},
			},
		}

		dedup := &deduplicateSingleFetches{}
		dedup.ProcessFetchTree(input)

		assert.Equal(t, output, input)
	})

	t.Run("same path, same input, different fetch id, same defer id", func(t *testing.T) {
		input := &resolve.FetchTreeNode{
			ChildNodes: []*resolve.FetchTreeNode{
				{
					Kind: resolve.FetchTreeNodeKindSingle,
					Item: &resolve.FetchItem{
						FetchPath: []resolve.FetchItemPathElement{{Kind: resolve.FetchItemPathElementKindObject, Path: []string{"a"}}},
						Fetch:     &resolve.SingleFetch{FetchConfiguration: resolve.FetchConfiguration{Input: "a"}, FetchDependencies: resolve.FetchDependencies{FetchID: 1, DeferID: 1}},
					},
				},
				{
					Kind: resolve.FetchTreeNodeKindSingle,
					Item: &resolve.FetchItem{
						FetchPath: []resolve.FetchItemPathElement{{Kind: resolve.FetchItemPathElementKindObject, Path: []string{"a"}}},
						Fetch:     &resolve.SingleFetch{FetchConfiguration: resolve.FetchConfiguration{Input: "a"}, FetchDependencies: resolve.FetchDependencies{FetchID: 2, DeferID: 1}},
					},
				},
			},
		}

		output := &resolve.FetchTreeNode{
			ChildNodes: []*resolve.FetchTreeNode{
				{
					Kind: resolve.FetchTreeNodeKindSingle,
					Item: &resolve.FetchItem{
						FetchPath: []resolve.FetchItemPathElement{{Kind: resolve.FetchItemPathElementKindObject, Path: []string{"a"}}},
						Fetch:     &resolve.SingleFetch{FetchConfiguration: resolve.FetchConfiguration{Input: "a"}, FetchDependencies: resolve.FetchDependencies{FetchID: 1, DeferID: 1}},
					},
				},
			},
		}

		dedup := &deduplicateSingleFetches{}
		dedup.ProcessFetchTree(input)

		assert.Equal(t, output, input)
	})

	t.Run("same path, same input, different fetch ids - should update dependencies with merged fetch ids", func(t *testing.T) {
		input := &resolve.FetchTreeNode{
			ChildNodes: []*resolve.FetchTreeNode{
				{
					Kind: resolve.FetchTreeNodeKindSingle,
					Item: &resolve.FetchItem{
						FetchPath: []resolve.FetchItemPathElement{{Kind: resolve.FetchItemPathElementKindObject, Path: []string{"root"}}},
						Fetch: &resolve.SingleFetch{
							FetchDependencies: resolve.FetchDependencies{
								FetchID:           0,
								DependsOnFetchIDs: []int{},
							},
							FetchConfiguration: resolve.FetchConfiguration{Input: "rootQuery"},
						},
					},
				},
				{
					Kind: resolve.FetchTreeNodeKindSingle,
					Item: &resolve.FetchItem{
						FetchPath: []resolve.FetchItemPathElement{{Kind: resolve.FetchItemPathElementKindObject, Path: []string{"root.a"}}},
						Fetch: &resolve.SingleFetch{
							FetchDependencies: resolve.FetchDependencies{
								FetchID:           1,
								DependsOnFetchIDs: []int{0},
							},
							FetchConfiguration: resolve.FetchConfiguration{
								Input: "a",
							},
							Info: &resolve.FetchInfo{
								CoordinateDependencies: []resolve.FetchDependency{
									{
										DependsOn: []resolve.FetchDependencyOrigin{
											{
												FetchID: 0,
											},
										},
									},
								},
							},
						},
					},
				},
				{
					Kind: resolve.FetchTreeNodeKindSingle,
					Item: &resolve.FetchItem{
						FetchPath: []resolve.FetchItemPathElement{{Kind: resolve.FetchItemPathElementKindObject, Path: []string{"root.a"}}},
						Fetch: &resolve.SingleFetch{
							FetchDependencies: resolve.FetchDependencies{
								FetchID:           2,
								DependsOnFetchIDs: []int{0},
							},
							FetchConfiguration: resolve.FetchConfiguration{
								Input: "a",
							},
							Info: &resolve.FetchInfo{
								CoordinateDependencies: []resolve.FetchDependency{
									{
										DependsOn: []resolve.FetchDependencyOrigin{
											{
												FetchID: 0,
											},
										},
									},
								},
							},
						},
					},
				},
				{
					Kind: resolve.FetchTreeNodeKindSingle,
					Item: &resolve.FetchItem{
						FetchPath: []resolve.FetchItemPathElement{{Kind: resolve.FetchItemPathElementKindObject, Path: []string{"root.a.b"}}},
						Fetch: &resolve.SingleFetch{
							FetchDependencies: resolve.FetchDependencies{
								FetchID:           4,
								DependsOnFetchIDs: []int{0, 2},
							},
							FetchConfiguration: resolve.FetchConfiguration{
								Input: "b",
							},
							Info: &resolve.FetchInfo{
								CoordinateDependencies: []resolve.FetchDependency{
									{
										DependsOn: []resolve.FetchDependencyOrigin{
											{
												FetchID: 0,
											},
											{
												FetchID: 2,
											},
										},
									},
								},
							},
						},
					},
				},
				{
					Kind: resolve.FetchTreeNodeKindSingle,
					Item: &resolve.FetchItem{
						FetchPath: []resolve.FetchItemPathElement{{Kind: resolve.FetchItemPathElementKindObject, Path: []string{"root.a.b"}}},
						Fetch: &resolve.SingleFetch{
							FetchDependencies: resolve.FetchDependencies{
								FetchID:           3,
								DependsOnFetchIDs: []int{0, 1},
							},
							FetchConfiguration: resolve.FetchConfiguration{
								Input: "b",
							},
							Info: &resolve.FetchInfo{
								CoordinateDependencies: []resolve.FetchDependency{
									{
										DependsOn: []resolve.FetchDependencyOrigin{
											{
												FetchID: 0,
											},
											{
												FetchID: 1,
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}

		output := &resolve.FetchTreeNode{
			ChildNodes: []*resolve.FetchTreeNode{
				{
					Kind: resolve.FetchTreeNodeKindSingle,
					Item: &resolve.FetchItem{
						FetchPath: []resolve.FetchItemPathElement{{Kind: resolve.FetchItemPathElementKindObject, Path: []string{"root"}}},
						Fetch: &resolve.SingleFetch{
							FetchDependencies: resolve.FetchDependencies{
								FetchID:           0,
								DependsOnFetchIDs: []int{},
							},
							FetchConfiguration: resolve.FetchConfiguration{Input: "rootQuery"},
						},
					},
				},
				{
					Kind: resolve.FetchTreeNodeKindSingle,
					Item: &resolve.FetchItem{
						FetchPath: []resolve.FetchItemPathElement{{Kind: resolve.FetchItemPathElementKindObject, Path: []string{"root.a"}}},
						Fetch: &resolve.SingleFetch{
							FetchDependencies: resolve.FetchDependencies{
								FetchID:           1,
								DependsOnFetchIDs: []int{0},
							},
							FetchConfiguration: resolve.FetchConfiguration{
								Input: "a",
							},
							Info: &resolve.FetchInfo{
								CoordinateDependencies: []resolve.FetchDependency{
									{
										DependsOn: []resolve.FetchDependencyOrigin{
											{
												FetchID: 0,
											},
										},
									},
								},
							},
						},
					},
				},
				{
					Kind: resolve.FetchTreeNodeKindSingle,
					Item: &resolve.FetchItem{
						FetchPath: []resolve.FetchItemPathElement{{Kind: resolve.FetchItemPathElementKindObject, Path: []string{"root.a.b"}}},
						Fetch: &resolve.SingleFetch{
							FetchDependencies: resolve.FetchDependencies{
								FetchID:           4,
								DependsOnFetchIDs: []int{0, 1},
							},
							FetchConfiguration: resolve.FetchConfiguration{
								Input: "b",
							},
							Info: &resolve.FetchInfo{
								CoordinateDependencies: []resolve.FetchDependency{
									{
										DependsOn: []resolve.FetchDependencyOrigin{
											{
												FetchID: 0,
											},
											{
												FetchID: 1,
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}

		dedup := &deduplicateSingleFetches{}
		dedup.ProcessFetchTree(input)

		assert.Equal(t, output, input)
	})

	t.Run("same path, same input, different dependencies - should keep the dependencies of both fetches", func(t *testing.T) {
		fetch := func(fetchID int, dependsOn []int, input string, coordinateDependencies []resolve.FetchDependency) *resolve.FetchTreeNode {
			return &resolve.FetchTreeNode{
				Kind: resolve.FetchTreeNodeKindSingle,
				Item: &resolve.FetchItem{
					FetchPath: []resolve.FetchItemPathElement{{Kind: resolve.FetchItemPathElementKindArray, Path: []string{"things", "users"}}},
					Fetch: &resolve.SingleFetch{
						FetchDependencies:  resolve.FetchDependencies{FetchID: fetchID, DependsOnFetchIDs: dependsOn},
						FetchConfiguration: resolve.FetchConfiguration{Input: input},
						Info:               &resolve.FetchInfo{CoordinateDependencies: coordinateDependencies},
					},
				},
			}
		}
		name := resolve.GraphCoordinate{TypeName: "User", FieldName: "name"}
		id := resolve.GraphCoordinate{TypeName: "User", FieldName: "id"}
		email := resolve.GraphCoordinate{TypeName: "User", FieldName: "email"}

		input := &resolve.FetchTreeNode{
			ChildNodes: []*resolve.FetchTreeNode{
				fetch(3, []int{1}, "names", []resolve.FetchDependency{
					{Coordinate: name, IsUserRequested: true, DependsOn: []resolve.FetchDependencyOrigin{{FetchID: 1, Subgraph: "a", Coordinate: id, IsKey: true}}},
				}),
				fetch(4, []int{2}, "names", []resolve.FetchDependency{
					{Coordinate: name, IsUserRequested: true, DependsOn: []resolve.FetchDependencyOrigin{{FetchID: 2, Subgraph: "b", Coordinate: id, IsKey: true}}},
					{Coordinate: email, IsUserRequested: true, DependsOn: []resolve.FetchDependencyOrigin{{FetchID: 2, Subgraph: "b", Coordinate: id, IsKey: true}}},
				}),
			},
		}

		output := &resolve.FetchTreeNode{
			ChildNodes: []*resolve.FetchTreeNode{
				fetch(3, []int{1, 2}, "names", []resolve.FetchDependency{
					{Coordinate: name, IsUserRequested: true, DependsOn: []resolve.FetchDependencyOrigin{
						{FetchID: 1, Subgraph: "a", Coordinate: id, IsKey: true},
						{FetchID: 2, Subgraph: "b", Coordinate: id, IsKey: true},
					}},
					{Coordinate: email, IsUserRequested: true, DependsOn: []resolve.FetchDependencyOrigin{{FetchID: 2, Subgraph: "b", Coordinate: id, IsKey: true}}},
				}),
			},
		}

		dedup := &deduplicateSingleFetches{}
		dedup.ProcessFetchTree(input)

		assert.Equal(t, output, input)
	})

	t.Run("different path, same input", func(t *testing.T) {
		input := &resolve.FetchTreeNode{
			ChildNodes: []*resolve.FetchTreeNode{
				{
					Kind: resolve.FetchTreeNodeKindSingle,
					Item: &resolve.FetchItem{
						FetchPath: []resolve.FetchItemPathElement{{Kind: resolve.FetchItemPathElementKindObject, Path: []string{"a"}}},
						Fetch:     &resolve.SingleFetch{FetchConfiguration: resolve.FetchConfiguration{Input: "a"}},
					},
				},
				{
					Kind: resolve.FetchTreeNodeKindSingle,
					Item: &resolve.FetchItem{
						FetchPath: []resolve.FetchItemPathElement{{Kind: resolve.FetchItemPathElementKindObject, Path: []string{"b"}}},
						Fetch:     &resolve.SingleFetch{FetchConfiguration: resolve.FetchConfiguration{Input: "a"}},
					},
				},
			},
		}

		output := &resolve.FetchTreeNode{
			ChildNodes: []*resolve.FetchTreeNode{
				{
					Kind: resolve.FetchTreeNodeKindSingle,
					Item: &resolve.FetchItem{
						FetchPath: []resolve.FetchItemPathElement{{Kind: resolve.FetchItemPathElementKindObject, Path: []string{"a"}}},
						Fetch:     &resolve.SingleFetch{FetchConfiguration: resolve.FetchConfiguration{Input: "a"}},
					},
				},
				{
					Kind: resolve.FetchTreeNodeKindSingle,
					Item: &resolve.FetchItem{
						FetchPath: []resolve.FetchItemPathElement{{Kind: resolve.FetchItemPathElementKindObject, Path: []string{"b"}}},
						Fetch:     &resolve.SingleFetch{FetchConfiguration: resolve.FetchConfiguration{Input: "a"}},
					},
				},
			},
		}

		dedup := &deduplicateSingleFetches{}
		dedup.ProcessFetchTree(input)

		assert.Equal(t, output, input)
	})

	contextualFetch := func(fetchID int, setterTypeName string) *resolve.FetchTreeNode {
		return &resolve.FetchTreeNode{
			Kind: resolve.FetchTreeNodeKindSingle,
			Item: &resolve.FetchItem{
				FetchPath: []resolve.FetchItemPathElement{{Kind: resolve.FetchItemPathElementKindArray, Path: []string{"queues"}}},
				Fetch: &resolve.SingleFetch{
					FetchDependencies: resolve.FetchDependencies{FetchID: fetchID},
					FetchConfiguration: resolve.FetchConfiguration{
						Input: "a",
						Variables: resolve.NewVariables(&resolve.ContextualArgumentVariable{
							Capture: resolve.ContextualArgumentCapture{
								VariableName: "contextualArgument_0",
								Levels: []resolve.ContextualArgumentLevel{
									{Depth: 1, Setters: []resolve.ContextualArgumentSetter{{TypeName: setterTypeName, Path: []string{"__context_0"}}}},
								},
							},
						}),
					},
				},
			},
		}
	}

	t.Run("same path, same input, same contextual argument capture", func(t *testing.T) {
		input := &resolve.FetchTreeNode{
			ChildNodes: []*resolve.FetchTreeNode{
				contextualFetch(1, "ModerationQueue"),
				contextualFetch(2, "ModerationQueue"),
			},
		}

		output := &resolve.FetchTreeNode{
			ChildNodes: []*resolve.FetchTreeNode{
				contextualFetch(1, "ModerationQueue"),
			},
		}

		dedup := &deduplicateSingleFetches{}
		dedup.ProcessFetchTree(input)

		assert.Equal(t, output, input)
	})

	t.Run("same path, same input, different contextual argument captures", func(t *testing.T) {
		input := &resolve.FetchTreeNode{
			ChildNodes: []*resolve.FetchTreeNode{
				contextualFetch(1, "ModerationQueue"),
				contextualFetch(2, "AppealQueue"),
			},
		}

		output := &resolve.FetchTreeNode{
			ChildNodes: []*resolve.FetchTreeNode{
				contextualFetch(1, "ModerationQueue"),
				contextualFetch(2, "AppealQueue"),
			},
		}

		dedup := &deduplicateSingleFetches{}
		dedup.ProcessFetchTree(input)

		assert.Equal(t, output, input)
	})
}
