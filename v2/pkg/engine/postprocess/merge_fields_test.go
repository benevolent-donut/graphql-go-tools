package postprocess

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

func TestMergeFields_Process(t *testing.T) {

	runTest := func(in, expected resolve.Node) func(t *testing.T) {
		return func(t *testing.T) {
			m := &mergeFields{}
			m.Process(in)
			assert.Equal(t, expected, in)
		}
	}

	t.Run("merge fields at the end of an object", runTest(
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name:  []byte(`a`),
					Value: &resolve.Integer{},
				},
				{
					Name:  []byte(`a`),
					Value: &resolve.Integer{},
					OnTypeNames: [][]byte{
						[]byte(`A`),
					},
				},
			},
		},
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name:  []byte(`a`),
					Value: &resolve.Integer{},
				},
			},
		},
	))

	t.Run("merge enum fields", runTest(
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name: []byte(`interfaceField`),
					Value: &resolve.Object{
						Fields: []*resolve.Field{
							{
								Name: []byte(`enumField`),
								Value: &resolve.Enum{
									Values:             []string{`a`},
									InaccessibleValues: []string{},
									TypeName:           `Enum`,
								},
							},
						},
					},
					OnTypeNames: [][]byte{[]byte(`A`), []byte(`B`)},
				},
				{
					Name: []byte(`interfaceField`),
					Value: &resolve.Object{
						Fields: []*resolve.Field{
							{
								Name:  []byte(`stringField`),
								Value: &resolve.String{},
							},
						},
					},
					OnTypeNames: [][]byte{[]byte(`A`), []byte(`B`)},
				},
			},
		},
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name: []byte(`interfaceField`),
					Value: &resolve.Object{
						Fields: []*resolve.Field{
							{
								Name: []byte(`enumField`),
								Value: &resolve.Enum{
									Values:             []string{`a`},
									InaccessibleValues: []string{},
									TypeName:           `Enum`,
								},
								ParentOnTypeNames: []resolve.ParentOnTypeNames{
									{
										Depth: 1,
										Names: [][]byte{[]byte(`A`)},
									},
								},
							},
							{
								Name:  []byte(`stringField`),
								Value: &resolve.String{},
								ParentOnTypeNames: []resolve.ParentOnTypeNames{
									{
										Depth: 1,
										Names: [][]byte{[]byte(`A`)},
									},
								},
							},
						},
					},
					OnTypeNames: [][]byte{[]byte(`A`)},
				},
				{
					Name: []byte(`interfaceField`),
					Value: &resolve.Object{
						Fields: []*resolve.Field{
							{
								Name: []byte(`enumField`),
								Value: &resolve.Enum{
									Values:             []string{`a`},
									InaccessibleValues: []string{},
									TypeName:           `Enum`,
								},
								ParentOnTypeNames: []resolve.ParentOnTypeNames{
									{
										Depth: 1,
										Names: [][]byte{[]byte(`B`)},
									},
								},
							},
							{
								Name:  []byte(`stringField`),
								Value: &resolve.String{},
								ParentOnTypeNames: []resolve.ParentOnTypeNames{
									{
										Depth: 1,
										Names: [][]byte{[]byte(`B`)},
									},
								},
							},
						},
					},
					OnTypeNames: [][]byte{[]byte(`B`)},
				},
			},
		},
	))

	t.Run("merge fields at the end of an object reverse", runTest(
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name:  []byte(`a`),
					Value: &resolve.Integer{},
					OnTypeNames: [][]byte{
						[]byte(`A`),
					},
				},
				{
					Name:  []byte(`a`),
					Value: &resolve.Integer{},
					OnTypeNames: [][]byte{
						[]byte(`A`),
					},
				},
				{
					Name:  []byte(`a`),
					Value: &resolve.Integer{},
				},
			},
		},
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name:  []byte(`a`),
					Value: &resolve.Integer{},
				},
			},
		},
	))

	t.Run("merge fields at the end of an object nested", runTest(
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name: []byte(`a`),
					Value: &resolve.Object{
						Fields: []*resolve.Field{
							{
								Name:  []byte(`b`),
								Value: &resolve.Integer{},
							},
						},
					},
					OnTypeNames: [][]byte{
						[]byte(`A`),
					},
				},
				{
					Name: []byte(`a`),
					Value: &resolve.Object{
						Fields: []*resolve.Field{
							{
								Name:  []byte(`b`),
								Value: &resolve.Integer{},
							},
						},
					},
				},
			},
		},
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name: []byte(`a`),
					Value: &resolve.Object{
						Fields: []*resolve.Field{
							{
								Name:  []byte(`b`),
								Value: &resolve.Integer{},
							},
						},
					},
				},
			},
		},
	))

	t.Run("merge fields at the end of an object nested reverse", runTest(
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name: []byte(`a`),
					Value: &resolve.Object{
						Fields: []*resolve.Field{
							{
								Name:  []byte(`b`),
								Value: &resolve.Integer{},
							},
						},
					},
				},
				{
					Name: []byte(`a`),
					Value: &resolve.Object{
						Fields: []*resolve.Field{
							{
								Name:  []byte(`b`),
								Value: &resolve.Integer{},
							},
						},
					},
					OnTypeNames: [][]byte{
						[]byte(`A`),
					},
				},
			},
		},
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name: []byte(`a`),
					Value: &resolve.Object{
						Fields: []*resolve.Field{
							{
								Name:  []byte(`b`),
								Value: &resolve.Integer{},
							},
						},
					},
				},
			},
		},
	))

	t.Run("keep object fields of sibling type conditions apart below a field without type conditions", runTest(
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name: []byte(`flaggedUsers`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name:  []byte(`id`),
									Value: &resolve.String{},
								},
							},
						},
					},
				},
				{
					Name: []byte(`flaggedUsers`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name: []byte(`friends`),
									Value: &resolve.Array{
										Item: &resolve.Object{
											Fields: []*resolve.Field{
												{
													Name:  []byte(`id`),
													Value: &resolve.String{},
												},
											},
										},
									},
								},
								{
									Name:  []byte(`name`),
									Value: &resolve.String{},
								},
							},
						},
					},
					OnTypeNames: [][]byte{[]byte(`ModerationQueue`)},
				},
				{
					Name: []byte(`flaggedUsers`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name: []byte(`friends`),
									Value: &resolve.Array{
										Item: &resolve.Object{
											Fields: []*resolve.Field{
												{
													Name:  []byte(`id`),
													Value: &resolve.String{},
												},
											},
										},
									},
								},
								{
									Name:  []byte(`name`),
									Value: &resolve.String{},
								},
							},
						},
					},
					OnTypeNames: [][]byte{[]byte(`AppealQueue`)},
				},
			},
		},
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name: []byte(`flaggedUsers`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name:  []byte(`id`),
									Value: &resolve.String{},
								},
								{
									Name: []byte(`friends`),
									Value: &resolve.Array{
										Item: &resolve.Object{
											Fields: []*resolve.Field{
												{
													Name:  []byte(`id`),
													Value: &resolve.String{},
													ParentOnTypeNames: []resolve.ParentOnTypeNames{
														{
															Depth: 2,
															Names: [][]byte{[]byte(`ModerationQueue`)},
														},
													},
												},
											},
										},
									},
									ParentOnTypeNames: []resolve.ParentOnTypeNames{
										{
											Depth: 1,
											Names: [][]byte{[]byte(`ModerationQueue`)},
										},
									},
								},
								{
									Name:  []byte(`name`),
									Value: &resolve.String{},
									ParentOnTypeNames: []resolve.ParentOnTypeNames{
										{
											Depth: 1,
											Names: [][]byte{[]byte(`ModerationQueue`)},
										},
									},
								},
								{
									Name: []byte(`friends`),
									Value: &resolve.Array{
										Item: &resolve.Object{
											Fields: []*resolve.Field{
												{
													Name:  []byte(`id`),
													Value: &resolve.String{},
													ParentOnTypeNames: []resolve.ParentOnTypeNames{
														{
															Depth: 2,
															Names: [][]byte{[]byte(`AppealQueue`)},
														},
													},
												},
											},
										},
									},
									ParentOnTypeNames: []resolve.ParentOnTypeNames{
										{
											Depth: 1,
											Names: [][]byte{[]byte(`AppealQueue`)},
										},
									},
								},
								{
									Name:  []byte(`name`),
									Value: &resolve.String{},
									ParentOnTypeNames: []resolve.ParentOnTypeNames{
										{
											Depth: 1,
											Names: [][]byte{[]byte(`AppealQueue`)},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	))

	t.Run("keep nested object fields of sibling type conditions apart", runTest(
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name: []byte(`flaggedUsers`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name:  []byte(`id`),
									Value: &resolve.String{},
								},
							},
						},
					},
				},
				{
					Name: []byte(`flaggedUsers`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name: []byte(`friends`),
									Value: &resolve.Array{
										Item: &resolve.Object{
											Fields: []*resolve.Field{
												{
													Name: []byte(`friends`),
													Value: &resolve.Array{
														Item: &resolve.Object{
															Fields: []*resolve.Field{
																{
																	Name:  []byte(`id`),
																	Value: &resolve.String{},
																},
															},
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
					OnTypeNames: [][]byte{[]byte(`ModerationQueue`)},
				},
				{
					Name: []byte(`flaggedUsers`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name: []byte(`friends`),
									Value: &resolve.Array{
										Item: &resolve.Object{
											Fields: []*resolve.Field{
												{
													Name: []byte(`friends`),
													Value: &resolve.Array{
														Item: &resolve.Object{
															Fields: []*resolve.Field{
																{
																	Name:  []byte(`name`),
																	Value: &resolve.String{},
																},
															},
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
					OnTypeNames: [][]byte{[]byte(`AppealQueue`)},
				},
			},
		},
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name: []byte(`flaggedUsers`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name:  []byte(`id`),
									Value: &resolve.String{},
								},
								{
									Name: []byte(`friends`),
									Value: &resolve.Array{
										Item: &resolve.Object{
											Fields: []*resolve.Field{
												{
													Name: []byte(`friends`),
													Value: &resolve.Array{
														Item: &resolve.Object{
															Fields: []*resolve.Field{
																{
																	Name:  []byte(`id`),
																	Value: &resolve.String{},
																	ParentOnTypeNames: []resolve.ParentOnTypeNames{
																		{
																			Depth: 3,
																			Names: [][]byte{[]byte(`ModerationQueue`)},
																		},
																	},
																},
															},
														},
													},
													ParentOnTypeNames: []resolve.ParentOnTypeNames{
														{
															Depth: 2,
															Names: [][]byte{[]byte(`ModerationQueue`)},
														},
													},
												},
											},
										},
									},
									ParentOnTypeNames: []resolve.ParentOnTypeNames{
										{
											Depth: 1,
											Names: [][]byte{[]byte(`ModerationQueue`)},
										},
									},
								},
								{
									Name: []byte(`friends`),
									Value: &resolve.Array{
										Item: &resolve.Object{
											Fields: []*resolve.Field{
												{
													Name: []byte(`friends`),
													Value: &resolve.Array{
														Item: &resolve.Object{
															Fields: []*resolve.Field{
																{
																	Name:  []byte(`name`),
																	Value: &resolve.String{},
																	ParentOnTypeNames: []resolve.ParentOnTypeNames{
																		{
																			Depth: 3,
																			Names: [][]byte{[]byte(`AppealQueue`)},
																		},
																	},
																},
															},
														},
													},
													ParentOnTypeNames: []resolve.ParentOnTypeNames{
														{
															Depth: 2,
															Names: [][]byte{[]byte(`AppealQueue`)},
														},
													},
												},
											},
										},
									},
									ParentOnTypeNames: []resolve.ParentOnTypeNames{
										{
											Depth: 1,
											Names: [][]byte{[]byte(`AppealQueue`)},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	))

	t.Run("keep object fields of union members of sibling type conditions apart", runTest(
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name: []byte(`flagged`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name:        []byte(`id`),
									Value:       &resolve.String{},
									OnTypeNames: [][]byte{[]byte(`Bot`)},
								},
							},
						},
					},
				},
				{
					Name: []byte(`flagged`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name: []byte(`friends`),
									Value: &resolve.Array{
										Item: &resolve.Object{
											Fields: []*resolve.Field{
												{
													Name:  []byte(`id`),
													Value: &resolve.String{},
												},
											},
										},
									},
									OnTypeNames: [][]byte{[]byte(`User`)},
								},
							},
						},
					},
					OnTypeNames: [][]byte{[]byte(`ModerationQueue`)},
				},
				{
					Name: []byte(`flagged`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name: []byte(`friends`),
									Value: &resolve.Array{
										Item: &resolve.Object{
											Fields: []*resolve.Field{
												{
													Name:  []byte(`id`),
													Value: &resolve.String{},
												},
											},
										},
									},
									OnTypeNames: [][]byte{[]byte(`User`)},
								},
							},
						},
					},
					OnTypeNames: [][]byte{[]byte(`AppealQueue`)},
				},
			},
		},
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name: []byte(`flagged`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name:        []byte(`id`),
									Value:       &resolve.String{},
									OnTypeNames: [][]byte{[]byte(`Bot`)},
								},
								{
									Name: []byte(`friends`),
									Value: &resolve.Array{
										Item: &resolve.Object{
											Fields: []*resolve.Field{
												{
													Name:  []byte(`id`),
													Value: &resolve.String{},
													ParentOnTypeNames: []resolve.ParentOnTypeNames{
														{
															Depth: 2,
															Names: [][]byte{[]byte(`ModerationQueue`)},
														},
														{
															Depth: 1,
															Names: [][]byte{[]byte(`User`)},
														},
													},
												},
											},
										},
									},
									OnTypeNames: [][]byte{[]byte(`User`)},
									ParentOnTypeNames: []resolve.ParentOnTypeNames{
										{
											Depth: 1,
											Names: [][]byte{[]byte(`ModerationQueue`)},
										},
									},
								},
								{
									Name: []byte(`friends`),
									Value: &resolve.Array{
										Item: &resolve.Object{
											Fields: []*resolve.Field{
												{
													Name:  []byte(`id`),
													Value: &resolve.String{},
													ParentOnTypeNames: []resolve.ParentOnTypeNames{
														{
															Depth: 2,
															Names: [][]byte{[]byte(`AppealQueue`)},
														},
														{
															Depth: 1,
															Names: [][]byte{[]byte(`User`)},
														},
													},
												},
											},
										},
									},
									OnTypeNames: [][]byte{[]byte(`User`)},
									ParentOnTypeNames: []resolve.ParentOnTypeNames{
										{
											Depth: 1,
											Names: [][]byte{[]byte(`AppealQueue`)},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	))

	t.Run("merge a scalar field of a union member with and without a parent type condition", runTest(
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name: []byte(`flagged`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name:        []byte(`id`),
									Value:       &resolve.String{},
									OnTypeNames: [][]byte{[]byte(`Bot`)},
								},
							},
						},
					},
				},
				{
					Name: []byte(`flagged`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name:        []byte(`id`),
									Value:       &resolve.String{},
									OnTypeNames: [][]byte{[]byte(`Bot`)},
								},
							},
						},
					},
					OnTypeNames: [][]byte{[]byte(`AppealQueue`)},
				},
			},
		},
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name: []byte(`flagged`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name:        []byte(`id`),
									Value:       &resolve.String{},
									OnTypeNames: [][]byte{[]byte(`Bot`)},
								},
							},
						},
					},
				},
			},
		},
	))

	t.Run("keep object fields apart whose parent type conditions differ at two depths", runTest(
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name: []byte(`queues`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name: []byte(`flaggedUsers`),
									Value: &resolve.Array{
										Item: &resolve.Object{
											Fields: []*resolve.Field{
												{
													Name:  []byte(`id`),
													Value: &resolve.String{},
												},
											},
										},
									},
								},
							},
						},
					},
				},
				{
					Name: []byte(`queues`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name: []byte(`flaggedUsers`),
									Value: &resolve.Array{
										Item: &resolve.Object{
											Fields: []*resolve.Field{
												{
													Name: []byte(`friends`),
													Value: &resolve.Array{
														Item: &resolve.Object{
															Fields: []*resolve.Field{
																{
																	Name:  []byte(`id`),
																	Value: &resolve.String{},
																},
															},
														},
													},
												},
											},
										},
									},
									OnTypeNames: [][]byte{[]byte(`ModerationQueue`)},
								},
							},
						},
					},
					OnTypeNames: [][]byte{[]byte(`Guild`)},
				},
				{
					Name: []byte(`queues`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name: []byte(`flaggedUsers`),
									Value: &resolve.Array{
										Item: &resolve.Object{
											Fields: []*resolve.Field{
												{
													Name: []byte(`friends`),
													Value: &resolve.Array{
														Item: &resolve.Object{
															Fields: []*resolve.Field{
																{
																	Name:  []byte(`id`),
																	Value: &resolve.String{},
																},
															},
														},
													},
												},
											},
										},
									},
									OnTypeNames: [][]byte{[]byte(`AppealQueue`)},
								},
							},
						},
					},
					OnTypeNames: [][]byte{[]byte(`Channel`)},
				},
			},
		},
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name: []byte(`queues`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name: []byte(`flaggedUsers`),
									Value: &resolve.Array{
										Item: &resolve.Object{
											Fields: []*resolve.Field{
												{
													Name:  []byte(`id`),
													Value: &resolve.String{},
												},
												{
													Name: []byte(`friends`),
													Value: &resolve.Array{
														Item: &resolve.Object{
															Fields: []*resolve.Field{
																{
																	Name:  []byte(`id`),
																	Value: &resolve.String{},
																	ParentOnTypeNames: []resolve.ParentOnTypeNames{
																		{
																			Depth: 3,
																			Names: [][]byte{[]byte(`Guild`)},
																		},
																		{
																			Depth: 2,
																			Names: [][]byte{[]byte(`ModerationQueue`)},
																		},
																	},
																},
															},
														},
													},
													ParentOnTypeNames: []resolve.ParentOnTypeNames{
														{
															Depth: 2,
															Names: [][]byte{[]byte(`Guild`)},
														},
														{
															Depth: 1,
															Names: [][]byte{[]byte(`ModerationQueue`)},
														},
													},
												},
												{
													Name: []byte(`friends`),
													Value: &resolve.Array{
														Item: &resolve.Object{
															Fields: []*resolve.Field{
																{
																	Name:  []byte(`id`),
																	Value: &resolve.String{},
																	ParentOnTypeNames: []resolve.ParentOnTypeNames{
																		{
																			Depth: 3,
																			Names: [][]byte{[]byte(`Channel`)},
																		},
																		{
																			Depth: 2,
																			Names: [][]byte{[]byte(`AppealQueue`)},
																		},
																	},
																},
															},
														},
													},
													ParentOnTypeNames: []resolve.ParentOnTypeNames{
														{
															Depth: 2,
															Names: [][]byte{[]byte(`Channel`)},
														},
														{
															Depth: 1,
															Names: [][]byte{[]byte(`AppealQueue`)},
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	))

	t.Run("keep fields without type conditions apart from fields whose parent type conditions exclude theirs", runTest(
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name: []byte(`queues`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name:  []byte(`id`),
									Value: &resolve.String{},
								},
							},
						},
					},
				},
				{
					Name: []byte(`queues`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name: []byte(`flaggedUsers`),
									Value: &resolve.Array{
										Item: &resolve.Object{
											Fields: []*resolve.Field{
												{
													Name:  []byte(`id`),
													Value: &resolve.String{},
												},
											},
										},
									},
								},
								{
									Name:  []byte(`name`),
									Value: &resolve.String{},
								},
							},
						},
					},
					OnTypeNames: [][]byte{[]byte(`Guild`)},
				},
				{
					Name: []byte(`queues`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name: []byte(`flaggedUsers`),
									Value: &resolve.Array{
										Item: &resolve.Object{
											Fields: []*resolve.Field{
												{
													Name:  []byte(`name`),
													Value: &resolve.String{},
												},
											},
										},
									},
									OnTypeNames: [][]byte{[]byte(`ModerationQueue`)},
								},
								{
									Name:        []byte(`name`),
									Value:       &resolve.String{},
									OnTypeNames: [][]byte{[]byte(`ModerationQueue`)},
								},
							},
						},
					},
					OnTypeNames: [][]byte{[]byte(`Channel`)},
				},
			},
		},
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name: []byte(`queues`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name:  []byte(`id`),
									Value: &resolve.String{},
								},
								{
									Name: []byte(`flaggedUsers`),
									Value: &resolve.Array{
										Item: &resolve.Object{
											Fields: []*resolve.Field{
												{
													Name:  []byte(`id`),
													Value: &resolve.String{},
													ParentOnTypeNames: []resolve.ParentOnTypeNames{
														{
															Depth: 2,
															Names: [][]byte{[]byte(`Guild`)},
														},
													},
												},
											},
										},
									},
									ParentOnTypeNames: []resolve.ParentOnTypeNames{
										{
											Depth: 1,
											Names: [][]byte{[]byte(`Guild`)},
										},
									},
								},
								{
									Name:  []byte(`name`),
									Value: &resolve.String{},
									ParentOnTypeNames: []resolve.ParentOnTypeNames{
										{
											Depth: 1,
											Names: [][]byte{[]byte(`Guild`)},
										},
									},
								},
								{
									Name: []byte(`flaggedUsers`),
									Value: &resolve.Array{
										Item: &resolve.Object{
											Fields: []*resolve.Field{
												{
													Name:  []byte(`name`),
													Value: &resolve.String{},
													ParentOnTypeNames: []resolve.ParentOnTypeNames{
														{
															Depth: 2,
															Names: [][]byte{[]byte(`Channel`)},
														},
														{
															Depth: 1,
															Names: [][]byte{[]byte(`ModerationQueue`)},
														},
													},
												},
											},
										},
									},
									OnTypeNames: [][]byte{[]byte(`ModerationQueue`)},
									ParentOnTypeNames: []resolve.ParentOnTypeNames{
										{
											Depth: 1,
											Names: [][]byte{[]byte(`Channel`)},
										},
									},
								},
								{
									Name:        []byte(`name`),
									Value:       &resolve.String{},
									OnTypeNames: [][]byte{[]byte(`ModerationQueue`)},
									ParentOnTypeNames: []resolve.ParentOnTypeNames{
										{
											Depth: 1,
											Names: [][]byte{[]byte(`Channel`)},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	))

	t.Run("merge object fields whose parent type conditions include those of the other", runTest(
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name: []byte(`queues`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name: []byte(`flaggedUsers`),
									Value: &resolve.Array{
										Item: &resolve.Object{
											Fields: []*resolve.Field{
												{
													Name:  []byte(`id`),
													Value: &resolve.String{},
												},
											},
										},
									},
								},
								{
									Name: []byte(`flaggedUsers`),
									Value: &resolve.Array{
										Item: &resolve.Object{
											Fields: []*resolve.Field{
												{
													Name: []byte(`friends`),
													Value: &resolve.Array{
														Item: &resolve.Object{
															Fields: []*resolve.Field{
																{
																	Name:  []byte(`id`),
																	Value: &resolve.String{},
																},
															},
														},
													},
												},
											},
										},
									},
									OnTypeNames: [][]byte{[]byte(`ModerationQueue`)},
								},
							},
						},
					},
				},
				{
					Name: []byte(`queues`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name: []byte(`flaggedUsers`),
									Value: &resolve.Array{
										Item: &resolve.Object{
											Fields: []*resolve.Field{
												{
													Name: []byte(`friends`),
													Value: &resolve.Array{
														Item: &resolve.Object{
															Fields: []*resolve.Field{
																{
																	Name:  []byte(`name`),
																	Value: &resolve.String{},
																},
															},
														},
													},
												},
											},
										},
									},
									OnTypeNames: [][]byte{[]byte(`ModerationQueue`)},
								},
							},
						},
					},
					OnTypeNames: [][]byte{[]byte(`Guild`)},
				},
			},
		},
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name: []byte(`queues`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name: []byte(`flaggedUsers`),
									Value: &resolve.Array{
										Item: &resolve.Object{
											Fields: []*resolve.Field{
												{
													Name:  []byte(`id`),
													Value: &resolve.String{},
												},
												{
													Name: []byte(`friends`),
													Value: &resolve.Array{
														Item: &resolve.Object{
															Fields: []*resolve.Field{
																{
																	Name:  []byte(`id`),
																	Value: &resolve.String{},
																	ParentOnTypeNames: []resolve.ParentOnTypeNames{
																		{
																			Depth: 2,
																			Names: [][]byte{[]byte(`ModerationQueue`)},
																		},
																	},
																},
																{
																	Name:  []byte(`name`),
																	Value: &resolve.String{},
																	ParentOnTypeNames: []resolve.ParentOnTypeNames{
																		{
																			Depth: 3,
																			Names: [][]byte{[]byte(`Guild`)},
																		},
																		{
																			Depth: 2,
																			Names: [][]byte{[]byte(`ModerationQueue`)},
																		},
																	},
																},
															},
														},
													},
													ParentOnTypeNames: []resolve.ParentOnTypeNames{
														{
															Depth: 1,
															Names: [][]byte{[]byte(`ModerationQueue`)},
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	))

	t.Run("keep the parent type conditions and defer of fields duplicated for multiple onTypeNames", runTest(
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name: []byte(`reviewer`),
					Value: &resolve.Object{
						Fields: []*resolve.Field{
							{
								Name:  []byte(`id`),
								Value: &resolve.String{},
								ParentOnTypeNames: []resolve.ParentOnTypeNames{
									{
										Depth: 2,
										Names: [][]byte{[]byte(`Guild`)},
									},
								},
							},
						},
					},
					Defer:       &resolve.DeferField{DeferID: 1},
					OnTypeNames: [][]byte{[]byte(`ModerationQueue`), []byte(`AppealQueue`)},
					ParentOnTypeNames: []resolve.ParentOnTypeNames{
						{
							Depth: 1,
							Names: [][]byte{[]byte(`Guild`)},
						},
					},
				},
				{
					Name:  []byte(`name`),
					Value: &resolve.String{},
				},
			},
		},
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name: []byte(`reviewer`),
					Value: &resolve.Object{
						Fields: []*resolve.Field{
							{
								Name:  []byte(`id`),
								Value: &resolve.String{},
								ParentOnTypeNames: []resolve.ParentOnTypeNames{
									{
										Depth: 2,
										Names: [][]byte{[]byte(`Guild`)},
									},
									{
										Depth: 1,
										Names: [][]byte{[]byte(`ModerationQueue`)},
									},
								},
							},
						},
					},
					Defer:       &resolve.DeferField{DeferID: 1},
					OnTypeNames: [][]byte{[]byte(`ModerationQueue`)},
					ParentOnTypeNames: []resolve.ParentOnTypeNames{
						{
							Depth: 1,
							Names: [][]byte{[]byte(`Guild`)},
						},
					},
				},
				{
					Name: []byte(`reviewer`),
					Value: &resolve.Object{
						Fields: []*resolve.Field{
							{
								Name:  []byte(`id`),
								Value: &resolve.String{},
								ParentOnTypeNames: []resolve.ParentOnTypeNames{
									{
										Depth: 2,
										Names: [][]byte{[]byte(`Guild`)},
									},
									{
										Depth: 1,
										Names: [][]byte{[]byte(`AppealQueue`)},
									},
								},
							},
						},
					},
					Defer:       &resolve.DeferField{DeferID: 1},
					OnTypeNames: [][]byte{[]byte(`AppealQueue`)},
					ParentOnTypeNames: []resolve.ParentOnTypeNames{
						{
							Depth: 1,
							Names: [][]byte{[]byte(`Guild`)},
						},
					},
				},
				{
					Name:  []byte(`name`),
					Value: &resolve.String{},
				},
			},
		},
	))

	t.Run("merge object fields that overlap once a merged field is resolved below more parents", runTest(
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name: []byte(`friends`),
					Value: &resolve.Object{
						Fields: []*resolve.Field{
							{
								Name:  []byte(`name`),
								Value: &resolve.String{},
							},
						},
					},
					ParentOnTypeNames: []resolve.ParentOnTypeNames{
						{
							Depth: 2,
							Names: [][]byte{[]byte(`Guild`)},
						},
						{
							Depth: 1,
							Names: [][]byte{[]byte(`ModerationQueue`)},
						},
					},
				},
				{
					Name: []byte(`friends`),
					Value: &resolve.Object{
						Fields: []*resolve.Field{
							{
								Name:  []byte(`id`),
								Value: &resolve.String{},
							},
						},
					},
					ParentOnTypeNames: []resolve.ParentOnTypeNames{
						{
							Depth: 2,
							Names: [][]byte{[]byte(`Channel`)},
						},
						{
							Depth: 1,
							Names: [][]byte{[]byte(`ModerationQueue`)},
						},
					},
				},
				{
					Name: []byte(`friends`),
					Value: &resolve.Object{
						Fields: []*resolve.Field{
							{
								Name:  []byte(`__typename`),
								Value: &resolve.String{},
							},
						},
					},
					ParentOnTypeNames: []resolve.ParentOnTypeNames{
						{
							Depth: 1,
							Names: [][]byte{[]byte(`ModerationQueue`)},
						},
					},
				},
			},
		},
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name: []byte(`friends`),
					Value: &resolve.Object{
						Fields: []*resolve.Field{
							{
								Name:  []byte(`name`),
								Value: &resolve.String{},
							},
							{
								Name:  []byte(`__typename`),
								Value: &resolve.String{},
							},
							{
								Name:  []byte(`id`),
								Value: &resolve.String{},
							},
						},
					},
					ParentOnTypeNames: []resolve.ParentOnTypeNames{
						{
							Depth: 1,
							Names: [][]byte{[]byte(`ModerationQueue`)},
						},
					},
				},
			},
		},
	))

	t.Run("merge a scalar field into a scalar field whose parent type conditions include its own", runTest(
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name:  []byte(`id`),
					Value: &resolve.String{},
					ParentOnTypeNames: []resolve.ParentOnTypeNames{
						{
							Depth: 2,
							Names: [][]byte{[]byte(`Channel`)},
						},
					},
				},
				{
					Name:  []byte(`id`),
					Value: &resolve.String{},
					ParentOnTypeNames: []resolve.ParentOnTypeNames{
						{
							Depth: 2,
							Names: [][]byte{[]byte(`Channel`)},
						},
						{
							Depth: 1,
							Names: [][]byte{[]byte(`AppealQueue`)},
						},
					},
				},
			},
		},
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name:  []byte(`id`),
					Value: &resolve.String{},
					ParentOnTypeNames: []resolve.ParentOnTypeNames{
						{
							Depth: 2,
							Names: [][]byte{[]byte(`Channel`)},
						},
					},
				},
			},
		},
	))

	t.Run("keep scalar fields apart whose parent type conditions exclude each other at two depths", runTest(
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name:  []byte(`name`),
					Value: &resolve.String{},
					ParentOnTypeNames: []resolve.ParentOnTypeNames{
						{
							Depth: 2,
							Names: [][]byte{[]byte(`Guild`)},
						},
						{
							Depth: 1,
							Names: [][]byte{[]byte(`ModerationQueue`)},
						},
					},
				},
				{
					Name:  []byte(`name`),
					Value: &resolve.String{},
					ParentOnTypeNames: []resolve.ParentOnTypeNames{
						{
							Depth: 2,
							Names: [][]byte{[]byte(`Channel`)},
						},
						{
							Depth: 1,
							Names: [][]byte{[]byte(`AppealQueue`)},
						},
					},
				},
				{
					Name:        []byte(`name`),
					Value:       &resolve.String{},
					OnTypeNames: [][]byte{[]byte(`User`)},
					ParentOnTypeNames: []resolve.ParentOnTypeNames{
						{
							Depth: 2,
							Names: [][]byte{[]byte(`Guild`)},
						},
						{
							Depth: 1,
							Names: [][]byte{[]byte(`AppealQueue`)},
						},
					},
				},
			},
		},
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name:  []byte(`name`),
					Value: &resolve.String{},
					ParentOnTypeNames: []resolve.ParentOnTypeNames{
						{
							Depth: 2,
							Names: [][]byte{[]byte(`Guild`)},
						},
						{
							Depth: 1,
							Names: [][]byte{[]byte(`ModerationQueue`)},
						},
					},
				},
				{
					Name:  []byte(`name`),
					Value: &resolve.String{},
					ParentOnTypeNames: []resolve.ParentOnTypeNames{
						{
							Depth: 2,
							Names: [][]byte{[]byte(`Channel`)},
						},
						{
							Depth: 1,
							Names: [][]byte{[]byte(`AppealQueue`)},
						},
					},
				},
				{
					Name:        []byte(`name`),
					Value:       &resolve.String{},
					OnTypeNames: [][]byte{[]byte(`User`)},
					ParentOnTypeNames: []resolve.ParentOnTypeNames{
						{
							Depth: 2,
							Names: [][]byte{[]byte(`Guild`)},
						},
						{
							Depth: 1,
							Names: [][]byte{[]byte(`AppealQueue`)},
						},
					},
				},
			},
		},
	))

	t.Run("keep fields of other defer scopes apart from deferred fields", runTest(
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name: []byte(`flaggedUsers`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name:  []byte(`name`),
									Value: &resolve.String{},
									Defer: &resolve.DeferField{DeferID: 1},
								},
							},
						},
					},
					Defer: &resolve.DeferField{DeferID: 1},
				},
				{
					Name: []byte(`flaggedUsers`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name:  []byte(`id`),
									Value: &resolve.String{},
								},
							},
						},
					},
					OnTypeNames: [][]byte{[]byte(`AppealQueue`)},
				},
				{
					Name:  []byte(`id`),
					Value: &resolve.String{},
					ParentOnTypeNames: []resolve.ParentOnTypeNames{
						{
							Depth: 1,
							Names: [][]byte{[]byte(`Guild`)},
						},
					},
				},
				{
					Name:  []byte(`id`),
					Value: &resolve.String{},
					Defer: &resolve.DeferField{DeferID: 1},
					ParentOnTypeNames: []resolve.ParentOnTypeNames{
						{
							Depth: 1,
							Names: [][]byte{[]byte(`Channel`)},
						},
					},
				},
			},
		},
		&resolve.Object{
			Fields: []*resolve.Field{
				{
					Name: []byte(`flaggedUsers`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name:  []byte(`name`),
									Value: &resolve.String{},
									Defer: &resolve.DeferField{DeferID: 1},
								},
							},
						},
					},
					Defer: &resolve.DeferField{DeferID: 1},
				},
				{
					Name: []byte(`flaggedUsers`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name:  []byte(`id`),
									Value: &resolve.String{},
									ParentOnTypeNames: []resolve.ParentOnTypeNames{
										{
											Depth: 1,
											Names: [][]byte{[]byte(`AppealQueue`)},
										},
									},
								},
							},
						},
					},
					OnTypeNames: [][]byte{[]byte(`AppealQueue`)},
				},
				{
					Name:  []byte(`id`),
					Value: &resolve.String{},
					ParentOnTypeNames: []resolve.ParentOnTypeNames{
						{
							Depth: 1,
							Names: [][]byte{[]byte(`Guild`)},
						},
					},
				},
				{
					Name:  []byte(`id`),
					Value: &resolve.String{},
					Defer: &resolve.DeferField{DeferID: 1},
					ParentOnTypeNames: []resolve.ParentOnTypeNames{
						{
							Depth: 1,
							Names: [][]byte{[]byte(`Channel`)},
						},
					},
				},
			},
		},
	))

	t.Run("merge fields of nested defer scopes into deferred fields", func(t *testing.T) {
		flaggedUsers := func(deferID int, onTypeNames [][]byte, child string) *resolve.Field {
			return &resolve.Field{
				Name: []byte(`flaggedUsers`),
				Value: &resolve.Array{
					Item: &resolve.Object{
						Fields: []*resolve.Field{
							{
								Name:  []byte(child),
								Value: &resolve.String{},
								Defer: &resolve.DeferField{DeferID: deferID},
							},
						},
					},
				},
				Defer:       &resolve.DeferField{DeferID: deferID},
				OnTypeNames: onTypeNames,
			}
		}
		in := &resolve.Object{
			Fields: []*resolve.Field{
				flaggedUsers(1, nil, `name`),
				flaggedUsers(2, [][]byte{[]byte(`AppealQueue`)}, `id`),
				flaggedUsers(3, [][]byte{[]byte(`AppealQueue`)}, `__typename`),
			},
		}
		m := &mergeFields{}
		m.ProcessDefer(in, map[int]resolve.DeferDescriptor{
			1: {ID: 1},
			2: {ID: 2, ParentID: 1},
			3: {ID: 3},
		})
		expected := &resolve.Object{
			Fields: []*resolve.Field{
				{
					Name: []byte(`flaggedUsers`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name:  []byte(`name`),
									Value: &resolve.String{},
									Defer: &resolve.DeferField{DeferID: 1},
								},
								{
									Name:  []byte(`id`),
									Value: &resolve.String{},
									Defer: &resolve.DeferField{DeferID: 2},
									ParentOnTypeNames: []resolve.ParentOnTypeNames{
										{
											Depth: 1,
											Names: [][]byte{[]byte(`AppealQueue`)},
										},
									},
								},
							},
						},
					},
					Defer: &resolve.DeferField{DeferID: 1},
				},
				{
					Name: []byte(`flaggedUsers`),
					Value: &resolve.Array{
						Item: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name:  []byte(`__typename`),
									Value: &resolve.String{},
									Defer: &resolve.DeferField{DeferID: 3},
									ParentOnTypeNames: []resolve.ParentOnTypeNames{
										{
											Depth: 1,
											Names: [][]byte{[]byte(`AppealQueue`)},
										},
									},
								},
							},
						},
					},
					Defer:       &resolve.DeferField{DeferID: 3},
					OnTypeNames: [][]byte{[]byte(`AppealQueue`)},
				},
			},
		}
		assert.Equal(t, expected, in)
	})
}
