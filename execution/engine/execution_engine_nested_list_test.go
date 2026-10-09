package engine

import (
	"context"
	"testing"

	"github.com/jensneuse/abstractlogger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wundergraph/graphql-go-tools/execution/graphql"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/datasource/graphql_datasource"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

const nestedListSchema = `
	type Query {
		pages: [[User!]!]!
		shelves: [[[User!]!]!]!
		sparsePages: [[User]]
		queues: [Queue!]!
		queueGrid: [[Queue!]!]!
	}
	type Queue {
		pages: [[User!]!]!
		users: [User!]!
	}
	type User {
		id: ID!
		name: String
	}
`

const nestedListModerationSDL = `
	type Query {
		pages: [[User!]!]!
		shelves: [[[User!]!]!]!
		sparsePages: [[User]]
		queues: [Queue!]!
		queueGrid: [[Queue!]!]!
	}
	type Queue {
		pages: [[User!]!]!
		users: [User!]!
	}
	type User @key(fields: "id", resolvable: false) {
		id: ID!
	}
`

const nestedListUsersSDL = `
	type User @key(fields: "id") {
		id: ID!
		name: String
	}
`

const nestedListUsersBodyPrefix = `{"query":"query($representations: [_Any!]!){_entities(representations: $representations){... on User {__typename name}}}","variables":{"representations":[`

func nestedListDataSources(t *testing.T, usersRec *multiFetchRecorder, moderationResponses, usersResponses map[string]sendResponse) []plan.DataSource {
	t.Helper()

	moderation := mustGraphqlDataSourceConfiguration(t,
		"moderation",
		mustFactory(t, recordingClient(t, nil, "moderation", "/", moderationResponses)),
		&plan.DataSourceMetadata{
			RootNodes: []plan.TypeField{
				{TypeName: "Query", FieldNames: []string{"pages", "shelves", "sparsePages", "queues", "queueGrid"}},
				{TypeName: "User", FieldNames: []string{"id"}},
			},
			ChildNodes: []plan.TypeField{
				{TypeName: "Queue", FieldNames: []string{"pages", "users"}},
			},
			FederationMetaData: plan.FederationMetaData{
				Keys: plan.FederationFieldConfigurations{
					{TypeName: "User", SelectionSet: "id", DisableEntityResolver: true},
				},
			},
		},
		mustConfiguration(t, graphql_datasource.ConfigurationInput{
			Fetch: &graphql_datasource.FetchConfiguration{URL: "http://moderation/", Method: "POST"},
			SchemaConfiguration: mustSchemaConfig(t,
				&graphql_datasource.FederationConfiguration{Enabled: true, ServiceSDL: nestedListModerationSDL},
				nestedListModerationSDL,
			),
		}),
	)

	users := mustGraphqlDataSourceConfiguration(t,
		"users",
		mustFactory(t, recordingClient(t, usersRec, "users", "/", usersResponses)),
		&plan.DataSourceMetadata{
			RootNodes: []plan.TypeField{
				{TypeName: "User", FieldNames: []string{"id", "name"}},
			},
			FederationMetaData: plan.FederationMetaData{
				Keys: plan.FederationFieldConfigurations{
					{TypeName: "User", SelectionSet: "id"},
				},
			},
		},
		mustConfiguration(t, graphql_datasource.ConfigurationInput{
			Fetch: &graphql_datasource.FetchConfiguration{URL: "http://users/", Method: "POST"},
			SchemaConfiguration: mustSchemaConfig(t,
				&graphql_datasource.FederationConfiguration{Enabled: true, ServiceSDL: nestedListUsersSDL},
				nestedListUsersSDL,
			),
		}),
	)

	return []plan.DataSource{moderation, users}
}

func TestExecutionEngine_NestedListEntities(t *testing.T) {
	testCases := []struct {
		name               string
		query              string
		multiFetch         bool
		moderationBody     string
		moderationResponse string
		usersBody          string
		usersResponse      string
		expectedResponse   string
	}{
		{
			name:               "list of lists",
			query:              `{ pages { id name } }`,
			moderationBody:     `{"query":"{pages {id __typename}}"}`,
			moderationResponse: `{"data":{"pages":[[{"id":"1","__typename":"User"},{"id":"2","__typename":"User"}],[],[{"id":"2","__typename":"User"}]]}}`,
			usersBody:          nestedListUsersBodyPrefix + `{"__typename":"User","id":"1"},{"__typename":"User","id":"2"}]}}`,
			usersResponse:      `{"data":{"_entities":[{"__typename":"User","name":"Ada"},{"__typename":"User","name":"Bo"}]}}`,
			expectedResponse:   `{"data":{"pages":[[{"id":"1","name":"Ada"},{"id":"2","name":"Bo"}],[],[{"id":"2","name":"Bo"}]]}}`,
		},
		{
			name:               "list of lists of lists",
			query:              `{ shelves { id name } }`,
			moderationBody:     `{"query":"{shelves {id __typename}}"}`,
			moderationResponse: `{"data":{"shelves":[[[{"id":"1","__typename":"User"}],[{"id":"2","__typename":"User"}]],[[{"id":"3","__typename":"User"}]]]}}`,
			usersBody:          nestedListUsersBodyPrefix + `{"__typename":"User","id":"1"},{"__typename":"User","id":"2"},{"__typename":"User","id":"3"}]}}`,
			usersResponse:      `{"data":{"_entities":[{"__typename":"User","name":"Ada"},{"__typename":"User","name":"Bo"},{"__typename":"User","name":"Cy"}]}}`,
			expectedResponse:   `{"data":{"shelves":[[[{"id":"1","name":"Ada"}],[{"id":"2","name":"Bo"}]],[[{"id":"3","name":"Cy"}]]]}}`,
		},
		{
			name:               "nulls inside a list of lists",
			query:              `{ sparsePages { id name } }`,
			moderationBody:     `{"query":"{sparsePages {id __typename}}"}`,
			moderationResponse: `{"data":{"sparsePages":[null,[{"id":"1","__typename":"User"},null],[],[{"id":"3","__typename":"User"}]]}}`,
			usersBody:          nestedListUsersBodyPrefix + `{"__typename":"User","id":"1"},{"__typename":"User","id":"3"}]}}`,
			usersResponse:      `{"data":{"_entities":[{"__typename":"User","name":"Ada"},{"__typename":"User","name":"Cy"}]}}`,
			expectedResponse:   `{"data":{"sparsePages":[null,[{"id":"1","name":"Ada"},null],[],[{"id":"3","name":"Cy"}]]}}`,
		},
		{
			name:               "list of lists under a list",
			query:              `{ queues { pages { id name } } }`,
			moderationBody:     `{"query":"{queues {pages {id __typename}}}"}`,
			moderationResponse: `{"data":{"queues":[{"pages":[[{"id":"1","__typename":"User"}],[{"id":"2","__typename":"User"}]]},{"pages":[[{"id":"3","__typename":"User"}]]}]}}`,
			usersBody:          nestedListUsersBodyPrefix + `{"__typename":"User","id":"1"},{"__typename":"User","id":"2"},{"__typename":"User","id":"3"}]}}`,
			usersResponse:      `{"data":{"_entities":[{"__typename":"User","name":"Ada"},{"__typename":"User","name":"Bo"},{"__typename":"User","name":"Cy"}]}}`,
			expectedResponse:   `{"data":{"queues":[{"pages":[[{"id":"1","name":"Ada"}],[{"id":"2","name":"Bo"}]]},{"pages":[[{"id":"3","name":"Cy"}]]}]}}`,
		},
		{
			name:               "list under a list of lists",
			query:              `{ queueGrid { users { id name } } }`,
			moderationBody:     `{"query":"{queueGrid {users {id __typename}}}"}`,
			moderationResponse: `{"data":{"queueGrid":[[{"users":[{"id":"1","__typename":"User"},{"id":"2","__typename":"User"}]}],[{"users":[{"id":"3","__typename":"User"}]}]]}}`,
			usersBody:          nestedListUsersBodyPrefix + `{"__typename":"User","id":"1"},{"__typename":"User","id":"2"},{"__typename":"User","id":"3"}]}}`,
			usersResponse:      `{"data":{"_entities":[{"__typename":"User","name":"Ada"},{"__typename":"User","name":"Bo"},{"__typename":"User","name":"Cy"}]}}`,
			expectedResponse:   `{"data":{"queueGrid":[[{"users":[{"id":"1","name":"Ada"},{"id":"2","name":"Bo"}]}],[{"users":[{"id":"3","name":"Cy"}]}]]}}`,
		},
		{
			name:               "list of lists merged with a list into a multi fetch",
			query:              `{ pages { id name } queues { users { id name } } }`,
			multiFetch:         true,
			moderationBody:     `{"query":"{pages {id __typename} queues {users {id __typename}}}"}`,
			moderationResponse: `{"data":{"pages":[[{"id":"1","__typename":"User"},{"id":"2","__typename":"User"}],[],[{"id":"2","__typename":"User"}]],"queues":[{"users":[{"id":"4","__typename":"User"}]},{"users":[{"id":"5","__typename":"User"}]}]}}`,
			usersBody:          `{"query":"query($representations_f1: [_Any!]!, $includeF1: Boolean!, $representations_f2: [_Any!]!, $includeF2: Boolean!){f1: _entities(representations: $representations_f1)@include(if: $includeF1) {... on User {__typename name}} f2: _entities(representations: $representations_f2)@include(if: $includeF2) {... on User {__typename name}}}","variables":{"representations_f1":[{"__typename":"User","id":"1"},{"__typename":"User","id":"2"}],"includeF1":true,"representations_f2":[{"__typename":"User","id":"4"},{"__typename":"User","id":"5"}],"includeF2":true}}`,
			usersResponse:      `{"data":{"f1":[{"__typename":"User","name":"Ada"},{"__typename":"User","name":"Bo"}],"f2":[{"__typename":"User","name":"Di"},{"__typename":"User","name":"Ed"}]}}`,
			expectedResponse:   `{"data":{"pages":[[{"id":"1","name":"Ada"},{"id":"2","name":"Bo"}],[],[{"id":"2","name":"Bo"}]],"queues":[{"users":[{"id":"4","name":"Di"}]},{"users":[{"id":"5","name":"Ed"}]}]}}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			usersRec := &multiFetchRecorder{}
			dataSources := nestedListDataSources(t, usersRec,
				map[string]sendResponse{tc.moderationBody: {statusCode: 200, body: tc.moderationResponse}},
				map[string]sendResponse{tc.usersBody: {statusCode: 200, body: tc.usersResponse}},
			)

			schema, err := graphql.NewSchemaFromString(nestedListSchema)
			require.NoError(t, err)
			engineConf := NewConfiguration(schema)
			engineConf.SetDataSources(dataSources)
			if tc.multiFetch {
				engineConf.EnableMultiFetch()
			}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			engine, err := NewExecutionEngine(ctx, abstractlogger.Noop{}, engineConf, resolve.ResolverOptions{MaxConcurrency: 1024})
			require.NoError(t, err)

			resultWriter := graphql.NewEngineResultWriter()
			require.NoError(t, engine.Execute(ctx, &graphql.Request{Query: tc.query}, &resultWriter))

			assert.Equal(t, []string{tc.usersBody}, usersRec.requests())
			assert.Equal(t, tc.expectedResponse, resultWriter.String())
		})
	}
}

func TestExecutionEngine_NestedListDefer(t *testing.T) {
	usersBody := nestedListUsersBodyPrefix + `{"__typename":"User","id":"1"},{"__typename":"User","id":"2"},{"__typename":"User","id":"3"}]}}`
	usersRec := &multiFetchRecorder{}
	dataSources := nestedListDataSources(t, usersRec,
		map[string]sendResponse{`{"query":"{pages {id __typename}}"}`: {statusCode: 200, body: `{"data":{"pages":[[{"id":"1","__typename":"User"},{"id":"2","__typename":"User"}],[],[{"id":"3","__typename":"User"}]]}}`}},
		map[string]sendResponse{usersBody: {statusCode: 200, body: `{"data":{"_entities":[{"__typename":"User","name":"Ada"},{"__typename":"User","name":"Bo"},{"__typename":"User","name":"Cy"}]}}`}},
	)
	schema, err := graphql.NewSchemaFromString(nestedListSchema)
	require.NoError(t, err)

	t.Run("defer inside a list of lists", runWithoutError(ExecutionEngineTestCase{
		schema: schema,
		operation: func(t *testing.T) graphql.Request {
			return graphql.Request{Query: `{ pages { id ... @defer { name } } }`}
		},
		dataSources: dataSources,
		expectedResponse: `{"data":{"pages":[[{"id":"1"},{"id":"2"}],[],[{"id":"3"}]]},"pending":[{"id":"1","path":["pages"]}],"hasNext":true}
{"incremental":[{"data":{"name":"Ada"},"id":"1","subPath":[0,0]},{"data":{"name":"Bo"},"id":"1","subPath":[0,1]},{"data":{"name":"Cy"},"id":"1","subPath":[2,0]}],"completed":[{"id":"1"}],"hasNext":false}
`,
	}, withStreamingResponse()))
	assert.Equal(t, []string{usersBody}, usersRec.requests())
}
