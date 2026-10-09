package resolve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wundergraph/astjson"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/datasource/httpclient"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/fastjsonext"
)

const contextualUsersQuery = `query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {profile(includeSuspended: $contextualArgument_0)}}}`

// contextualEntitiesDataSource records its requests and answers every representation with entity(representation, variables).
type contextualEntitiesDataSource struct {
	mu       sync.Mutex
	requests []string
	entity   func(representation, variables *astjson.Value) string
	// cacheControl is sent as the Cache-Control response header when set.
	cacheControl string
	// onRequest runs after a request is recorded and before it is answered.
	onRequest func(input string)
}

func (d *contextualEntitiesDataSource) Load(ctx context.Context, _ http.Header, input []byte) ([]byte, error) {
	d.mu.Lock()
	d.requests = append(d.requests, string(input))
	d.mu.Unlock()
	if d.onRequest != nil {
		d.onRequest(string(input))
	}
	if responseContext := httpclient.GetResponseContext(ctx); responseContext != nil && d.cacheControl != "" {
		responseContext.StatusCode = http.StatusOK
		responseContext.Response = &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Cache-Control": []string{d.cacheControl}}}
	}
	request, err := astjson.ParseBytes(input)
	if err != nil {
		return nil, err
	}
	variables := request.Get("body", "variables")
	out := []byte(`{"data":{"_entities":[`)
	for i, representation := range variables.GetArray("representations") {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, d.entity(representation, variables)...)
	}
	return append(out, `]}}`...), nil
}

func (d *contextualEntitiesDataSource) LoadWithFiles(ctx context.Context, headers http.Header, input []byte, _ []*httpclient.FileUpload) ([]byte, error) {
	return d.Load(ctx, headers, input)
}

func (d *contextualEntitiesDataSource) recordedRequests() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.requests)
}

// echoProfile answers a user with the profile "<id>:<value>" for the values of the named variables it received.
func echoProfile(variableNames ...string) func(representation, variables *astjson.Value) string {
	return func(representation, variables *astjson.Value) string {
		profile := string(representation.GetStringBytes("id"))
		for _, name := range variableNames {
			value := "absent"
			if v := variables.Get(name); v != nil {
				value = string(v.MarshalTo(nil))
			}
			profile += ":" + value
		}
		encoded, _ := json.Marshal(profile)
		return `{"profile":` + string(encoded) + `}`
	}
}

func newContextualUsersDataSource(variableNames ...string) *contextualEntitiesDataSource {
	return &contextualEntitiesDataSource{entity: echoProfile(variableNames...)}
}

func contextualStatic(data string) TemplateSegment {
	return TemplateSegment{SegmentType: StaticSegmentType, Data: []byte(data)}
}

func contextualUsersHeader() InputTemplate {
	return InputTemplate{
		Segments:                              []TemplateSegment{contextualStatic(`{"method":"POST","url":"http://users","body":{"query":"` + contextualUsersQuery + `","variables":{"representations":[`)},
		SetTemplateOutputToNullOnVariableNull: true,
	}
}

// contextualUsersFooter closes the representations and renders one variable per capture, as the planner's footer does.
func contextualUsersFooter(captures []ContextualArgumentCapture) InputTemplate {
	segments := []TemplateSegment{contextualStatic(`]`)}
	for _, capture := range captures {
		variable := &ContextualArgumentVariable{Capture: capture}
		segments = append(segments, contextualStatic(`,"`+capture.VariableName+`":`), variable.TemplateSegment())
	}
	segments = append(segments, contextualStatic(`}}}`))
	return InputTemplate{Segments: segments, SetTemplateOutputToNullOnVariableNull: true}
}

func contextualUserRepresentation() InputTemplate {
	return InputTemplate{
		Segments: []TemplateSegment{{
			SegmentType:  VariableSegmentType,
			VariableKind: ResolvableObjectVariableKind,
			Renderer: NewGraphQLVariableResolveRenderer(&Object{Fields: []*Field{
				{Name: []byte("__typename"), Value: &String{Path: []string{"__typename"}}},
				{Name: []byte("id"), Value: &String{Path: []string{"id"}}},
			}}),
		}},
		SetTemplateOutputToNullOnVariableNull: true,
	}
}

func contextualUsersInfo() *FetchInfo {
	return &FetchInfo{DataSourceID: "users", DataSourceName: "users", OperationType: ast.OperationTypeQuery}
}

// contextualUsersFetch is the batch entity fetch of the users subgraph with one variable per capture.
func contextualUsersFetch(fetchID int, ds DataSource, captures ...ContextualArgumentCapture) *BatchEntityFetch {
	return &BatchEntityFetch{
		FetchDependencies: FetchDependencies{FetchID: fetchID},
		Input: BatchInput{
			Header:               contextualUsersHeader(),
			Items:                []InputTemplate{contextualUserRepresentation()},
			SkipNullItems:        true,
			SkipEmptyObjectItems: true,
			SkipErrItems:         true,
			Separator:            InputTemplate{Segments: []TemplateSegment{contextualStatic(`,`)}},
			Footer:               contextualUsersFooter(captures),
		},
		DataSource:          ds,
		PostProcessing:      PostProcessingConfiguration{SelectResponseDataPath: []string{"data", "_entities"}, SelectResponseErrorsPath: []string{"errors"}},
		Info:                contextualUsersInfo(),
		ContextualArguments: captures,
	}
}

// contextualUsersRequest is the request of the users fetch for the user ids, followed by the rendered variables.
func contextualUsersRequest(variables string, ids ...string) string {
	representations := make([]string, len(ids))
	for i, id := range ids {
		representations[i] = `{"__typename":"User","id":"` + id + `"}`
	}
	return `{"method":"POST","url":"http://users","body":{"query":"` + contextualUsersQuery + `","variables":{"representations":[` + strings.Join(representations, ",") + `]` + variables + `}}}`
}

// staticContextualCapture reads the variable from the alias of every object at depth.
func staticContextualCapture(variableName string, depth int, typeName, alias string) ContextualArgumentCapture {
	return ContextualArgumentCapture{
		VariableName: variableName,
		Levels:       []ContextualArgumentLevel{{Depth: depth, Setters: []ContextualArgumentSetter{{TypeName: typeName, Path: []string{alias}}}}},
	}
}

var queueContextualCapture = staticContextualCapture("contextualArgument_0", 1, "ModerationQueue", "__context_0")

func flaggedUsersFetch(fetch Fetch) *FetchTreeNode {
	return SingleWithPath(fetch, "moderationQueues.@.flaggedUsers", ArrayPath("moderationQueues"), ArrayPath("flaggedUsers"))
}

func newContextualLoader(data string) *Loader {
	return &Loader{
		dataBuffer:   &DataBuffer{data: astjson.MustParse(data)},
		singleFlight: NewSingleFlight(1),
	}
}

// runContextualLoader runs fetches with loader and prints the merged data together with the fetch errors.
func runContextualLoader(t *testing.T, loader *Loader, ctx *Context, fetches *FetchTreeNode) string {
	t.Helper()
	response := &GraphQLResponse{Fetches: fetches, Info: &GraphQLResponseInfo{OperationType: ast.OperationTypeQuery}}
	require.NoError(t, loader.LoadGraphQLResponseData(ctx, response))
	return fastjsonext.PrintGraphQLResponse(loader.dataBuffer.Get(), loader.errors)
}

func loadContextual(t *testing.T, ctx *Context, data string, fetches *FetchTreeNode) string {
	t.Helper()
	return runContextualLoader(t, newContextualLoader(data), ctx, fetches)
}

func TestContextualArgumentCapture(t *testing.T) {
	t.Run("nearest ancestor wins", func(t *testing.T) {
		capture := ContextualArgumentCapture{
			VariableName: "contextualArgument_0",
			Levels: []ContextualArgumentLevel{
				{Depth: 2, MatchByTypename: true, Setters: []ContextualArgumentSetter{{TypeName: "ModerationQueue", Path: []string{"__context_0"}}}},
				{Depth: 1, Setters: []ContextualArgumentSetter{{TypeName: "Organization", Path: []string{"__context_1"}}}},
			},
		}
		users := newContextualUsersDataSource("contextualArgument_0")
		data := `{"organization":{"__context_1":false,"queues":[` +
			`{"__typename":"ModerationQueue","__context_0":true,"flaggedUsers":[{"__typename":"User","id":"u1"}]},` +
			`{"__typename":"AppealQueue","flaggedUsers":[{"__typename":"User","id":"u5"}]}]}}`

		out := loadContextual(t, NewContext(t.Context()), data,
			Single(contextualUsersFetch(1, users, capture), ObjectPath("organization"), ArrayPath("queues"), ArrayPath("flaggedUsers")))

		assert.ElementsMatch(t, []string{
			contextualUsersRequest(`,"contextualArgument_0":false`, "u5"),
			contextualUsersRequest(`,"contextualArgument_0":true`, "u1"),
		}, users.recordedRequests())
		assert.Equal(t, `{"data":{"organization":{"__context_1":false,"queues":[`+
			`{"__typename":"ModerationQueue","__context_0":true,"flaggedUsers":[{"__typename":"User","id":"u1","profile":"u1:true"}]},`+
			`{"__typename":"AppealQueue","flaggedUsers":[{"__typename":"User","id":"u5","profile":"u5:false"}]}]}}}`, out)
	})

	t.Run("typename level for an interface setter", func(t *testing.T) {
		capture := ContextualArgumentCapture{
			VariableName: "contextualArgument_0",
			Levels: []ContextualArgumentLevel{{Depth: 1, MatchByTypename: true, Setters: []ContextualArgumentSetter{
				{TypeName: "ModerationQueue", Path: []string{"__context_0"}},
				{TypeName: "AppealQueue", Path: []string{"__context_1"}},
			}}},
		}
		users := newContextualUsersDataSource("contextualArgument_0")
		data := `{"queues":[` +
			`{"__typename":"ModerationQueue","__context_0":true,"flaggedUsers":[{"__typename":"User","id":"u1"}]},` +
			`{"__typename":"AppealQueue","__context_1":false,"flaggedUsers":[{"__typename":"User","id":"u5"}]}]}`

		out := loadContextual(t, NewContext(t.Context()), data,
			Single(contextualUsersFetch(1, users, capture), ArrayPath("queues"), ArrayPath("flaggedUsers")))

		assert.ElementsMatch(t, []string{
			contextualUsersRequest(`,"contextualArgument_0":false`, "u5"),
			contextualUsersRequest(`,"contextualArgument_0":true`, "u1"),
		}, users.recordedRequests())
		assert.Equal(t, `{"data":{"queues":[`+
			`{"__typename":"ModerationQueue","__context_0":true,"flaggedUsers":[{"__typename":"User","id":"u1","profile":"u1:true"}]},`+
			`{"__typename":"AppealQueue","__context_1":false,"flaggedUsers":[{"__typename":"User","id":"u5","profile":"u5:false"}]}]}}`, out)
	})

	t.Run("typename level for a union setter with a member without a branch", func(t *testing.T) {
		// Team is a member of the union without a selection branch, so it sets the context to null and hides the root value.
		capture := ContextualArgumentCapture{
			VariableName: "contextualArgument_0",
			Levels: []ContextualArgumentLevel{
				{Depth: 1, MatchByTypename: true, Setters: []ContextualArgumentSetter{
					{TypeName: "ModerationQueue", Path: []string{"__context_0"}},
					{TypeName: "AppealQueue", Path: []string{"__context_1"}},
					{TypeName: "Team"},
				}},
				{Depth: 0, Setters: []ContextualArgumentSetter{{TypeName: "Query", Path: []string{"__context_2"}}}},
			},
		}
		users := newContextualUsersDataSource("contextualArgument_0")
		data := `{"__context_2":"root","scopes":[` +
			`{"__typename":"ModerationQueue","__context_0":"queue","users":[{"__typename":"User","id":"u2"}]},` +
			`{"__typename":"AppealQueue","__context_1":"appeal","users":[{"__typename":"User","id":"u5"}]},` +
			`{"__typename":"Team","users":[{"__typename":"User","id":"u7"}]},` +
			`{"__typename":"Department","users":[{"__typename":"User","id":"u8"}]}]}`

		out := loadContextual(t, NewContext(t.Context()), data,
			Single(contextualUsersFetch(1, users, capture), ArrayPath("scopes"), ArrayPath("users")))

		assert.ElementsMatch(t, []string{
			contextualUsersRequest(`,"contextualArgument_0":"appeal"`, "u5"),
			contextualUsersRequest(`,"contextualArgument_0":"queue"`, "u2"),
			contextualUsersRequest(`,"contextualArgument_0":"root"`, "u8"),
			contextualUsersRequest(`,"contextualArgument_0":null`, "u7"),
		}, users.recordedRequests())
		assert.Contains(t, out, `{"__typename":"User","id":"u7","profile":"u7:null"}`)
		assert.Contains(t, out, `{"__typename":"User","id":"u8","profile":"u8:\"root\""}`)
	})

	t.Run("capture depths under lists of lists", func(t *testing.T) {
		users := newContextualUsersDataSource("contextualArgument_0")
		data := `{"queueGrid":[` +
			`[{"__typename":"ModerationQueue","__context_0":true,"flaggedUserPages":[[{"__typename":"User","id":"u1"}],[{"__typename":"User","id":"u2"}]]},` +
			`{"__typename":"ModerationQueue","__context_0":false,"flaggedUserPages":[[{"__typename":"User","id":"u2"},{"__typename":"User","id":"u3"}]]}],` +
			`[{"__typename":"ModerationQueue","__context_0":true,"flaggedUserPages":[[{"__typename":"User","id":"u1"}],[],[{"__typename":"User","id":"u4"}]]}]]}`

		out := loadContextual(t, NewContext(t.Context()), data,
			Single(contextualUsersFetch(1, users, queueContextualCapture), ArrayPath("queueGrid"), ArrayPath("flaggedUserPages")))

		assert.ElementsMatch(t, []string{
			contextualUsersRequest(`,"contextualArgument_0":false`, "u2", "u3"),
			contextualUsersRequest(`,"contextualArgument_0":true`, "u1", "u2", "u4"),
		}, users.recordedRequests())
		assert.Equal(t, `{"data":{"queueGrid":[`+
			`[{"__typename":"ModerationQueue","__context_0":true,"flaggedUserPages":[[{"__typename":"User","id":"u1","profile":"u1:true"}],[{"__typename":"User","id":"u2","profile":"u2:true"}]]},`+
			`{"__typename":"ModerationQueue","__context_0":false,"flaggedUserPages":[[{"__typename":"User","id":"u2","profile":"u2:false"},{"__typename":"User","id":"u3","profile":"u3:false"}]]}],`+
			`[{"__typename":"ModerationQueue","__context_0":true,"flaggedUserPages":[[{"__typename":"User","id":"u1","profile":"u1:true"}],[],[{"__typename":"User","id":"u4","profile":"u4:true"}]]}]]}}`, out)
	})

	t.Run("null intermediate ancestors drop out of the walk", func(t *testing.T) {
		capture := staticContextualCapture("contextualArgument_0", 2, "ModerationQueue", "__context_0")
		users := newContextualUsersDataSource("contextualArgument_0")
		data := `{"organization":{"queues":[` +
			`{"__context_0":true,"team":{"members":[{"__typename":"User","id":"u7"}]}},` +
			`{"__context_0":false,"team":null},` +
			`null,` +
			`{"__context_0":false,"team":{"members":null}}]}}`

		out := loadContextual(t, NewContext(t.Context()), data,
			Single(contextualUsersFetch(1, users, capture), ObjectPath("organization"), ArrayPath("queues"), ObjectPath("team"), ArrayPath("members")))

		assert.ElementsMatch(t, []string{contextualUsersRequest(`,"contextualArgument_0":true`, "u7")}, users.recordedRequests())
		assert.Equal(t, `{"data":{"organization":{"queues":[`+
			`{"__context_0":true,"team":{"members":[{"__typename":"User","id":"u7","profile":"u7:true"}]}},`+
			`{"__context_0":false,"team":null},`+
			`null,`+
			`{"__context_0":false,"team":{"members":null}}]}}}`, out)
	})

	t.Run("a null setter value is sent as null and a missing value excludes its entities", func(t *testing.T) {
		users := newContextualUsersDataSource("contextualArgument_0")
		data := `{"moderationQueues":[` +
			`{"__typename":"ModerationQueue","__context_0":null,"flaggedUsers":[{"__typename":"User","id":"u1"}]},` +
			`{"__typename":"ModerationQueue","flaggedUsers":[{"__typename":"User","id":"u2"},{"__typename":"User","id":"u3"}]}]}`

		out := loadContextual(t, NewContext(t.Context()), data, flaggedUsersFetch(contextualUsersFetch(1, users, queueContextualCapture)))

		assert.ElementsMatch(t, []string{contextualUsersRequest(`,"contextualArgument_0":null`, "u1")}, users.recordedRequests())
		assert.Equal(t, `{"errors":[{"message":"Failed to fetch from Subgraph 'users' at Path 'moderationQueues.@.flaggedUsers', Reason: 2 entities were not fetched because the context value of their 'ModerationQueue' ancestor is missing."}],`+
			`"data":{"moderationQueues":[`+
			`{"__typename":"ModerationQueue","__context_0":null,"flaggedUsers":[{"__typename":"User","id":"u1","profile":"u1:null"}]},`+
			`{"__typename":"ModerationQueue","flaggedUsers":[{"__typename":"User","id":"u2"},{"__typename":"User","id":"u3"}]}]}}`, out)
	})

	t.Run("a selection crossing a list sends the selected field of every element", func(t *testing.T) {
		capture := ContextualArgumentCapture{
			VariableName: "contextualArgument_0",
			Levels: []ContextualArgumentLevel{{Depth: 1, Setters: []ContextualArgumentSetter{
				{TypeName: "ModerationQueue", Path: []string{"__context_0", "id"}},
			}}},
		}
		users := newContextualUsersDataSource("contextualArgument_0")
		data := `{"moderationQueues":[` +
			`{"__context_0":[{"id":"u1"},null,{"id":"u2"}],"flaggedUsers":[{"__typename":"User","id":"u1"}]},` +
			`{"__context_0":[{"name":"without id"}],"flaggedUsers":[{"__typename":"User","id":"u3"}]}]}`

		out := loadContextual(t, NewContext(t.Context()), data, flaggedUsersFetch(contextualUsersFetch(1, users, capture)))

		assert.ElementsMatch(t, []string{contextualUsersRequest(`,"contextualArgument_0":["u1",null,"u2"]`, "u1")}, users.recordedRequests())
		assert.Contains(t, out, `Reason: 1 entity was not fetched because the context value of its 'ModerationQueue' ancestor is missing.`)
	})

	t.Run("missing values of several setter types are counted per type", func(t *testing.T) {
		capture := ContextualArgumentCapture{
			VariableName: "contextualArgument_0",
			Levels: []ContextualArgumentLevel{{Depth: 1, MatchByTypename: true, Setters: []ContextualArgumentSetter{
				{TypeName: "ModerationQueue", Path: []string{"__context_0"}},
				{TypeName: "AppealQueue", Path: []string{"__context_1"}},
			}}},
		}
		users := newContextualUsersDataSource("contextualArgument_0")
		data := `{"moderationQueues":[` +
			`{"__typename":"AppealQueue","flaggedUsers":[{"__typename":"User","id":"u0"}]},` +
			`{"__typename":"ModerationQueue","flaggedUsers":[{"__typename":"User","id":"u1"}]},` +
			`{"__typename":"ModerationQueue","__context_0":true,"flaggedUsers":[{"__typename":"User","id":"u2"}]},` +
			`{"__typename":"ModerationQueue","flaggedUsers":[{"__typename":"User","id":"u3"},{"__typename":"User","id":"u4"}]},` +
			`{"__typename":"AppealQueue","flaggedUsers":[{"__typename":"User","id":"u5"}]}]}`

		out := loadContextual(t, NewContext(t.Context()), data, flaggedUsersFetch(contextualUsersFetch(1, users, capture)))

		assert.Equal(t, []string{contextualUsersRequest(`,"contextualArgument_0":true`, "u2")}, users.recordedRequests())
		assert.Equal(t, `{"errors":[{"message":"Failed to fetch from Subgraph 'users' at Path 'moderationQueues.@.flaggedUsers', Reason: `+
			`2 entities were not fetched because the context value of their 'AppealQueue' ancestor is missing; `+
			`3 entities were not fetched because the context value of their 'ModerationQueue' ancestor is missing."}],`+
			`"data":{"moderationQueues":[`+
			`{"__typename":"AppealQueue","flaggedUsers":[{"__typename":"User","id":"u0"}]},`+
			`{"__typename":"ModerationQueue","flaggedUsers":[{"__typename":"User","id":"u1"}]},`+
			`{"__typename":"ModerationQueue","__context_0":true,"flaggedUsers":[{"__typename":"User","id":"u2","profile":"u2:true"}]},`+
			`{"__typename":"ModerationQueue","flaggedUsers":[{"__typename":"User","id":"u3"},{"__typename":"User","id":"u4"}]},`+
			`{"__typename":"AppealQueue","flaggedUsers":[{"__typename":"User","id":"u5"}]}]}}`, out)
	})

	t.Run("two contextual arguments group by their tuple of values", func(t *testing.T) {
		captures := []ContextualArgumentCapture{
			queueContextualCapture,
			staticContextualCapture("contextualArgument_1", 1, "ModerationQueue", "__context_1"),
		}
		users := newContextualUsersDataSource("contextualArgument_0", "contextualArgument_1")
		data := `{"moderationQueues":[` +
			`{"__context_0":true,"__context_1":3,"flaggedUsers":[{"__typename":"User","id":"u1"}]},` +
			`{"__context_0":true,"__context_1":1,"flaggedUsers":[{"__typename":"User","id":"u2"}]},` +
			`{"__context_0":true,"__context_1":3,"flaggedUsers":[{"__typename":"User","id":"u4"}]}]}`

		out := loadContextual(t, NewContext(t.Context()), data, flaggedUsersFetch(contextualUsersFetch(1, users, captures...)))

		assert.ElementsMatch(t, []string{
			contextualUsersRequest(`,"contextualArgument_0":true,"contextualArgument_1":1`, "u2"),
			contextualUsersRequest(`,"contextualArgument_0":true,"contextualArgument_1":3`, "u1", "u4"),
		}, users.recordedRequests())
		assert.Equal(t, `{"data":{"moderationQueues":[`+
			`{"__context_0":true,"__context_1":3,"flaggedUsers":[{"__typename":"User","id":"u1","profile":"u1:true:3"}]},`+
			`{"__context_0":true,"__context_1":1,"flaggedUsers":[{"__typename":"User","id":"u2","profile":"u2:true:1"}]},`+
			`{"__context_0":true,"__context_1":3,"flaggedUsers":[{"__typename":"User","id":"u4","profile":"u4:true:3"}]}]}}`, out)
	})

	t.Run("a setter value merged by an earlier fetch of another subgraph", func(t *testing.T) {
		valueOf := map[string]string{"q1": "true", "q2": "false"}
		public := &contextualEntitiesDataSource{entity: func(representation, _ *astjson.Value) string {
			return `{"__context_0":` + valueOf[string(representation.GetStringBytes("id"))] + `}`
		}}
		users := newContextualUsersDataSource("contextualArgument_0")
		profiles := contextualUsersFetch(2, users, queueContextualCapture)
		profiles.DependsOnFetchIDs = []int{1}
		data := `{"moderationQueues":[` +
			`{"__typename":"ModerationQueue","id":"q1","flaggedUsers":[{"__typename":"User","id":"u1"}]},` +
			`{"__typename":"ModerationQueue","id":"q2","flaggedUsers":[{"__typename":"User","id":"u2"}]}]}`

		out := loadContextual(t, NewContext(t.Context()), data, Sequence(
			SingleWithPath(contextualUsersFetch(1, public), "moderationQueues", ArrayPath("moderationQueues")),
			flaggedUsersFetch(profiles),
		))

		assert.ElementsMatch(t, []string{
			contextualUsersRequest(`,"contextualArgument_0":true`, "u1"),
			contextualUsersRequest(`,"contextualArgument_0":false`, "u2"),
		}, users.recordedRequests())
		assert.Contains(t, out, `"flaggedUsers":[{"__typename":"User","id":"u1","profile":"u1:true"}]`)
		assert.Contains(t, out, `"flaggedUsers":[{"__typename":"User","id":"u2","profile":"u2:false"}]`)
	})

	t.Run("sibling fragments with and without a setter keep their own items", func(t *testing.T) {
		data := `{"queues":[` +
			`{"__typename":"ModerationQueue","__context_0":true,"members":[{"__typename":"User","id":"u1"}]},` +
			`{"__typename":"AppealQueue","members":[{"__typename":"User","id":"u5"}]}]}`
		for range 20 {
			contextual := newContextualUsersDataSource("contextualArgument_0")
			withoutSetter := newContextualUsersDataSource()
			nullFetch := contextualUsersFetch(2, withoutSetter)
			nullFetch.Input.Footer = InputTemplate{Segments: []TemplateSegment{contextualStatic(`],"contextualArgument_0":null}}}`)}}

			out := loadContextual(t, NewContext(t.Context()), data, Parallel(
				Single(contextualUsersFetch(1, contextual, queueContextualCapture), ArrayPath("queues"), PathElementWithTypeNames(ArrayPath("members"), []string{"ModerationQueue"})),
				Single(nullFetch, ArrayPath("queues"), PathElementWithTypeNames(ArrayPath("members"), []string{"AppealQueue"})),
			))

			require.Equal(t, []string{contextualUsersRequest(`,"contextualArgument_0":true`, "u1")}, contextual.recordedRequests())
			require.Equal(t, []string{contextualUsersRequest(`,"contextualArgument_0":null`, "u5")}, withoutSetter.recordedRequests())
			require.Equal(t, `{"data":{"queues":[`+
				`{"__typename":"ModerationQueue","__context_0":true,"members":[{"__typename":"User","id":"u1","profile":"u1:true"}]},`+
				`{"__typename":"AppealQueue","members":[{"__typename":"User","id":"u5","profile":"u5"}]}]}}`, out)
		}
	})

	t.Run("an entity fetch renders the value of its single item", func(t *testing.T) {
		users := newContextualUsersDataSource("contextualArgument_0")
		fetch := &EntityFetch{
			FetchDependencies: FetchDependencies{FetchID: 1},
			Input: EntityInput{
				Header:      contextualUsersHeader(),
				Item:        contextualUserRepresentation(),
				SkipErrItem: true,
				Footer:      contextualUsersFooter([]ContextualArgumentCapture{queueContextualCapture}),
			},
			DataSource:          users,
			PostProcessing:      PostProcessingConfiguration{SelectResponseDataPath: []string{"data", "_entities", "0"}, SelectResponseErrorsPath: []string{"errors"}},
			Info:                contextualUsersInfo(),
			ContextualArguments: []ContextualArgumentCapture{queueContextualCapture},
		}
		data := `{"moderationQueue":{"__context_0":true,"owner":{"__typename":"User","id":"u1"}}}`

		out := loadContextual(t, NewContext(t.Context()), data, Single(fetch, ObjectPath("moderationQueue"), ObjectPath("owner")))

		assert.ElementsMatch(t, []string{contextualUsersRequest(`,"contextualArgument_0":true`, "u1")}, users.recordedRequests())
		assert.Equal(t, `{"data":{"moderationQueue":{"__context_0":true,"owner":{"__typename":"User","id":"u1","profile":"u1:true"}}}}`, out)
	})
}

const contextualQueuesData = `{"moderationQueues":[` +
	`{"__context_0":true,"flaggedUsers":[{"__typename":"User","id":"u1"},{"__typename":"User","id":"u2"}]},` +
	`{"__context_0":false,"flaggedUsers":[{"__typename":"User","id":"u2"},{"__typename":"User","id":"u3"}]},` +
	`{"__context_0":true,"flaggedUsers":[{"__typename":"User","id":"u1"},{"__typename":"User","id":"u4"}]}]}`

// contextualQueuesWithValues seeds one queue with one flagged user per value.
func contextualQueuesWithValues(values ...string) string {
	queues := make([]string, len(values))
	for i, value := range values {
		queues[i] = `{"__context_0":` + value + `,"flaggedUsers":[{"__typename":"User","id":"u` + strconv.Itoa(i) + `"}]}`
	}
	return `{"moderationQueues":[` + strings.Join(queues, ",") + `]}`
}

func contextualListField(name string, fields ...*Field) *Field {
	return &Field{Name: []byte(name), Value: &Array{Path: []string{name}, Item: &Object{Fields: fields}}}
}

func contextualStringField(name string) *Field {
	return &Field{Name: []byte(name), Value: &String{Path: []string{name}, Nullable: true}}
}

func contextualRootFetch(data string) *SingleFetch {
	return &SingleFetch{
		FetchConfiguration: FetchConfiguration{
			DataSource:     FakeDataSource(`{"data":` + data + `}`),
			PostProcessing: PostProcessingConfiguration{SelectResponseDataPath: []string{"data"}},
		},
		InputTemplate: InputTemplate{Segments: []TemplateSegment{contextualStatic(`{"method":"POST","url":"http://moderation","body":{"query":"{moderationQueues}"}}`)}},
	}
}

func TestContextualArgumentGrouping(t *testing.T) {
	t.Run("two values two requests", func(t *testing.T) {
		users := newContextualUsersDataSource("contextualArgument_0")

		out := loadContextual(t, NewContext(t.Context()), contextualQueuesData, flaggedUsersFetch(contextualUsersFetch(1, users, queueContextualCapture)))

		assert.ElementsMatch(t, []string{
			contextualUsersRequest(`,"contextualArgument_0":true`, "u1", "u2", "u4"),
			contextualUsersRequest(`,"contextualArgument_0":false`, "u2", "u3"),
		}, users.recordedRequests())
		assert.Equal(t, `{"data":{"moderationQueues":[`+
			`{"__context_0":true,"flaggedUsers":[{"__typename":"User","id":"u1","profile":"u1:true"},{"__typename":"User","id":"u2","profile":"u2:true"}]},`+
			`{"__context_0":false,"flaggedUsers":[{"__typename":"User","id":"u2","profile":"u2:false"},{"__typename":"User","id":"u3","profile":"u3:false"}]},`+
			`{"__context_0":true,"flaggedUsers":[{"__typename":"User","id":"u1","profile":"u1:true"},{"__typename":"User","id":"u4","profile":"u4:true"}]}]}}`, out)
	})

	t.Run("same representation in two groups is fetched twice", func(t *testing.T) {
		users := newContextualUsersDataSource("contextualArgument_0")
		data := `{"moderationQueues":[` +
			`{"__context_0":true,"flaggedUsers":[{"__typename":"User","id":"u2"},{"__typename":"User","id":"u2"}]},` +
			`{"__context_0":false,"flaggedUsers":[{"__typename":"User","id":"u2"}]}]}`

		out := loadContextual(t, NewContext(t.Context()), data, flaggedUsersFetch(contextualUsersFetch(1, users, queueContextualCapture)))

		assert.ElementsMatch(t, []string{
			contextualUsersRequest(`,"contextualArgument_0":true`, "u2"),
			contextualUsersRequest(`,"contextualArgument_0":false`, "u2"),
		}, users.recordedRequests())
		assert.Equal(t, `{"data":{"moderationQueues":[`+
			`{"__context_0":true,"flaggedUsers":[{"__typename":"User","id":"u2","profile":"u2:true"},{"__typename":"User","id":"u2","profile":"u2:true"}]},`+
			`{"__context_0":false,"flaggedUsers":[{"__typename":"User","id":"u2","profile":"u2:false"}]}]}}`, out)
	})

	t.Run("single value matches today's request", func(t *testing.T) {
		data := `{"moderationQueues":[` +
			`{"__context_0":true,"flaggedUsers":[{"__typename":"User","id":"u1"},{"__typename":"User","id":"u2"}]},` +
			`{"__context_0":true,"flaggedUsers":[{"__typename":"User","id":"u1"},{"__typename":"User","id":"u4"}]}]}`
		contextual := newContextualUsersDataSource("contextualArgument_0")
		today := newContextualUsersDataSource("contextualArgument_0")
		todayFetch := contextualUsersFetch(1, today)
		todayFetch.Input.Footer = InputTemplate{Segments: []TemplateSegment{contextualStatic(`],"contextualArgument_0":true}}}`)}, SetTemplateOutputToNullOnVariableNull: true}

		contextualOut := loadContextual(t, NewContext(t.Context()), data, flaggedUsersFetch(contextualUsersFetch(1, contextual, queueContextualCapture)))
		todayOut := loadContextual(t, NewContext(t.Context()), data, flaggedUsersFetch(todayFetch))

		assert.Equal(t, []string{contextualUsersRequest(`,"contextualArgument_0":true`, "u1", "u2", "u4")}, contextual.recordedRequests())
		assert.Equal(t, today.recordedRequests(), contextual.recordedRequests())
		assert.Equal(t, todayOut, contextualOut)
	})

	t.Run("positions sharing nodes after batch dedupe keep their own values", func(t *testing.T) {
		// The friends fetch deduplicates u2 across both queues, so without copies both u2 positions would share one friends list.
		root := `{"moderationQueues":[` +
			`{"id":"q1","__context_0":true,"flaggedUsers":[{"__typename":"User","id":"u1"},{"__typename":"User","id":"u2"}]},` +
			`{"id":"q2","__context_0":false,"flaggedUsers":[{"__typename":"User","id":"u2"},{"__typename":"User","id":"u3"}]}]}`
		friendsOf := map[string]string{"u1": `[{"__typename":"User","id":"u2"}]`, "u2": `[{"__typename":"User","id":"u3"}]`, "u3": `[]`}
		expected := `{"data":{"moderationQueues":[` +
			`{"id":"q1","flaggedUsers":[{"id":"u1","friends":[{"id":"u2","profile":"u2:true"}]},{"id":"u2","friends":[{"id":"u3","profile":"u3:true"}]}]},` +
			`{"id":"q2","flaggedUsers":[{"id":"u2","friends":[{"id":"u3","profile":"u3:false"}]},{"id":"u3","friends":[]}]}]}}`

		for _, withArena := range []bool{false, true} {
			friends := &contextualEntitiesDataSource{entity: func(representation, _ *astjson.Value) string {
				return `{"friends":` + friendsOf[string(representation.GetStringBytes("id"))] + `}`
			}}
			friendsFetch := contextualUsersFetch(1, friends)
			friendsFetch.DeepCopyForSharedTargets = true
			users := newContextualUsersDataSource("contextualArgument_0")
			profiles := contextualUsersFetch(2, users, queueContextualCapture)
			profiles.DependsOnFetchIDs = []int{1}
			response := &GraphQLResponse{
				Info: &GraphQLResponseInfo{OperationType: ast.OperationTypeQuery},
				Fetches: Sequence(
					Single(contextualRootFetch(root)),
					flaggedUsersFetch(friendsFetch),
					SingleWithPath(profiles, "moderationQueues.@.flaggedUsers.@.friends", ArrayPath("moderationQueues"), ArrayPath("flaggedUsers"), ArrayPath("friends")),
				),
				Data: &Object{Fields: []*Field{
					contextualListField("moderationQueues", contextualStringField("id"),
						contextualListField("flaggedUsers", contextualStringField("id"),
							contextualListField("friends", contextualStringField("id"), contextualStringField("profile")))),
				}},
			}

			r := newResolver(t.Context())
			out := &bytes.Buffer{}
			var err error
			if withArena {
				_, err = r.ArenaResolveGraphQLResponse(NewContext(t.Context()), response, out)
			} else {
				_, err = r.ResolveGraphQLResponse(NewContext(t.Context()), response, nil, out)
			}
			require.NoError(t, err)
			assert.Equal(t, expected, out.String(), "arena: %v", withArena)
			assert.ElementsMatch(t, []string{
				contextualUsersRequest(`,"contextualArgument_0":true`, "u2", "u3"),
				contextualUsersRequest(`,"contextualArgument_0":false`, "u3"),
			}, users.recordedRequests(), "arena: %v", withArena)
		}
	})

	t.Run("a failed group request skips the dependents of the fetch", func(t *testing.T) {
		users := newContextualUsersDataSource("contextualArgument_0")
		failing := &failingContextualDataSource{DataSource: users, failWhen: `"contextualArgument_0":false`}
		dependent := newContextualUsersDataSource()
		dependentFetch := contextualUsersFetch(2, dependent)
		dependentFetch.DependsOnFetchIDs = []int{1}

		out := loadContextual(t, NewContext(t.Context()), contextualQueuesData, Sequence(
			flaggedUsersFetch(contextualUsersFetch(1, failing, queueContextualCapture)),
			flaggedUsersFetch(dependentFetch),
		))

		assert.Len(t, users.recordedRequests(), 2)
		assert.Empty(t, dependent.recordedRequests())
		assert.Contains(t, out, `{"errors":[{"message":"Failed to fetch from Subgraph 'users' at Path 'moderationQueues.@.flaggedUsers'."}]`)
		assert.Contains(t, out, `{"__typename":"User","id":"u4","profile":"u4:true"}`)
		assert.Contains(t, out, `{"__context_0":false,"flaggedUsers":[{"__typename":"User","id":"u2"},{"__typename":"User","id":"u3"}]}`)
	})

	t.Run("over the group limit returns an error and sends nothing", func(t *testing.T) {
		users := newContextualUsersDataSource("contextualArgument_0")
		dependent := newContextualUsersDataSource()
		dependentFetch := contextualUsersFetch(2, dependent)
		dependentFetch.DependsOnFetchIDs = []int{1}
		loader := newContextualLoader(contextualQueuesWithValues("1", "2", "3"))
		loader.maxContextualArgumentGroups = 2

		out := runContextualLoader(t, loader, NewContext(t.Context()), Sequence(
			flaggedUsersFetch(contextualUsersFetch(1, users, queueContextualCapture)),
			flaggedUsersFetch(dependentFetch),
		))

		assert.Empty(t, users.recordedRequests())
		assert.Empty(t, dependent.recordedRequests())
		assert.Equal(t, `{"errors":[{"message":"Failed to fetch from Subgraph 'users' at Path 'moderationQueues.@.flaggedUsers', Reason: the entities need 3 requests for distinct contextual argument values, which exceeds the limit of 2."}],`+
			`"data":`+contextualQueuesWithValues("1", "2", "3")+`}`, out)
	})

	t.Run("the default limit allows 100 groups", func(t *testing.T) {
		values := make([]string, 101)
		for i := range values {
			values[i] = strconv.Itoa(i)
		}

		users := newContextualUsersDataSource("contextualArgument_0")
		out := loadContextual(t, NewContext(t.Context()), contextualQueuesWithValues(values[:100]...), flaggedUsersFetch(contextualUsersFetch(1, users, queueContextualCapture)))
		assert.Len(t, users.recordedRequests(), 100)
		assert.NotContains(t, out, `"errors"`)

		users = newContextualUsersDataSource("contextualArgument_0")
		out = loadContextual(t, NewContext(t.Context()), contextualQueuesWithValues(values...), flaggedUsersFetch(contextualUsersFetch(1, users, queueContextualCapture)))
		assert.Empty(t, users.recordedRequests())
		assert.Contains(t, out, `Reason: the entities need 101 requests for distinct contextual argument values, which exceeds the limit of 100.`)
	})

	t.Run("equal values in separate setter objects share one request", func(t *testing.T) {
		users := newContextualUsersDataSource("contextualArgument_0")

		out := loadContextual(t, NewContext(t.Context()), contextualQueuesWithValues("1", "1", "2", "1"), flaggedUsersFetch(contextualUsersFetch(1, users, queueContextualCapture)))

		assert.ElementsMatch(t, []string{
			contextualUsersRequest(`,"contextualArgument_0":1`, "u0", "u1", "u3"),
			contextualUsersRequest(`,"contextualArgument_0":2`, "u2"),
		}, users.recordedRequests())
		assert.Contains(t, out, `{"__typename":"User","id":"u3","profile":"u3:1"}`)
	})

	t.Run("groups that render only null representations send nothing", func(t *testing.T) {
		data := `{"moderationQueues":[{"__context_0":1,"flaggedUsers":[null]},{"__context_0":2,"flaggedUsers":[null,null]}]}`
		for _, tracing := range []bool{false, true} {
			users := newContextualUsersDataSource("contextualArgument_0")
			ctx := NewContext(t.Context())
			ctx.TracingOptions = TraceOptions{Enable: tracing, ExcludeLoadStats: true, EnablePredictableDebugTimings: true}
			fetch := contextualUsersFetch(1, users, queueContextualCapture)

			out := loadContextual(t, ctx, data, flaggedUsersFetch(fetch))

			assert.Empty(t, users.recordedRequests(), "tracing: %v", tracing)
			assert.Equal(t, `{"data":`+data+`}`, out, "tracing: %v", tracing)
			if tracing {
				require.Len(t, fetch.Traces, 2)
				assert.True(t, fetch.Traces[0].LoadSkipped)
				assert.True(t, fetch.Traces[1].LoadSkipped)
			}
		}
	})

	t.Run("rate limiter is called once for three groups", func(t *testing.T) {
		limiter := &testRateLimiter{allowFn: func(*Context, *FetchInfo, json.RawMessage) (*RateLimitDeny, error) { return nil, nil }}
		ctx := NewContext(t.Context())
		ctx.rateLimiter = limiter
		ctx.RateLimitOptions = RateLimitOptions{Enable: true}
		users := newContextualUsersDataSource("contextualArgument_0")

		loadContextual(t, ctx, contextualQueuesWithValues("1", "2", "3"), flaggedUsersFetch(contextualUsersFetch(1, users, queueContextualCapture)))

		assert.Equal(t, int64(1), limiter.rateLimitPreFetchCalls.Load())
		assert.Len(t, users.recordedRequests(), 3)
	})

	t.Run("a rejection skips every group and is rendered once", func(t *testing.T) {
		limiter := &testRateLimiter{allowFn: func(*Context, *FetchInfo, json.RawMessage) (*RateLimitDeny, error) {
			return &RateLimitDeny{Reason: "rate limit exceeded"}, nil
		}}
		ctx := NewContext(t.Context())
		ctx.rateLimiter = limiter
		ctx.RateLimitOptions = RateLimitOptions{Enable: true}
		users := newContextualUsersDataSource("contextualArgument_0")

		out := loadContextual(t, ctx, contextualQueuesWithValues("1", "2", "3"), flaggedUsersFetch(contextualUsersFetch(1, users, queueContextualCapture)))

		assert.Equal(t, int64(1), limiter.rateLimitPreFetchCalls.Load())
		assert.Empty(t, users.recordedRequests())
		assert.Equal(t, 1, strings.Count(out, `"message"`))
		assert.Contains(t, out, `{"errors":[{"message":"Rate limit exceeded for Subgraph 'users' at Path 'moderationQueues.@.flaggedUsers', Reason: rate limit exceeded."}]`)
	})
}

// failingContextualDataSource fails the requests whose input contains failWhen.
type failingContextualDataSource struct {
	DataSource

	failWhen string
}

func (f *failingContextualDataSource) Load(ctx context.Context, headers http.Header, input []byte) ([]byte, error) {
	data, err := f.DataSource.Load(ctx, headers, input)
	if strings.Contains(string(input), f.failWhen) {
		return nil, errors.New("subgraph unavailable")
	}
	return data, err
}

// scoreErrorsDataSource reports an error for the score of the entities at indices in the responses to the requests that contain when.
type scoreErrorsDataSource struct {
	*contextualEntitiesDataSource

	when    string
	indices []int
}

func (d *scoreErrorsDataSource) Load(ctx context.Context, headers http.Header, input []byte) ([]byte, error) {
	out, err := d.contextualEntitiesDataSource.Load(ctx, headers, input)
	if err != nil || !strings.Contains(string(input), d.when) {
		return out, err
	}
	errs := make([]string, len(d.indices))
	for i, index := range d.indices {
		errs[i] = `{"message":"score failed","path":["_entities",` + strconv.Itoa(index) + `,"score"]}`
	}
	return append(out[:len(out)-1], `,"errors":[`+strings.Join(errs, ",")+`]}`...), nil
}

func (d *scoreErrorsDataSource) LoadWithFiles(ctx context.Context, headers http.Header, input []byte, _ []*httpclient.FileUpload) ([]byte, error) {
	return d.Load(ctx, headers, input)
}

func TestContextualArgumentTaintedEntities(t *testing.T) {
	requiresScore := []FetchReason{{TypeName: "User", FieldName: "score", IsRequires: true, Nullable: true}}

	t.Run("entities tainted by an earlier fetch are left out of their runs", func(t *testing.T) {
		// The earlier fetch fails the score of u1, the last entity of the first run, and of u2, the first entity of the second run.
		data := `{"moderationQueues":[` +
			`{"__context_0":true,"flaggedUsers":[{"__typename":"User","id":"u0"},{"__typename":"User","id":"u1"}]},` +
			`{"__context_0":false,"flaggedUsers":[{"__typename":"User","id":"u2"},{"__typename":"User","id":"u3"}]},` +
			`{"__context_0":true,"flaggedUsers":[{"__typename":"User","id":"u4"}]}]}`
		scores := &scoreErrorsDataSource{contextualEntitiesDataSource: &contextualEntitiesDataSource{entity: func(_, _ *astjson.Value) string {
			return `{"__typename":"User","score":null}`
		}}, indices: []int{1, 2}}
		scoresFetch := contextualUsersFetch(1, scores)
		scoresFetch.Info.FetchReasons = requiresScore
		users := newContextualUsersDataSource("contextualArgument_0")
		profiles := contextualUsersFetch(2, users, queueContextualCapture)
		profiles.DependsOnFetchIDs = []int{1}
		loader := newContextualLoader(data)
		loader.validateRequiredExternalFields = true

		runContextualLoader(t, loader, NewContext(t.Context()), Sequence(flaggedUsersFetch(scoresFetch), flaggedUsersFetch(profiles)))

		assert.ElementsMatch(t, []string{
			contextualUsersRequest(`,"contextualArgument_0":true`, "u0", "u4"),
			contextualUsersRequest(`,"contextualArgument_0":false`, "u3"),
		}, users.recordedRequests())
	})

	t.Run("a group taints only its own entities", func(t *testing.T) {
		// The response to the false group fails the score of its first entity, u2.
		data := `{"moderationQueues":[` +
			`{"__context_0":true,"flaggedUsers":[{"__typename":"User","id":"u0"},{"__typename":"User","id":"u1"}]},` +
			`{"__context_0":false,"flaggedUsers":[{"__typename":"User","id":"u2"},{"__typename":"User","id":"u3"}]}]}`
		users := &scoreErrorsDataSource{contextualEntitiesDataSource: &contextualEntitiesDataSource{entity: func(representation, _ *astjson.Value) string {
			return `{"__typename":"User","score":null,"profile":"` + string(representation.GetStringBytes("id")) + `"}`
		}}, when: `"contextualArgument_0":false`, indices: []int{0}}
		profiles := contextualUsersFetch(1, users, queueContextualCapture)
		profiles.Info.FetchReasons = requiresScore
		dependent := newContextualUsersDataSource()
		dependentFetch := contextualUsersFetch(2, dependent)
		dependentFetch.DependsOnFetchIDs = []int{1}
		loader := newContextualLoader(data)
		loader.validateRequiredExternalFields = true

		runContextualLoader(t, loader, NewContext(t.Context()), Sequence(flaggedUsersFetch(profiles), flaggedUsersFetch(dependentFetch)))

		assert.Len(t, users.recordedRequests(), 2)
		assert.Equal(t, []string{contextualUsersRequest(``, "u0", "u1", "u3")}, dependent.recordedRequests())
	})
}

// blockingContextualDataSource reports every request on arrived and answers it only with the error of its canceled context.
type blockingContextualDataSource struct {
	arrived chan struct{}
}

func (d *blockingContextualDataSource) Load(ctx context.Context, _ http.Header, _ []byte) ([]byte, error) {
	d.arrived <- struct{}{}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (d *blockingContextualDataSource) LoadWithFiles(ctx context.Context, headers http.Header, input []byte, _ []*httpclient.FileUpload) ([]byte, error) {
	return d.Load(ctx, headers, input)
}

func TestContextualArgumentCancellation(t *testing.T) {
	t.Run("groups that have not started when the operation is canceled are not sent and are reported once", func(t *testing.T) {
		const groups = maxConcurrentContextualArgumentGroupLoads + 4
		values := make([]string, groups)
		for i := range values {
			values[i] = strconv.Itoa(i)
		}
		users := &blockingContextualDataSource{arrived: make(chan struct{}, groups)}
		var loads, finishes atomic.Int64
		operation, cancel := context.WithCancel(t.Context())
		ctx := NewContext(operation)
		ctx.SetEngineLoaderHooks(&spyLoaderHooks{
			onLoad: func(ctx context.Context, _ DataSourceInfo) context.Context {
				loads.Add(1)
				return ctx
			},
			onFinished: func(context.Context, DataSourceInfo, *ResponseInfo) { finishes.Add(1) },
		})
		done := make(chan string, 1)
		go func() {
			done <- loadContextual(t, ctx, contextualQueuesWithValues(values...), flaggedUsersFetch(contextualUsersFetch(1, users, queueContextualCapture)))
		}()

		for range maxConcurrentContextualArgumentGroupLoads {
			<-users.arrived
		}
		cancel()
		var out string
		select {
		case out = <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("the loader did not return after the cancellation")
		}

		assert.Empty(t, users.arrived, "no group starts after the cancellation")
		assert.Equal(t, int64(maxConcurrentContextualArgumentGroupLoads), loads.Load())
		assert.Equal(t, int64(maxConcurrentContextualArgumentGroupLoads), finishes.Load())
		assert.Equal(t, maxConcurrentContextualArgumentGroupLoads, strings.Count(out, `{"message":"Failed to fetch from Subgraph 'users' at Path 'moderationQueues.@.flaggedUsers'."}`))
		assert.Equal(t, maxConcurrentContextualArgumentGroupLoads+1, strings.Count(out, `"message"`))
		assert.Contains(t, out, `Reason: 4 requests for distinct contextual argument values were not sent because the operation was canceled or timed out.`)
	})

	t.Run("an operation canceled before its groups start sends none and skips the dependents", func(t *testing.T) {
		users := newContextualUsersDataSource("contextualArgument_0")
		dependent := newContextualUsersDataSource()
		dependentFetch := contextualUsersFetch(2, dependent)
		dependentFetch.DependsOnFetchIDs = []int{1}
		operation, cancel := context.WithCancel(t.Context())
		cancel()
		ctx := NewContext(operation)

		out := loadContextual(t, ctx, contextualQueuesWithValues("1", "2", "3"), Sequence(
			flaggedUsersFetch(contextualUsersFetch(1, users, queueContextualCapture)),
			flaggedUsersFetch(dependentFetch),
		))

		assert.Empty(t, users.recordedRequests())
		assert.Empty(t, dependent.recordedRequests())
		assert.Equal(t, `{"errors":[{"message":"Failed to fetch from Subgraph 'users' at Path 'moderationQueues.@.flaggedUsers', Reason: 3 requests for distinct contextual argument values were not sent because the operation was canceled or timed out."}],`+
			`"data":`+contextualQueuesWithValues("1", "2", "3")+`}`, out)
		assert.ErrorIs(t, ctx.SubgraphErrors(), context.Canceled)
	})
}

func TestContextualArgumentHooksAndTraces(t *testing.T) {
	var mu sync.Mutex
	loads, finishes := map[string]int{}, map[string]int{}
	ctx := NewContext(t.Context())
	ctx.SetEngineLoaderHooks(&spyLoaderHooks{
		onLoad: func(ctx context.Context, ds DataSourceInfo) context.Context {
			mu.Lock()
			defer mu.Unlock()
			loads[ds.Name]++
			return ctx
		},
		onFinished: func(_ context.Context, ds DataSourceInfo, info *ResponseInfo) {
			mu.Lock()
			defer mu.Unlock()
			finishes[ds.Name]++
			assert.NoError(t, info.Err)
		},
	})
	ctx.TracingOptions = TraceOptions{Enable: true, ExcludeLoadStats: true, EnablePredictableDebugTimings: true}
	users := newContextualUsersDataSource("contextualArgument_0")
	fetch := contextualUsersFetch(1, users, queueContextualCapture)
	node := flaggedUsersFetch(fetch)

	loadContextual(t, ctx, contextualQueuesData, node)

	assert.Equal(t, map[string]int{"users": 2}, loads)
	assert.Equal(t, map[string]int{"users": 2}, finishes)
	require.Len(t, fetch.Traces, 2)
	assert.Same(t, fetch.Traces[0], fetch.Trace)
	assert.Contains(t, string(fetch.Traces[0].Input), `"contextualArgument_0":true`)
	assert.Contains(t, string(fetch.Traces[1].Input), `"contextualArgument_0":false`)
	assert.JSONEq(t, `[{"__typename":"User","id":"u2"},{"__typename":"User","id":"u3"}]`, string(fetch.Traces[1].RawInputData))
	assert.Equal(t, fetch.Traces, node.Trace().Fetch.Traces)
}

func TestContextualArgumentTraces(t *testing.T) {
	tracingContext := func(t *testing.T) *Context {
		ctx := NewContext(t.Context())
		ctx.TracingOptions = TraceOptions{Enable: true, ExcludeLoadStats: true, EnablePredictableDebugTimings: true}
		return ctx
	}

	t.Run("a single group traces only the entities it sends", func(t *testing.T) {
		users := newContextualUsersDataSource("contextualArgument_0")
		fetch := contextualUsersFetch(1, users, queueContextualCapture)
		data := `{"moderationQueues":[` +
			`{"__context_0":true,"flaggedUsers":[{"__typename":"User","id":"u1"}]},` +
			`{"flaggedUsers":[{"__typename":"User","id":"u2"}]}]}`

		loadContextual(t, tracingContext(t), data, flaggedUsersFetch(fetch))

		assert.Len(t, users.recordedRequests(), 1)
		require.NotNil(t, fetch.Trace)
		assert.Nil(t, fetch.Traces)
		assert.Contains(t, string(fetch.Trace.Input), `"representations":[{"__typename":"User","id":"u1"}]`)
		assert.JSONEq(t, `{"__typename":"User","id":"u1"}`, string(fetch.Trace.RawInputData))
	})

	t.Run("a fetch over the group limit traces the entities it skips", func(t *testing.T) {
		users := newContextualUsersDataSource("contextualArgument_0")
		fetch := contextualUsersFetch(1, users, queueContextualCapture)
		loader := newContextualLoader(contextualQueuesWithValues("1", "2", "3"))
		loader.maxContextualArgumentGroups = 2

		runContextualLoader(t, loader, tracingContext(t), flaggedUsersFetch(fetch))

		assert.Empty(t, users.recordedRequests())
		require.NotNil(t, fetch.Trace)
		assert.Nil(t, fetch.Traces)
		assert.True(t, fetch.Trace.LoadSkipped)
		assert.Equal(t, "moderationQueues.@.flaggedUsers", fetch.Trace.Path)
		assert.JSONEq(t, `[{"__typename":"User","id":"u0"},{"__typename":"User","id":"u1"},{"__typename":"User","id":"u2"}]`, string(fetch.Trace.RawInputData))
	})

	t.Run("a fetch whose entities all miss their value traces them as skipped", func(t *testing.T) {
		users := newContextualUsersDataSource("contextualArgument_0")
		fetch := contextualUsersFetch(1, users, queueContextualCapture)

		out := loadContextual(t, tracingContext(t), `{"moderationQueues":[{"flaggedUsers":[{"__typename":"User","id":"u1"}]}]}`, flaggedUsersFetch(fetch))

		assert.Empty(t, users.recordedRequests())
		assert.Contains(t, out, `Reason: 1 entity was not fetched because the context value of its 'ModerationQueue' ancestor is missing.`)
		require.NotNil(t, fetch.Trace)
		assert.True(t, fetch.Trace.LoadSkipped)
		assert.JSONEq(t, `{"__typename":"User","id":"u1"}`, string(fetch.Trace.RawInputData))
	})
}

func TestContextualArgumentResponseCache(t *testing.T) {
	cachedContext := func(t *testing.T, cache *testCache) *Context {
		ctx := NewContext(t.Context())
		ctx.SetResponseCache(ResponseCacheOptions{Store: cache, DefaultTTL: time.Minute})
		return ctx
	}
	cachedUsers := func() *contextualEntitiesDataSource {
		users := newContextualUsersDataSource("contextualArgument_0")
		users.cacheControl = "max-age=60"
		return users
	}

	t.Run("one representation under two values gives two keys and no cross hit", func(t *testing.T) {
		cache := newTestCache()
		users := cachedUsers()
		data := contextualQueuesWithValues("true", "false")
		data = strings.ReplaceAll(data, `"id":"u1"`, `"id":"u0"`)

		first := loadContextual(t, cachedContext(t, cache), data, flaggedUsersFetch(contextualUsersFetch(1, users, queueContextualCapture)))
		second := loadContextual(t, cachedContext(t, cache), data, flaggedUsersFetch(contextualUsersFetch(1, users, queueContextualCapture)))

		assert.ElementsMatch(t, []string{
			contextualUsersRequest(`,"contextualArgument_0":true`, "u0"),
			contextualUsersRequest(`,"contextualArgument_0":false`, "u0"),
		}, users.recordedRequests())
		assert.Len(t, cache.items, 2)
		assert.Equal(t, first, second)
		assert.Contains(t, second, `{"__context_0":true,"flaggedUsers":[{"__typename":"User","id":"u0","profile":"u0:true"}]}`)
		assert.Contains(t, second, `{"__context_0":false,"flaggedUsers":[{"__typename":"User","id":"u0","profile":"u0:false"}]}`)
	})

	t.Run("a group without a setter and a setter with an explicit null share a key", func(t *testing.T) {
		capture := ContextualArgumentCapture{
			VariableName: "contextualArgument_0",
			Levels: []ContextualArgumentLevel{{Depth: 1, MatchByTypename: true, Setters: []ContextualArgumentSetter{
				{TypeName: "ModerationQueue", Path: []string{"__context_0"}},
			}}},
		}
		cache := newTestCache()
		users := cachedUsers()

		loadContextual(t, cachedContext(t, cache), `{"moderationQueues":[{"__typename":"ModerationQueue","__context_0":null,"flaggedUsers":[{"__typename":"User","id":"u1"}]}]}`,
			flaggedUsersFetch(contextualUsersFetch(1, users, capture)))
		out := loadContextual(t, cachedContext(t, cache), `{"moderationQueues":[{"__typename":"AppealQueue","flaggedUsers":[{"__typename":"User","id":"u1"}]}]}`,
			flaggedUsersFetch(contextualUsersFetch(1, users, capture)))

		assert.Equal(t, []string{contextualUsersRequest(`,"contextualArgument_0":null`, "u1")}, users.recordedRequests())
		assert.Equal(t, `{"data":{"moderationQueues":[{"__typename":"AppealQueue","flaggedUsers":[{"__typename":"User","id":"u1","profile":"u1:null"}]}]}}`, out)
	})

	t.Run("a partially warm fetch sends only its cold groups", func(t *testing.T) {
		cache := newTestCache()
		users := cachedUsers()
		warm := `{"moderationQueues":[{"__context_0":true,"flaggedUsers":[{"__typename":"User","id":"u1"}]}]}`
		mixed := `{"moderationQueues":[{"__context_0":true,"flaggedUsers":[{"__typename":"User","id":"u1"}]},{"__context_0":false,"flaggedUsers":[{"__typename":"User","id":"u1"}]}]}`

		loadContextual(t, cachedContext(t, cache), warm, flaggedUsersFetch(contextualUsersFetch(1, users, queueContextualCapture)))
		out := loadContextual(t, cachedContext(t, cache), mixed, flaggedUsersFetch(contextualUsersFetch(1, users, queueContextualCapture)))

		assert.Equal(t, []string{
			contextualUsersRequest(`,"contextualArgument_0":true`, "u1"),
			contextualUsersRequest(`,"contextualArgument_0":false`, "u1"),
		}, users.recordedRequests())
		assert.Equal(t, `{"data":{"moderationQueues":[`+
			`{"__context_0":true,"flaggedUsers":[{"__typename":"User","id":"u1","profile":"u1:true"}]},`+
			`{"__context_0":false,"flaggedUsers":[{"__typename":"User","id":"u1","profile":"u1:false"}]}]}}`, out)
	})

}

func TestContextualArgumentSubgraphSingleflight(t *testing.T) {
	t.Run("two concurrent requests whose setters differ make two upstream calls", func(t *testing.T) {
		// The first request holds its flight until the second request reaches the subgraph, which it only does with its own key.
		release := make(chan struct{})
		arrived := make(chan struct{}, 2)
		var once sync.Once
		users := newContextualUsersDataSource("contextualArgument_0")
		users.onRequest = func(string) {
			arrived <- struct{}{}
			once.Do(func() { <-release })
		}
		singleFlight := NewSingleFlight(1)
		outputs := make([]string, 2)
		var wg sync.WaitGroup
		run := func(i int, value string) {
			wg.Go(func() {
				loader := newContextualLoader(`{"moderationQueues":[{"__context_0":` + value + `,"flaggedUsers":[{"__typename":"User","id":"u1"}]}]}`)
				loader.singleFlight = singleFlight
				outputs[i] = runContextualLoader(t, loader, NewContext(t.Context()), flaggedUsersFetch(contextualUsersFetch(1, users, queueContextualCapture)))
			})
		}

		run(0, "true")
		<-arrived
		run(1, "false")
		select {
		case <-arrived:
		case <-time.After(10 * time.Second):
			t.Fatal("the second request waited for the first one's flight")
		}
		close(release)
		wg.Wait()

		assert.ElementsMatch(t, []string{
			contextualUsersRequest(`,"contextualArgument_0":true`, "u1"),
			contextualUsersRequest(`,"contextualArgument_0":false`, "u1"),
		}, users.recordedRequests())
		assert.Contains(t, outputs[0], `"profile":"u1:true"`)
		assert.Contains(t, outputs[1], `"profile":"u1:false"`)
	})

}

func TestContextualArgumentVariableWithoutGroupValues(t *testing.T) {
	segment := (&ContextualArgumentVariable{Capture: queueContextualCapture}).TemplateSegment()

	t.Run("a single fetch fails to render its input", func(t *testing.T) {
		users := newContextualUsersDataSource("contextualArgument_0")
		fetch := &SingleFetch{
			FetchConfiguration: FetchConfiguration{
				DataSource:     users,
				PostProcessing: PostProcessingConfiguration{SelectResponseDataPath: []string{"data"}, SelectResponseErrorsPath: []string{"errors"}},
			},
			InputTemplate: InputTemplate{Segments: []TemplateSegment{contextualStatic(`{"method":"POST","url":"http://users","body":{"variables":{"a":`), segment, contextualStatic(`}}}`)}},
			Info:          contextualUsersInfo(),
		}

		loader := newContextualLoader(`{"moderationQueue":{"__context_0":true}}`)
		loader.propagateSubgraphErrors = true

		out := runContextualLoader(t, loader, NewContext(t.Context()), SingleWithPath(fetch, "moderationQueue", ObjectPath("moderationQueue")))

		assert.Empty(t, users.recordedRequests())
		assert.Equal(t, `{"errors":[{"message":"Failed to fetch from Subgraph 'users' at Path 'moderationQueue'.","extensions":{"errors":[{"message":"Failed to render Fetch Input","path":[]}]}}],`+
			`"data":{"moderationQueue":{"__context_0":true}}}`, out)
	})

	t.Run("a multi entity fetch entry fails the fetch", func(t *testing.T) {
		f := multiFetchFixture(t, withEntry2Vars(MultiEntityFetchVariable{
			KeyPrefix: []byte(`,"contextualArgument_0_f2":`),
			Value:     InputTemplate{Segments: []TemplateSegment{segment}},
		}))

		_, err := f.loader.preparePhase(f.item)
		require.ErrorContains(t, err, "no value for the contextual argument variable [contextualArgument_0]")
	})
}

func TestContextualArgumentSubscription(t *testing.T) {
	t.Run("each event sends the value of its own payload", func(t *testing.T) {
		values := []string{"true", "false", "true"}
		stream := createFakeStream(func(counter int) (string, bool) {
			return `{"data":{"queueUpdated":{"id":"q1","__context_0":` + values[counter] + `,"flaggedUsers":[{"__typename":"User","id":"u1"}]}}}`, counter == len(values)-1
		}, time.Millisecond, nil, nil)
		users := newContextualUsersDataSource("contextualArgument_0")
		profiles := contextualUsersFetch(1, users, staticContextualCapture("contextualArgument_0", 1, "ModerationQueue", "__context_0"))
		profiles.DependsOnFetchIDs = []int{0}
		fetches := Sequence(SingleWithPath(profiles, "queueUpdated.flaggedUsers", ObjectPath("queueUpdated"), ArrayPath("flaggedUsers")))
		fetches.Trigger = &FetchTreeNode{
			Kind: FetchTreeNodeKindTrigger,
			Item: &FetchItem{
				Fetch:        &SingleFetch{Info: &FetchInfo{DataSourceID: "0", DataSourceName: "moderation", QueryPlan: &QueryPlan{Query: "subscription {queueUpdated}"}}},
				ResponsePath: "queueUpdated",
			},
		}
		plan := &GraphQLSubscription{
			Trigger: GraphQLSubscriptionTrigger{
				Source:         stream,
				InputTemplate:  InputTemplate{Segments: []TemplateSegment{contextualStatic(`{"method":"POST","url":"http://moderation","body":{"query":"subscription {queueUpdated}"}}`)}},
				PostProcessing: PostProcessingConfiguration{SelectResponseDataPath: []string{"data"}, SelectResponseErrorsPath: []string{"errors"}},
			},
			Response: &GraphQLResponse{
				Data: &Object{Fields: []*Field{{
					Name: []byte("queueUpdated"),
					Value: &Object{Path: []string{"queueUpdated"}, Fields: []*Field{
						contextualStringField("id"),
						contextualListField("flaggedUsers", contextualStringField("id"), contextualStringField("profile")),
					}},
					Info: &FieldInfo{Name: "queueUpdated", ExactParentTypeName: "Subscription", Source: TypeFieldSource{IDs: []string{"0"}, Names: []string{"moderation"}}},
				}}},
				Fetches: fetches,
			},
		}
		recorder := &SubscriptionRecorder{buf: &bytes.Buffer{}, messages: []string{}}

		err := newResolver(t.Context()).AsyncResolveGraphQLSubscription(&Context{ctx: context.Background()}, plan, recorder, SubscriptionIdentifier{ConnectionID: 1, SubscriptionID: 1})
		require.NoError(t, err)
		recorder.AwaitComplete(t, 30*time.Second)

		assert.Equal(t, []string{
			`{"data":{"queueUpdated":{"id":"q1","flaggedUsers":[{"id":"u1","profile":"u1:true"}]}}}`,
			`{"data":{"queueUpdated":{"id":"q1","flaggedUsers":[{"id":"u1","profile":"u1:false"}]}}}`,
			`{"data":{"queueUpdated":{"id":"q1","flaggedUsers":[{"id":"u1","profile":"u1:true"}]}}}`,
		}, recorder.Messages())
		assert.Equal(t, []string{
			contextualUsersRequest(`,"contextualArgument_0":true`, "u1"),
			contextualUsersRequest(`,"contextualArgument_0":false`, "u1"),
			contextualUsersRequest(`,"contextualArgument_0":true`, "u1"),
		}, users.recordedRequests())
	})
}

func TestContextualArgumentSharedTargetCopies(t *testing.T) {
	seed := `{"employees":[{"__typename":"Employee","id":1},{"__typename":"Employee","id":1}],"employee":{"__typename":"Employee","id":9}}`
	productsOf := func(loader *Loader, i int) *astjson.Value {
		return loader.dataBuffer.Get().Get("employees", strconv.Itoa(i), "products")
	}

	for _, copies := range []bool{false, true} {
		f := multiFetchFixture(t, withSeed(seed))
		f.fetch.Input.Entries[0].DeepCopyForSharedTargets = copies
		f.fetch.DataSource = &recordingDataSource{response: []byte(`{"data":{"f1":[{"products":[{"upc":"1"}]}],"f2":[{"notes":"n"}]}}`)}

		require.NoError(t, f.loader.resolveSingle(t.Context(), f.item))

		assert.JSONEq(t, `[{"upc":"1"}]`, string(productsOf(f.loader, 1).MarshalTo(nil)))
		assert.Equal(t, !copies, productsOf(f.loader, 0) == productsOf(f.loader, 1), "a multi entity entry shares its merged nodes unless it copies them, copies: %v", copies)
	}
}
