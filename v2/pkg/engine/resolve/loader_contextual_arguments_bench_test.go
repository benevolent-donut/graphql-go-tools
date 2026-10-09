package resolve

import (
	"bytes"
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/datasource/httpclient"
)

// countedEntitiesDataSource answers an _entities request with the prebuilt response for its number of representations.
type countedEntitiesDataSource struct {
	responses map[int][]byte
}

var countedEntityRepresentation = []byte(`"__typename":"User"`)

func (d *countedEntitiesDataSource) Load(_ context.Context, _ http.Header, input []byte) ([]byte, error) {
	return d.responses[bytes.Count(input, countedEntityRepresentation)], nil
}

func (d *countedEntitiesDataSource) LoadWithFiles(ctx context.Context, headers http.Header, input []byte, _ []*httpclient.FileUpload) ([]byte, error) {
	return d.Load(ctx, headers, input)
}

func newCountedEntitiesDataSource(counts ...int) *countedEntitiesDataSource {
	d := &countedEntitiesDataSource{responses: make(map[int][]byte, len(counts))}
	for _, count := range counts {
		entities := make([]string, count)
		for i := range entities {
			entities[i] = `{"name":"n"}`
		}
		d.responses[count] = []byte(`{"data":{"_entities":[` + strings.Join(entities, ",") + `]}}`)
	}
	return d
}

// contextualBenchmarkPlan builds 100 queues with usersPerQueue distinct users each and the users fetch at [queues, flaggedUsers].
// values is the number of distinct context values, 0 for a fetch without captures; independent plans one fetch per queue.
func contextualBenchmarkPlan(usersPerQueue, values int, independent bool) *GraphQLResponse {
	const queueCount = 100
	queues := make([]string, queueCount)
	for i := range queues {
		users := make([]string, usersPerQueue)
		for j := range users {
			users[j] = `{"__typename":"User","id":"u` + strconv.Itoa(i*usersPerQueue+j) + `"}`
		}
		value := 0
		if values > 0 {
			value = i % values
		}
		queues[i] = `{"__context_0":` + strconv.Itoa(value) + `,"flaggedUsers":[` + strings.Join(users, ",") + `]}`
	}

	groupSize := queueCount * usersPerQueue
	if values > 0 {
		groupSize /= values
	}
	users := newCountedEntitiesDataSource(usersPerQueue, groupSize)
	fetch := func(fetchID int) *BatchEntityFetch {
		f := contextualUsersFetch(fetchID, users)
		f.DependsOnFetchIDs = []int{0}
		return f
	}

	root := Single(contextualRootFetch(`{"queues":[` + strings.Join(queues, ",") + `]}`))
	var fetches *FetchTreeNode
	switch {
	case independent:
		perQueue := make([]*FetchTreeNode, queueCount)
		for i := range perQueue {
			perQueue[i] = SingleWithPath(fetch(i+1), "queues.@.flaggedUsers", ObjectPath("queues", strconv.Itoa(i)), ArrayPath("flaggedUsers"))
		}
		fetches = Sequence(root, Parallel(perQueue...))
	case values > 0:
		capture := staticContextualCapture("contextualArgument_0", 1, "Queue", "__context_0")
		contextual := contextualUsersFetch(1, users, capture)
		contextual.DependsOnFetchIDs = []int{0}
		fetches = Sequence(root, SingleWithPath(contextual, "queues.@.flaggedUsers", ArrayPath("queues"), ArrayPath("flaggedUsers")))
	default:
		fetches = Sequence(root, SingleWithPath(fetch(1), "queues.@.flaggedUsers", ArrayPath("queues"), ArrayPath("flaggedUsers")))
	}

	return &GraphQLResponse{
		Info:    &GraphQLResponseInfo{OperationType: ast.OperationTypeQuery},
		Fetches: fetches,
		Data: &Object{Fields: []*Field{
			contextualListField("queues", contextualListField("flaggedUsers", contextualStringField("id"), contextualStringField("name"))),
		}},
	}
}

func BenchmarkContextualBatchEntityFetch(b *testing.B) {
	for _, bc := range []struct {
		name          string
		usersPerQueue int
		values        int
		independent   bool
	}{
		{name: "case=no_context/items=2000", usersPerQueue: 20},
		{name: "case=values_1/items=2000", usersPerQueue: 20, values: 1},
		{name: "case=values_10/items=2000", usersPerQueue: 20, values: 10},
		{name: "case=values_100/items=2000", usersPerQueue: 20, values: 100},
		{name: "case=independent_100_fetches/items=2000", usersPerQueue: 20, independent: true},
		{name: "case=no_context/items=4000", usersPerQueue: 40},
		{name: "case=values_100/items=4000", usersPerQueue: 40, values: 100},
		{name: "case=no_context/items=8000", usersPerQueue: 80},
		{name: "case=values_100/items=8000", usersPerQueue: 80, values: 100},
	} {
		b.Run(bc.name, func(b *testing.B) {
			response := contextualBenchmarkPlan(bc.usersPerQueue, bc.values, bc.independent)
			resolver := newResolver(b.Context())
			ctx := NewContext(context.Background())
			ctx.ExecutionOptions.DisableInboundRequestDeduplication = true
			out := &bytes.Buffer{}

			_, err := resolver.ArenaResolveGraphQLResponse(ctx, response, out)
			require.NoError(b, err)
			require.Equal(b, 100*bc.usersPerQueue, strings.Count(out.String(), `"name":"n"`))
			ctx.Free()

			b.ReportAllocs()
			for b.Loop() {
				out.Reset()
				ctx.ctx = context.Background()
				if _, err := resolver.ArenaResolveGraphQLResponse(ctx, response, out); err != nil {
					b.Fatal(err)
				}
				ctx.Free()
			}
		})
	}
}
