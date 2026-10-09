package engine

import (
	"net/http"
	"testing"
	"time"

	"github.com/jensneuse/abstractlogger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wundergraph/graphql-go-tools/execution/graphql"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/datasource/graphql_datasource"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

// The users of T1 come from the things subgraph and the users of T2 from the details subgraph,
// so the two names fetches below them are equal but depend on different fetches.
const deduplicateFetchesSchema = `
	type Query {
		things: [Thing!]!
	}
	interface Thing {
		id: ID!
	}
	type T1 implements Thing {
		id: ID!
		users: [User!]!
	}
	type T2 implements Thing {
		id: ID!
		users: [User!]!
	}
	type User {
		id: ID!
		name: String
	}
`

const deduplicateFetchesThingsSDL = `
	type Query {
		things: [Thing!]!
	}
	interface Thing {
		id: ID!
	}
	type T1 implements Thing @key(fields: "id") {
		id: ID!
		users: [User!]!
	}
	type T2 implements Thing @key(fields: "id") {
		id: ID!
	}
	type User @key(fields: "id", resolvable: false) {
		id: ID!
	}
`

const deduplicateFetchesDetailsSDL = `
	type T2 @key(fields: "id") {
		id: ID!
		users: [User!]!
	}
	type User @key(fields: "id", resolvable: false) {
		id: ID!
	}
`

const deduplicateFetchesNamesSDL = `
	type User @key(fields: "id") {
		id: ID!
		name: String
	}
`

func TestExecutionEngine_DeduplicatedFetchesWaitForTheParentsOfEveryDuplicate(t *testing.T) {
	const namesBody = `{"query":"query($representations: [_Any!]!){_entities(representations: $representations){... on User {__typename name}}}","variables":{"representations":[{"__typename":"User","id":"u1"},{"__typename":"User","id":"u2"}]}}`

	for _, mode := range []struct {
		name            string
		scheduleFetches bool
	}{
		{name: "scheduler off"},
		{name: "scheduler on", scheduleFetches: true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			namesRec := &multiFetchRecorder{}

			things := mustGraphqlDataSourceConfiguration(t,
				"things",
				mustFactory(t, recordingClient(t, nil, "things", "/", map[string]sendResponse{
					`{"query":"{things {__typename ... on T1 {users {__typename id}} ... on T2 {__typename id}}}"}`: {
						statusCode: 200,
						body:       `{"data":{"things":[{"__typename":"T1","users":[{"__typename":"User","id":"u1"}]},{"__typename":"T2","id":"t2"}]}}`,
					},
				})),
				&plan.DataSourceMetadata{
					RootNodes: []plan.TypeField{
						{TypeName: "Query", FieldNames: []string{"things"}},
						{TypeName: "T1", FieldNames: []string{"id", "users"}},
						{TypeName: "T2", FieldNames: []string{"id"}},
						{TypeName: "User", FieldNames: []string{"id"}},
					},
					ChildNodes: []plan.TypeField{
						{TypeName: "Thing", FieldNames: []string{"id"}},
					},
					FederationMetaData: plan.FederationMetaData{
						Keys: plan.FederationFieldConfigurations{
							{TypeName: "T1", SelectionSet: "id"},
							{TypeName: "T2", SelectionSet: "id"},
							{TypeName: "User", SelectionSet: "id", DisableEntityResolver: true},
						},
					},
				},
				mustConfiguration(t, graphql_datasource.ConfigurationInput{
					Fetch: &graphql_datasource.FetchConfiguration{URL: "http://things/", Method: "POST"},
					SchemaConfiguration: mustSchemaConfig(t,
						&graphql_datasource.FederationConfiguration{Enabled: true, ServiceSDL: deduplicateFetchesThingsSDL},
						deduplicateFetchesThingsSDL,
					),
				}),
			)

			detailsClient := recordingClient(t, nil, "details", "/", map[string]sendResponse{
				`{"query":"query($representations: [_Any!]!){_entities(representations: $representations){... on T2 {__typename users {__typename id}}}}","variables":{"representations":[{"__typename":"T2","id":"t2"}]}}`: {
					statusCode: 200,
					body:       `{"data":{"_entities":[{"__typename":"T2","users":[{"__typename":"User","id":"u2"}]}]}}`,
				},
			})
			// A names fetch that does not wait for details runs while details is still loading.
			detailsTransport := detailsClient.Transport
			detailsClient.Transport = testRoundTripper(func(req *http.Request) *http.Response {
				time.Sleep(50 * time.Millisecond)
				res, err := detailsTransport.RoundTrip(req)
				assert.NoError(t, err)
				return res
			})

			details := mustGraphqlDataSourceConfiguration(t,
				"details",
				mustFactory(t, detailsClient),
				&plan.DataSourceMetadata{
					RootNodes: []plan.TypeField{
						{TypeName: "T2", FieldNames: []string{"id", "users"}},
						{TypeName: "User", FieldNames: []string{"id"}},
					},
					FederationMetaData: plan.FederationMetaData{
						Keys: plan.FederationFieldConfigurations{
							{TypeName: "T2", SelectionSet: "id"},
							{TypeName: "User", SelectionSet: "id", DisableEntityResolver: true},
						},
					},
				},
				mustConfiguration(t, graphql_datasource.ConfigurationInput{
					Fetch: &graphql_datasource.FetchConfiguration{URL: "http://details/", Method: "POST"},
					SchemaConfiguration: mustSchemaConfig(t,
						&graphql_datasource.FederationConfiguration{Enabled: true, ServiceSDL: deduplicateFetchesDetailsSDL},
						deduplicateFetchesDetailsSDL,
					),
				}),
			)

			names := mustGraphqlDataSourceConfiguration(t,
				"names",
				mustFactory(t, recordingClient(t, namesRec, "names", "/", map[string]sendResponse{
					namesBody: {
						statusCode: 200,
						body:       `{"data":{"_entities":[{"__typename":"User","name":"n1"},{"__typename":"User","name":"n2"}]}}`,
					},
				})),
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
					Fetch: &graphql_datasource.FetchConfiguration{URL: "http://names/", Method: "POST"},
					SchemaConfiguration: mustSchemaConfig(t,
						&graphql_datasource.FederationConfiguration{Enabled: true, ServiceSDL: deduplicateFetchesNamesSDL},
						deduplicateFetchesNamesSDL,
					),
				}),
			)

			schema, err := graphql.NewSchemaFromString(deduplicateFetchesSchema)
			require.NoError(t, err)

			engineConf := NewConfiguration(schema)
			engineConf.SetDataSources([]plan.DataSource{things, details, names})
			if mode.scheduleFetches {
				engineConf.EnableScheduleFetches()
			}

			engine, err := NewExecutionEngine(t.Context(), abstractlogger.Noop{}, engineConf, resolve.ResolverOptions{MaxConcurrency: 1024})
			require.NoError(t, err)

			operation := graphql.Request{Query: `{ things { ... on T1 { users { name } } ... on T2 { users { name } } } }`}
			resultWriter := graphql.NewEngineResultWriter()
			require.NoError(t, engine.Execute(t.Context(), &operation, &resultWriter))

			assert.Equal(t, `{"data":{"things":[{"users":[{"name":"n1"}]},{"users":[{"name":"n2"}]}]}}`, resultWriter.String())
			assert.Equal(t, []string{namesBody}, namesRec.requests())
		})
	}
}
