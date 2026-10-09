package engine

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jensneuse/abstractlogger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wundergraph/astjson"

	"github.com/wundergraph/graphql-go-tools/execution/graphql"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/datasource/graphql_datasource"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

const contextualArgumentsSchema = `
	type Query {
		moderationQueue(id: ID!): ModerationQueue
		moderationQueues: [ModerationQueue!]!
		queueGrid: [[ModerationQueue!]!]!
		queues: [Queue!]!
		scopes: [Scope!]!
		organization(id: ID!): Organization
		post(id: ID!): Post
		viewerIncludeSuspended: Boolean
	}

	type Mutation {
		flagUser(queueId: ID!, userId: ID!): ModerationQueue!
	}

	type Subscription {
		queueUpdated(id: ID!): ModerationQueue!
	}

	interface Queue {
		id: ID!
		includeSuspended: Boolean!
		flaggedUsers: [User!]!
	}

	type ModerationQueue implements Queue {
		id: ID!
		includeSuspended: Boolean!
		flaggedUsers: [User!]!
		flaggedUserPages: [[User!]!]!
		team: Team
		policyLevel: Int
		summary: String
	}

	type AppealQueue implements Queue {
		id: ID!
		includeSuspended: Boolean!
		showSuspended: Boolean
		flaggedUsers: [User!]!
	}

	type Team {
		id: ID!
		members: [User!]!
	}

	union Scope = ModerationQueue | AppealQueue | Team

	type Organization {
		id: ID!
		includeSuspended: Boolean
		queues: [ModerationQueue!]!
		allQueues: [Queue!]!
		members: [User!]!
	}

	type Post {
		id: ID!
		author: User!
		includeSuspended: Boolean
	}

	type User {
		id: ID!
		handle: String
		friends: [User!]!
		profile: Profile
		badge: String
		peers: String
		queueView: String
		scopeView: String
		viewerView: String
		digest(locale: String): String
		signature: String
	}

	type Profile {
		displayName: String!
		includeSuspended: Boolean
	}
`

const contextualArgumentsModerationSDL = `
	type Query {
		moderationQueue(id: ID!): ModerationQueue
		moderationQueues: [ModerationQueue!]!
		queueGrid: [[ModerationQueue!]!]!
		queues: [Queue!]!
		scopes: [Scope!]!
		organization(id: ID!): Organization
	}

	type Mutation {
		flagUser(queueId: ID!, userId: ID!): ModerationQueue!
	}

	type Subscription {
		queueUpdated(id: ID!): ModerationQueue!
	}

	interface Queue {
		id: ID!
		includeSuspended: Boolean!
		flaggedUsers: [User!]!
	}

	type ModerationQueue implements Queue @key(fields: "id") {
		id: ID!
		includeSuspended: Boolean!
		flaggedUsers: [User!]!
		flaggedUserPages: [[User!]!]!
		team: Team
	}

	type AppealQueue implements Queue @key(fields: "id") {
		id: ID!
		includeSuspended: Boolean!
		showSuspended: Boolean
		flaggedUsers: [User!]!
	}

	type Team @key(fields: "id") {
		id: ID!
		members: [User!]!
	}

	union Scope = ModerationQueue | AppealQueue | Team

	type Organization @key(fields: "id") {
		id: ID!
		includeSuspended: Boolean
		queues: [ModerationQueue!]!
		allQueues: [Queue!]!
		members: [User!]!
	}

	type User @key(fields: "id", resolvable: false) {
		id: ID!
	}
`

const contextualArgumentsPublicSDL = `
	type Query {
		post(id: ID!): Post
	}

	type Post @key(fields: "id") {
		id: ID!
		author: User!
		includeSuspended: Boolean
	}

	type ModerationQueue @key(fields: "id") {
		id: ID!
		includeSuspended: Boolean! @external
		policyLevel: Int @requires(fields: "includeSuspended")
	}

	type User @key(fields: "id") {
		id: ID!
		handle: String
	}
`

const contextualArgumentsUsersSDL = `
	type Query @context(name: "viewer") {
		viewerIncludeSuspended: Boolean
	}

	type ModerationQueue @key(fields: "id") @context(name: "userScope") @context(name: "policyScope") @context(name: "peerScope") {
		id: ID!
		includeSuspended: Boolean! @external
		policyLevel: Int @external
		flaggedUsers: [User!]! @external
		summary(includeSuspended: Boolean @fromContext(field: "$userScope { includeSuspended }")): String
	}

	type Organization @key(fields: "id") @context(name: "userScope") {
		id: ID!
		includeSuspended: Boolean @external
	}

	interface Queue @context(name: "queueScope") {
		id: ID!
		includeSuspended: Boolean!
	}

	type AppealQueue @key(fields: "id") {
		id: ID!
		showSuspended: Boolean @external
	}

	type Team @key(fields: "id") {
		id: ID!
	}

	union Scope @context(name: "scope") = ModerationQueue | AppealQueue | Team

	type User @key(fields: "id") {
		id: ID!
		handle: String @external
		friends: [User!]!
		profile(includeSuspended: Boolean @fromContext(field: "$userScope { includeSuspended }")): Profile
		badge(level: Int @fromContext(field: "$policyScope { policyLevel }")): String
		peers(ids: [ID] @fromContext(field: "$peerScope { flaggedUsers { id } }")): String
		queueView(includeSuspended: Boolean @fromContext(field: "$queueScope { includeSuspended }")): String
		scopeView(show: Boolean @fromContext(field: "$scope ... on ModerationQueue { includeSuspended } ... on AppealQueue { showSuspended }")): String
		viewerView(includeSuspended: Boolean @fromContext(field: "$viewer { viewerIncludeSuspended }")): String
		digest(locale: String, includeSuspended: Boolean @fromContext(field: "$userScope { includeSuspended }"), level: Int @fromContext(field: "$policyScope { policyLevel }")): String
		signature(includeSuspended: Boolean @fromContext(field: "$userScope { includeSuspended }")): String @requires(fields: "handle")
	}

	type Profile {
		displayName: String!
		includeSuspended: Boolean
	}
`

// contextualArgumentsUsersPostSDL makes Post a second userScope setter, the parity variant of the motivating fixture.
const contextualArgumentsUsersPostSDL = `
	type Post @key(fields: "id") @context(name: "userScope") {
		id: ID!
		includeSuspended: Boolean @external
	}
`

// contextualExchange is a subgraph request the test expects and the response the subgraph sends for it.
type contextualExchange struct {
	subgraph string
	body     string
	response string
}

type contextualArgumentsSetup struct {
	withoutPostSetter  bool
	multiFetch         bool
	scheduleFetches    bool
	maxGroups          int
	exchanges          []contextualExchange
	subscriptionEvents []string
}

func contextualArgumentsDataSources(t *testing.T, setup contextualArgumentsSetup, clients map[string]*http.Client, subscriptions *contextualSubscriptionClient) []plan.DataSource {
	t.Helper()

	moderationFactory, err := graphql_datasource.NewFactory(t.Context(), clients["moderation"], subscriptions)
	require.NoError(t, err)

	moderation := mustGraphqlDataSourceConfiguration(t,
		"moderation",
		moderationFactory,
		&plan.DataSourceMetadata{
			RootNodes: []plan.TypeField{
				{TypeName: "Query", FieldNames: []string{"moderationQueue", "moderationQueues", "queueGrid", "queues", "scopes", "organization"}},
				{TypeName: "Mutation", FieldNames: []string{"flagUser"}},
				{TypeName: "Subscription", FieldNames: []string{"queueUpdated"}},
				{TypeName: "ModerationQueue", FieldNames: []string{"id", "includeSuspended", "flaggedUsers", "flaggedUserPages", "team"}},
				{TypeName: "AppealQueue", FieldNames: []string{"id", "includeSuspended", "showSuspended", "flaggedUsers"}},
				{TypeName: "Team", FieldNames: []string{"id", "members"}},
				{TypeName: "Organization", FieldNames: []string{"id", "includeSuspended", "queues", "allQueues", "members"}},
				{TypeName: "User", FieldNames: []string{"id"}},
			},
			ChildNodes: []plan.TypeField{
				{TypeName: "Queue", FieldNames: []string{"id", "includeSuspended", "flaggedUsers"}},
			},
			FederationMetaData: plan.FederationMetaData{
				Keys: plan.FederationFieldConfigurations{
					{TypeName: "ModerationQueue", SelectionSet: "id"},
					{TypeName: "AppealQueue", SelectionSet: "id"},
					{TypeName: "Team", SelectionSet: "id"},
					{TypeName: "Organization", SelectionSet: "id"},
					{TypeName: "User", SelectionSet: "id", DisableEntityResolver: true},
				},
			},
		},
		mustConfiguration(t, graphql_datasource.ConfigurationInput{
			Fetch:        &graphql_datasource.FetchConfiguration{URL: "http://moderation/", Method: "POST"},
			Subscription: &graphql_datasource.SubscriptionConfiguration{URL: "http://moderation/"},
			SchemaConfiguration: mustSchemaConfig(t,
				&graphql_datasource.FederationConfiguration{Enabled: true, ServiceSDL: contextualArgumentsModerationSDL},
				contextualArgumentsModerationSDL,
			),
		}),
	)

	public := mustGraphqlDataSourceConfiguration(t,
		"public",
		mustFactory(t, clients["public"]),
		&plan.DataSourceMetadata{
			RootNodes: []plan.TypeField{
				{TypeName: "Query", FieldNames: []string{"post"}},
				{TypeName: "Post", FieldNames: []string{"id", "author", "includeSuspended"}},
				{TypeName: "ModerationQueue", FieldNames: []string{"id", "policyLevel"}, ExternalFieldNames: []string{"includeSuspended"}},
				{TypeName: "User", FieldNames: []string{"id", "handle"}},
			},
			FederationMetaData: plan.FederationMetaData{
				Keys: plan.FederationFieldConfigurations{
					{TypeName: "Post", SelectionSet: "id"},
					{TypeName: "ModerationQueue", SelectionSet: "id"},
					{TypeName: "User", SelectionSet: "id"},
				},
				Requires: plan.FederationFieldConfigurations{
					{TypeName: "ModerationQueue", FieldName: "policyLevel", SelectionSet: "includeSuspended"},
				},
			},
		},
		mustConfiguration(t, graphql_datasource.ConfigurationInput{
			Fetch: &graphql_datasource.FetchConfiguration{URL: "http://public/", Method: "POST"},
			SchemaConfiguration: mustSchemaConfig(t,
				&graphql_datasource.FederationConfiguration{Enabled: true, ServiceSDL: contextualArgumentsPublicSDL},
				contextualArgumentsPublicSDL,
			),
		}),
	)

	userScopeSetters := []plan.ContextSetterConfiguration{
		{TypeName: "ModerationQueue", SelectionSet: "includeSuspended"},
		{TypeName: "Organization", SelectionSet: "includeSuspended"},
	}
	usersSDL := contextualArgumentsUsersSDL
	usersRootNodes := []plan.TypeField{
		{TypeName: "Query", FieldNames: []string{"viewerIncludeSuspended"}},
		{TypeName: "ModerationQueue", FieldNames: []string{"id", "summary"}, ExternalFieldNames: []string{"includeSuspended", "policyLevel", "flaggedUsers"}},
		{TypeName: "Organization", FieldNames: []string{"id"}, ExternalFieldNames: []string{"includeSuspended"}},
		{TypeName: "AppealQueue", FieldNames: []string{"id"}, ExternalFieldNames: []string{"showSuspended"}},
		{TypeName: "Team", FieldNames: []string{"id"}},
		{
			TypeName:           "User",
			FieldNames:         []string{"id", "friends", "profile", "badge", "peers", "queueView", "scopeView", "viewerView", "digest", "signature"},
			ExternalFieldNames: []string{"handle"},
		},
	}
	usersKeys := plan.FederationFieldConfigurations{
		{TypeName: "ModerationQueue", SelectionSet: "id"},
		{TypeName: "Organization", SelectionSet: "id"},
		{TypeName: "AppealQueue", SelectionSet: "id"},
		{TypeName: "Team", SelectionSet: "id"},
		{TypeName: "User", SelectionSet: "id"},
	}
	if !setup.withoutPostSetter {
		usersSDL += contextualArgumentsUsersPostSDL
		usersRootNodes = append(usersRootNodes, plan.TypeField{TypeName: "Post", FieldNames: []string{"id"}, ExternalFieldNames: []string{"includeSuspended"}})
		usersKeys = append(usersKeys, plan.FederationFieldConfiguration{TypeName: "Post", SelectionSet: "id"})
		userScopeSetters = append(userScopeSetters, plan.ContextSetterConfiguration{TypeName: "Post", SelectionSet: "includeSuspended"})
	}
	policyScopeSetters := []plan.ContextSetterConfiguration{{TypeName: "ModerationQueue", SelectionSet: "policyLevel"}}

	users := mustGraphqlDataSourceConfiguration(t,
		"users",
		mustFactory(t, clients["users"]),
		&plan.DataSourceMetadata{
			RootNodes: usersRootNodes,
			ChildNodes: []plan.TypeField{
				{TypeName: "Profile", FieldNames: []string{"displayName", "includeSuspended"}},
				{TypeName: "Queue", FieldNames: []string{"id", "includeSuspended"}},
			},
			FederationMetaData: plan.FederationMetaData{
				Keys: usersKeys,
				Requires: plan.FederationFieldConfigurations{
					{TypeName: "User", FieldName: "signature", SelectionSet: "handle"},
				},
				ContextualArguments: plan.ContextualArgumentConfigurations{
					{TypeName: "ModerationQueue", FieldName: "summary", ArgumentName: "includeSuspended", ContextName: "userScope", Setters: userScopeSetters},
					{TypeName: "User", FieldName: "profile", ArgumentName: "includeSuspended", ContextName: "userScope", Setters: userScopeSetters},
					{TypeName: "User", FieldName: "badge", ArgumentName: "level", ContextName: "policyScope", Setters: policyScopeSetters},
					{TypeName: "User", FieldName: "peers", ArgumentName: "ids", ContextName: "peerScope", Setters: []plan.ContextSetterConfiguration{{TypeName: "ModerationQueue", SelectionSet: "flaggedUsers { id }"}}},
					{
						TypeName:     "User",
						FieldName:    "queueView",
						ArgumentName: "includeSuspended",
						ContextName:  "queueScope",
						Setters: []plan.ContextSetterConfiguration{
							{TypeName: "AppealQueue", SelectionSet: "includeSuspended"},
							{TypeName: "ModerationQueue", SelectionSet: "includeSuspended"},
						},
					},
					{
						TypeName:     "User",
						FieldName:    "scopeView",
						ArgumentName: "show",
						ContextName:  "scope",
						Setters: []plan.ContextSetterConfiguration{
							{TypeName: "AppealQueue", SelectionSet: "showSuspended"},
							{TypeName: "ModerationQueue", SelectionSet: "includeSuspended"},
							{TypeName: "Team"},
						},
					},
					{TypeName: "User", FieldName: "viewerView", ArgumentName: "includeSuspended", ContextName: "viewer", Setters: []plan.ContextSetterConfiguration{{TypeName: "Query", SelectionSet: "viewerIncludeSuspended"}}},
					{TypeName: "User", FieldName: "digest", ArgumentName: "includeSuspended", ContextName: "userScope", Setters: userScopeSetters},
					{TypeName: "User", FieldName: "digest", ArgumentName: "level", ContextName: "policyScope", Setters: policyScopeSetters},
					{TypeName: "User", FieldName: "signature", ArgumentName: "includeSuspended", ContextName: "userScope", Setters: userScopeSetters},
				},
			},
		},
		mustConfiguration(t, graphql_datasource.ConfigurationInput{
			Fetch:               &graphql_datasource.FetchConfiguration{URL: "http://users/", Method: "POST"},
			SchemaConfiguration: mustSchemaConfig(t, &graphql_datasource.FederationConfiguration{Enabled: true, ServiceSDL: usersSDL}, usersSDL),
		}),
	)

	return []plan.DataSource{moderation, public, users}
}

var contextualArgumentsFields = plan.FieldConfigurations{
	{TypeName: "Query", FieldName: "moderationQueue", Arguments: plan.ArgumentsConfigurations{{Name: "id", SourceType: plan.FieldArgumentSource}}},
	{TypeName: "Query", FieldName: "organization", Arguments: plan.ArgumentsConfigurations{{Name: "id", SourceType: plan.FieldArgumentSource}}},
	{TypeName: "Query", FieldName: "post", Arguments: plan.ArgumentsConfigurations{{Name: "id", SourceType: plan.FieldArgumentSource}}},
	{TypeName: "Subscription", FieldName: "queueUpdated", Arguments: plan.ArgumentsConfigurations{{Name: "id", SourceType: plan.FieldArgumentSource}}},
	{
		TypeName:  "Mutation",
		FieldName: "flagUser",
		Arguments: plan.ArgumentsConfigurations{
			{Name: "queueId", SourceType: plan.FieldArgumentSource},
			{Name: "userId", SourceType: plan.FieldArgumentSource},
		},
	},
	{TypeName: "ModerationQueue", FieldName: "summary", Arguments: plan.ArgumentsConfigurations{{Name: "includeSuspended", SourceType: plan.ContextualArgumentSource}}},
	{TypeName: "User", FieldName: "profile", Arguments: plan.ArgumentsConfigurations{{Name: "includeSuspended", SourceType: plan.ContextualArgumentSource}}},
	{TypeName: "User", FieldName: "badge", Arguments: plan.ArgumentsConfigurations{{Name: "level", SourceType: plan.ContextualArgumentSource}}},
	{TypeName: "User", FieldName: "peers", Arguments: plan.ArgumentsConfigurations{{Name: "ids", SourceType: plan.ContextualArgumentSource}}},
	{TypeName: "User", FieldName: "queueView", Arguments: plan.ArgumentsConfigurations{{Name: "includeSuspended", SourceType: plan.ContextualArgumentSource}}},
	{TypeName: "User", FieldName: "scopeView", Arguments: plan.ArgumentsConfigurations{{Name: "show", SourceType: plan.ContextualArgumentSource}}},
	{TypeName: "User", FieldName: "viewerView", Arguments: plan.ArgumentsConfigurations{{Name: "includeSuspended", SourceType: plan.ContextualArgumentSource}}},
	{
		TypeName:  "User",
		FieldName: "digest",
		Arguments: plan.ArgumentsConfigurations{
			{Name: "locale", SourceType: plan.FieldArgumentSource},
			{Name: "includeSuspended", SourceType: plan.ContextualArgumentSource},
			{Name: "level", SourceType: plan.ContextualArgumentSource},
		},
	},
	{TypeName: "User", FieldName: "signature", Arguments: plan.ArgumentsConfigurations{{Name: "includeSuspended", SourceType: plan.ContextualArgumentSource}}},
}

// contextualSubscriptionClient sends its events in order and completes the subscription.
type contextualSubscriptionClient struct {
	recorder *multiFetchRecorder
	events   []string
}

func (c *contextualSubscriptionClient) Subscribe(_ *resolve.Context, options graphql_datasource.GraphQLSubscriptionOptions, updater resolve.SubscriptionUpdater) error {
	c.recorder.record(options.Body.Query)
	go func() {
		for _, event := range c.events {
			updater.Update([]byte(event))
		}
		updater.Complete()
		updater.Done()
	}()
	return nil
}

func newContextualArgumentsEngine(t *testing.T, setup contextualArgumentsSetup, clients map[string]*http.Client, subscriptions *contextualSubscriptionClient) *ExecutionEngine {
	t.Helper()

	schema, err := graphql.NewSchemaFromString(contextualArgumentsSchema)
	require.NoError(t, err)

	engineConf := NewConfiguration(schema)
	engineConf.SetDataSources(contextualArgumentsDataSources(t, setup, clients, subscriptions))
	engineConf.SetFieldConfigurations(contextualArgumentsFields)
	if setup.multiFetch {
		engineConf.EnableMultiFetch()
	}
	if setup.scheduleFetches {
		engineConf.EnableScheduleFetches()
	}

	engine, err := NewExecutionEngine(t.Context(), abstractlogger.Noop{}, engineConf, resolve.ResolverOptions{
		MaxConcurrency:              1024,
		MaxContextualArgumentGroups: setup.maxGroups,
	})
	require.NoError(t, err)
	return engine
}

// executeContextualArguments plans and resolves the request and returns the response, one line per flushed frame
// when streaming, together with the requests every subgraph received.
func executeContextualArguments(t *testing.T, setup contextualArgumentsSetup, request graphql.Request, streaming bool, options ...ExecutionOptions) (string, map[string][]string, error) {
	t.Helper()

	responses := map[string]map[string]sendResponse{"moderation": {}, "public": {}, "users": {}}
	for _, exchange := range setup.exchanges {
		responses[exchange.subgraph][exchange.body] = sendResponse{statusCode: 200, body: exchange.response}
	}
	recorders := map[string]*multiFetchRecorder{"moderation": {}, "public": {}, "users": {}, "subscription": {}}
	clients := make(map[string]*http.Client, len(responses))
	for subgraph := range responses {
		clients[subgraph] = recordingClient(t, recorders[subgraph], subgraph, "/", responses[subgraph])
	}
	engine := newContextualArgumentsEngine(t, setup, clients, &contextualSubscriptionClient{recorder: recorders["subscription"], events: setup.subscriptionEvents})

	resultWriter := graphql.NewEngineResultWriter()
	var frames string
	if streaming {
		resultWriter.SetFlushCallback(func(data []byte) {
			frames += string(data) + "\n"
		})
	}
	execErr := engine.Execute(t.Context(), &request, &resultWriter, options...)

	requests := make(map[string][]string, len(recorders))
	for name, recorder := range recorders {
		requests[name] = recorder.requests()
	}
	if streaming {
		return frames, requests, execErr
	}
	return resultWriter.String(), requests, execErr
}

// The router enables MultiFetch and the fetch scheduler by default, so the cases run with each of them on and off.
var contextualArgumentsModes = []struct {
	name            string
	multiFetch      bool
	scheduleFetches bool
}{
	{name: "multi fetch off, scheduler off"},
	{name: "multi fetch on, scheduler off", multiFetch: true},
	{name: "multi fetch off, scheduler on", scheduleFetches: true},
	{name: "multi fetch on, scheduler on", multiFetch: true, scheduleFetches: true},
}

func assertContextualRequests(t *testing.T, exchanges []contextualExchange, requests map[string][]string) {
	t.Helper()

	expected := map[string][]string{"moderation": nil, "public": nil, "users": nil}
	for _, exchange := range exchanges {
		expected[exchange.subgraph] = append(expected[exchange.subgraph], exchange.body)
	}
	for subgraph, bodies := range expected {
		assert.ElementsMatch(t, bodies, requests[subgraph], "requests received by %s", subgraph)
	}
}

const (
	contextualProfileQuery           = `{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $contextualArgument_0){displayName includeSuspended}}}}","variables":{"representations":[`
	contextualScopeViewQuery         = `{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename scopeView(show: $contextualArgument_0)}}}","variables":{"representations":[`
	contextualProfileAndFriendsQuery = `{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $contextualArgument_0){displayName includeSuspended} friends {id __typename}}}}","variables":{"representations":[`
	contextualDigestQuery            = `{"query":"query($representations: [_Any!]!, $a: String, $contextualArgument_0: Boolean, $contextualArgument_1: Int){_entities(representations: $representations){... on User {__typename digest(locale: $a, includeSuspended: $contextualArgument_0, level: $contextualArgument_1)}}}","variables":{"representations":[`
)

func TestExecutionEngine_ContextualArguments(t *testing.T) {
	testCases := []struct {
		name             string
		query            string
		withoutSetter    bool
		runs             int
		exchanges        []contextualExchange
		expectedResponse string
	}{
		{
			name:  "flagged users receive the queue value",
			query: `{ moderationQueue(id: "q1") { flaggedUsers { id profile { displayName includeSuspended } } } }`,
			exchanges: []contextualExchange{
				{
					subgraph: "moderation",
					body:     `{"query":"query($a: ID!){moderationQueue(id: $a){flaggedUsers {id __typename} __context_0: includeSuspended}}","variables":{"a":"q1"}}`,
					response: `{"data":{"moderationQueue":{"flaggedUsers":[{"id":"u1","__typename":"User"},{"id":"u2","__typename":"User"}],"__context_0":true}}}`,
				},
				{
					subgraph: "users",
					body:     contextualProfileQuery + `{"__typename":"User","id":"u1"},{"__typename":"User","id":"u2"}],"contextualArgument_0":true}}`,
					response: `{"data":{"_entities":[{"__typename":"User","profile":{"displayName":"u1","includeSuspended":true}},{"__typename":"User","profile":{"displayName":"u2","includeSuspended":true}}]}}`,
				},
			},
			expectedResponse: `{"data":{"moderationQueue":{"flaggedUsers":[{"id":"u1","profile":{"displayName":"u1","includeSuspended":true}},{"id":"u2","profile":{"displayName":"u2","includeSuspended":true}}]}}}`,
		},
		{
			name:          "post author receives null without a setter",
			query:         `{ post(id: "p1") { author { id profile { displayName includeSuspended } } } }`,
			withoutSetter: true,
			exchanges: []contextualExchange{
				{
					subgraph: "public",
					body:     `{"query":"query($a: ID!){post(id: $a){author {id __typename}}}","variables":{"a":"p1"}}`,
					response: `{"data":{"post":{"author":{"id":"u1","__typename":"User"}}}}`,
				},
				{
					subgraph: "users",
					body:     `{"query":"query($representations: [_Any!]!, $a: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $a){displayName includeSuspended}}}}","variables":{"a":null,"representations":[{"__typename":"User","id":"u1"}]}}`,
					response: `{"data":{"_entities":[{"__typename":"User","profile":{"displayName":"u1","includeSuspended":null}}]}}`,
				},
			},
			expectedResponse: `{"data":{"post":{"author":{"id":"u1","profile":{"displayName":"u1","includeSuspended":null}}}}}`,
		},
		{
			name:  "post author receives null from the post setter",
			query: `{ post(id: "p1") { author { id profile { displayName includeSuspended } } } }`,
			exchanges: []contextualExchange{
				{
					subgraph: "public",
					body:     `{"query":"query($a: ID!){post(id: $a){author {id __typename} __context_0: includeSuspended}}","variables":{"a":"p1"}}`,
					response: `{"data":{"post":{"author":{"id":"u1","__typename":"User"},"__context_0":null}}}`,
				},
				{
					subgraph: "users",
					body:     contextualProfileQuery + `{"__typename":"User","id":"u1"}],"contextualArgument_0":null}}`,
					response: `{"data":{"_entities":[{"__typename":"User","profile":{"displayName":"u1","includeSuspended":null}}]}}`,
				},
			},
			expectedResponse: `{"data":{"post":{"author":{"id":"u1","profile":{"displayName":"u1","includeSuspended":null}}}}}`,
		},
		{
			name:  "queues with different values send one request per value",
			query: `{ moderationQueues { id flaggedUsers { id profile { displayName includeSuspended } } } }`,
			exchanges: []contextualExchange{
				{
					subgraph: "moderation",
					body:     `{"query":"{moderationQueues {id flaggedUsers {id __typename} __context_0: includeSuspended}}"}`,
					response: `{"data":{"moderationQueues":[` +
						`{"id":"q1","flaggedUsers":[{"id":"u1","__typename":"User"},{"id":"u2","__typename":"User"}],"__context_0":true},` +
						`{"id":"q2","flaggedUsers":[{"id":"u2","__typename":"User"},{"id":"u3","__typename":"User"}],"__context_0":false},` +
						`{"id":"q3","flaggedUsers":[{"id":"u1","__typename":"User"},{"id":"u4","__typename":"User"}],"__context_0":true}]}}`,
				},
				{
					subgraph: "users",
					body:     contextualProfileQuery + `{"__typename":"User","id":"u1"},{"__typename":"User","id":"u2"},{"__typename":"User","id":"u4"}],"contextualArgument_0":true}}`,
					response: `{"data":{"_entities":[` +
						`{"__typename":"User","profile":{"displayName":"u1","includeSuspended":true}},` +
						`{"__typename":"User","profile":{"displayName":"u2","includeSuspended":true}},` +
						`{"__typename":"User","profile":{"displayName":"u4","includeSuspended":true}}]}}`,
				},
				{
					subgraph: "users",
					body:     contextualProfileQuery + `{"__typename":"User","id":"u2"},{"__typename":"User","id":"u3"}],"contextualArgument_0":false}}`,
					response: `{"data":{"_entities":[` +
						`{"__typename":"User","profile":{"displayName":"u2","includeSuspended":false}},` +
						`{"__typename":"User","profile":{"displayName":"u3","includeSuspended":false}}]}}`,
				},
			},
			expectedResponse: `{"data":{"moderationQueues":[` +
				`{"id":"q1","flaggedUsers":[{"id":"u1","profile":{"displayName":"u1","includeSuspended":true}},{"id":"u2","profile":{"displayName":"u2","includeSuspended":true}}]},` +
				`{"id":"q2","flaggedUsers":[{"id":"u2","profile":{"displayName":"u2","includeSuspended":false}},{"id":"u3","profile":{"displayName":"u3","includeSuspended":false}}]},` +
				`{"id":"q3","flaggedUsers":[{"id":"u1","profile":{"displayName":"u1","includeSuspended":true}},{"id":"u4","profile":{"displayName":"u4","includeSuspended":true}}]}]}}`,
		},
		{
			name:  "friends below different queue values keep their own values",
			query: `{ moderationQueues { id flaggedUsers { id friends { id profile { displayName includeSuspended } } } } }`,
			exchanges: []contextualExchange{
				{
					subgraph: "moderation",
					body:     `{"query":"{moderationQueues {id flaggedUsers {id __typename} __context_0: includeSuspended}}"}`,
					response: `{"data":{"moderationQueues":[` +
						`{"id":"q1","flaggedUsers":[{"id":"u1","__typename":"User"},{"id":"u2","__typename":"User"}],"__context_0":true},` +
						`{"id":"q2","flaggedUsers":[{"id":"u2","__typename":"User"},{"id":"u3","__typename":"User"}],"__context_0":false},` +
						`{"id":"q3","flaggedUsers":[{"id":"u1","__typename":"User"},{"id":"u4","__typename":"User"}],"__context_0":true}]}}`,
				},
				{
					subgraph: "users",
					body:     `{"query":"query($representations: [_Any!]!){_entities(representations: $representations){... on User {__typename friends {id __typename}}}}","variables":{"representations":[{"__typename":"User","id":"u1"},{"__typename":"User","id":"u2"},{"__typename":"User","id":"u3"},{"__typename":"User","id":"u4"}]}}`,
					response: `{"data":{"_entities":[` +
						`{"__typename":"User","friends":[{"id":"u2","__typename":"User"}]},` +
						`{"__typename":"User","friends":[{"id":"u3","__typename":"User"}]},` +
						`{"__typename":"User","friends":[]},` +
						`{"__typename":"User","friends":[{"id":"u1","__typename":"User"}]}]}}`,
				},
				{
					subgraph: "users",
					body:     contextualProfileQuery + `{"__typename":"User","id":"u2"},{"__typename":"User","id":"u3"},{"__typename":"User","id":"u1"}],"contextualArgument_0":true}}`,
					response: `{"data":{"_entities":[` +
						`{"__typename":"User","profile":{"displayName":"u2","includeSuspended":true}},` +
						`{"__typename":"User","profile":{"displayName":"u3","includeSuspended":true}},` +
						`{"__typename":"User","profile":{"displayName":"u1","includeSuspended":true}}]}}`,
				},
				{
					subgraph: "users",
					body:     contextualProfileQuery + `{"__typename":"User","id":"u3"}],"contextualArgument_0":false}}`,
					response: `{"data":{"_entities":[{"__typename":"User","profile":{"displayName":"u3","includeSuspended":false}}]}}`,
				},
			},
			expectedResponse: `{"data":{"moderationQueues":[` +
				`{"id":"q1","flaggedUsers":[{"id":"u1","friends":[{"id":"u2","profile":{"displayName":"u2","includeSuspended":true}}]},{"id":"u2","friends":[{"id":"u3","profile":{"displayName":"u3","includeSuspended":true}}]}]},` +
				`{"id":"q2","flaggedUsers":[{"id":"u2","friends":[{"id":"u3","profile":{"displayName":"u3","includeSuspended":false}}]},{"id":"u3","friends":[]}]},` +
				`{"id":"q3","flaggedUsers":[{"id":"u1","friends":[{"id":"u2","profile":{"displayName":"u2","includeSuspended":true}}]},{"id":"u4","friends":[{"id":"u1","profile":{"displayName":"u1","includeSuspended":true}}]}]}]}}`,
		},
		{
			name:  "consumer below a contextual fetch of its own subgraph gets its own entity fetch",
			query: `{ moderationQueues { id flaggedUsers { id profile { displayName includeSuspended } friends { id profile { displayName includeSuspended } } } } }`,
			exchanges: []contextualExchange{
				{
					subgraph: "moderation",
					body:     `{"query":"{moderationQueues {id flaggedUsers {id __typename} __context_0: includeSuspended}}"}`,
					response: `{"data":{"moderationQueues":[` +
						`{"id":"q1","flaggedUsers":[{"id":"u1","__typename":"User"},{"id":"u2","__typename":"User"}],"__context_0":true},` +
						`{"id":"q2","flaggedUsers":[{"id":"u2","__typename":"User"},{"id":"u3","__typename":"User"}],"__context_0":false},` +
						`{"id":"q3","flaggedUsers":[{"id":"u1","__typename":"User"},{"id":"u4","__typename":"User"}],"__context_0":true}]}}`,
				},
				{
					subgraph: "users",
					body:     contextualProfileAndFriendsQuery + `{"__typename":"User","id":"u1"},{"__typename":"User","id":"u2"},{"__typename":"User","id":"u4"}],"contextualArgument_0":true}}`,
					response: `{"data":{"_entities":[` +
						`{"__typename":"User","profile":{"displayName":"u1","includeSuspended":true},"friends":[{"id":"u2","__typename":"User"}]},` +
						`{"__typename":"User","profile":{"displayName":"u2","includeSuspended":true},"friends":[{"id":"u3","__typename":"User"}]},` +
						`{"__typename":"User","profile":{"displayName":"u4","includeSuspended":true},"friends":[{"id":"u1","__typename":"User"}]}]}}`,
				},
				{
					subgraph: "users",
					body:     contextualProfileAndFriendsQuery + `{"__typename":"User","id":"u2"},{"__typename":"User","id":"u3"}],"contextualArgument_0":false}}`,
					response: `{"data":{"_entities":[` +
						`{"__typename":"User","profile":{"displayName":"u2","includeSuspended":false},"friends":[{"id":"u3","__typename":"User"}]},` +
						`{"__typename":"User","profile":{"displayName":"u3","includeSuspended":false},"friends":[]}]}}`,
				},
				{
					subgraph: "users",
					body:     contextualProfileQuery + `{"__typename":"User","id":"u2"},{"__typename":"User","id":"u3"},{"__typename":"User","id":"u1"}],"contextualArgument_0":true}}`,
					response: `{"data":{"_entities":[` +
						`{"__typename":"User","profile":{"displayName":"u2","includeSuspended":true}},` +
						`{"__typename":"User","profile":{"displayName":"u3","includeSuspended":true}},` +
						`{"__typename":"User","profile":{"displayName":"u1","includeSuspended":true}}]}}`,
				},
				{
					subgraph: "users",
					body:     contextualProfileQuery + `{"__typename":"User","id":"u3"}],"contextualArgument_0":false}}`,
					response: `{"data":{"_entities":[{"__typename":"User","profile":{"displayName":"u3","includeSuspended":false}}]}}`,
				},
			},
			expectedResponse: `{"data":{"moderationQueues":[` +
				`{"id":"q1","flaggedUsers":[` +
				`{"id":"u1","profile":{"displayName":"u1","includeSuspended":true},"friends":[{"id":"u2","profile":{"displayName":"u2","includeSuspended":true}}]},` +
				`{"id":"u2","profile":{"displayName":"u2","includeSuspended":true},"friends":[{"id":"u3","profile":{"displayName":"u3","includeSuspended":true}}]}]},` +
				`{"id":"q2","flaggedUsers":[` +
				`{"id":"u2","profile":{"displayName":"u2","includeSuspended":false},"friends":[{"id":"u3","profile":{"displayName":"u3","includeSuspended":false}}]},` +
				`{"id":"u3","profile":{"displayName":"u3","includeSuspended":false},"friends":[]}]},` +
				`{"id":"q3","flaggedUsers":[` +
				`{"id":"u1","profile":{"displayName":"u1","includeSuspended":true},"friends":[{"id":"u2","profile":{"displayName":"u2","includeSuspended":true}}]},` +
				`{"id":"u4","profile":{"displayName":"u4","includeSuspended":true},"friends":[{"id":"u1","profile":{"displayName":"u1","includeSuspended":true}}]}]}]}}`,
		},
		{
			name:  "setter type does not feed its own field",
			query: `{ moderationQueue(id: "q1") { summary } organization(id: "o1") { queues { id summary } } }`,
			exchanges: []contextualExchange{
				{
					subgraph: "moderation",
					body:     `{"query":"query($a: ID!, $b: ID!){moderationQueue(id: $a){__typename id} organization(id: $b){queues {id __typename} __context_0: includeSuspended}}","variables":{"b":"o1","a":"q1"}}`,
					response: `{"data":{"moderationQueue":{"__typename":"ModerationQueue","id":"q1"},` +
						`"organization":{"queues":[{"id":"q1","__typename":"ModerationQueue"},{"id":"q2","__typename":"ModerationQueue"}],"__context_0":false}}}`,
				},
				{
					subgraph: "users",
					body:     `{"query":"query($representations: [_Any!]!, $a: Boolean){_entities(representations: $representations){... on ModerationQueue {__typename summary(includeSuspended: $a)}}}","variables":{"a":null,"representations":[{"__typename":"ModerationQueue","id":"q1"}]}}`,
					response: `{"data":{"_entities":[{"__typename":"ModerationQueue","summary":"q1|includeSuspended=null"}]}}`,
				},
				{
					subgraph: "users",
					body:     `{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on ModerationQueue {__typename summary(includeSuspended: $contextualArgument_0)}}}","variables":{"representations":[{"__typename":"ModerationQueue","id":"q1"},{"__typename":"ModerationQueue","id":"q2"}],"contextualArgument_0":false}}`,
					response: `{"data":{"_entities":[{"__typename":"ModerationQueue","summary":"q1|includeSuspended=false"},{"__typename":"ModerationQueue","summary":"q2|includeSuspended=false"}]}}`,
				},
			},
			expectedResponse: `{"data":{"moderationQueue":{"summary":"q1|includeSuspended=null"},` +
				`"organization":{"queues":[{"id":"q1","summary":"q1|includeSuspended=false"},{"id":"q2","summary":"q2|includeSuspended=false"}]}}}`,
		},
		{
			name:  "nested setters use the nearest ancestor value",
			query: `{ organization(id: "o1") { members { id profile { displayName includeSuspended } } allQueues { flaggedUsers { id profile { displayName includeSuspended } } } } }`,
			exchanges: []contextualExchange{
				{
					subgraph: "moderation",
					body:     `{"query":"query($a: ID!){organization(id: $a){members {id __typename} allQueues {__typename flaggedUsers {id __typename} ... on ModerationQueue {__context_1: includeSuspended}} __context_0: includeSuspended}}","variables":{"a":"o1"}}`,
					response: `{"data":{"organization":{"members":[{"id":"u6","__typename":"User"}],"allQueues":[` +
						`{"__typename":"ModerationQueue","flaggedUsers":[{"id":"u1","__typename":"User"},{"id":"u2","__typename":"User"}],"__context_1":true},` +
						`{"__typename":"AppealQueue","flaggedUsers":[{"id":"u5","__typename":"User"}]}],"__context_0":false}}}`,
				},
				{
					subgraph: "users",
					body:     contextualProfileQuery + `{"__typename":"User","id":"u6"}],"contextualArgument_0":false}}`,
					response: `{"data":{"_entities":[{"__typename":"User","profile":{"displayName":"u6","includeSuspended":false}}]}}`,
				},
				{
					subgraph: "users",
					body:     contextualProfileQuery + `{"__typename":"User","id":"u1"},{"__typename":"User","id":"u2"}],"contextualArgument_0":true}}`,
					response: `{"data":{"_entities":[{"__typename":"User","profile":{"displayName":"u1","includeSuspended":true}},{"__typename":"User","profile":{"displayName":"u2","includeSuspended":true}}]}}`,
				},
				{
					subgraph: "users",
					body:     contextualProfileQuery + `{"__typename":"User","id":"u5"}],"contextualArgument_0":false}}`,
					response: `{"data":{"_entities":[{"__typename":"User","profile":{"displayName":"u5","includeSuspended":false}}]}}`,
				},
			},
			expectedResponse: `{"data":{"organization":{"members":[{"id":"u6","profile":{"displayName":"u6","includeSuspended":false}}],"allQueues":[` +
				`{"flaggedUsers":[{"id":"u1","profile":{"displayName":"u1","includeSuspended":true}},{"id":"u2","profile":{"displayName":"u2","includeSuspended":true}}]},` +
				`{"flaggedUsers":[{"id":"u5","profile":{"displayName":"u5","includeSuspended":false}}]}]}}}`,
		},
		{
			name:  "union setters pick the selection of their type condition",
			query: `{ scopes { ... on ModerationQueue { flaggedUsers { id scopeView } } ... on AppealQueue { flaggedUsers { id scopeView } } ... on Team { members { id scopeView } } } }`,
			exchanges: []contextualExchange{
				{
					subgraph: "moderation",
					body:     `{"query":"{scopes {__typename ... on ModerationQueue {flaggedUsers {id __typename} __context_0: includeSuspended} ... on AppealQueue {flaggedUsers {id __typename} __context_1: showSuspended} ... on Team {members {id __typename}}}}"}`,
					response: `{"data":{"scopes":[` +
						`{"__typename":"ModerationQueue","flaggedUsers":[{"id":"u2","__typename":"User"},{"id":"u3","__typename":"User"}],"__context_0":false},` +
						`{"__typename":"AppealQueue","flaggedUsers":[{"id":"u5","__typename":"User"}],"__context_1":true},` +
						`{"__typename":"Team","members":[{"id":"u7","__typename":"User"}]}]}}`,
				},
				{
					subgraph: "users",
					body:     contextualScopeViewQuery + `{"__typename":"User","id":"u2"},{"__typename":"User","id":"u3"}],"contextualArgument_0":false}}`,
					response: `{"data":{"_entities":[{"__typename":"User","scopeView":"u2|show=false"},{"__typename":"User","scopeView":"u3|show=false"}]}}`,
				},
				{
					subgraph: "users",
					body:     contextualScopeViewQuery + `{"__typename":"User","id":"u5"}],"contextualArgument_0":true}}`,
					response: `{"data":{"_entities":[{"__typename":"User","scopeView":"u5|show=true"}]}}`,
				},
				{
					subgraph: "users",
					body:     contextualScopeViewQuery + `{"__typename":"User","id":"u7"}],"contextualArgument_0":null}}`,
					response: `{"data":{"_entities":[{"__typename":"User","scopeView":"u7|show=null"}]}}`,
				},
			},
			expectedResponse: `{"data":{"scopes":[` +
				`{"flaggedUsers":[{"id":"u2","scopeView":"u2|show=false"},{"id":"u3","scopeView":"u3|show=false"}]},` +
				`{"flaggedUsers":[{"id":"u5","scopeView":"u5|show=true"}]},` +
				`{"members":[{"id":"u7","scopeView":"u7|show=null"}]}]}}`,
		},
		{
			name:  "interface setters at an abstract level are matched by typename",
			query: `{ queues { id flaggedUsers { id queueView } } }`,
			exchanges: []contextualExchange{
				{
					subgraph: "moderation",
					body:     `{"query":"{queues {__typename id flaggedUsers {id __typename} ... on AppealQueue {__context_0: includeSuspended} ... on ModerationQueue {__context_1: includeSuspended}}}"}`,
					response: `{"data":{"queues":[` +
						`{"__typename":"ModerationQueue","id":"q1","flaggedUsers":[{"id":"u1","__typename":"User"},{"id":"u2","__typename":"User"}],"__context_1":true},` +
						`{"__typename":"AppealQueue","id":"a1","flaggedUsers":[{"id":"u5","__typename":"User"}],"__context_0":false}]}}`,
				},
				{
					subgraph: "users",
					body:     `{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename queueView(includeSuspended: $contextualArgument_0)}}}","variables":{"representations":[{"__typename":"User","id":"u1"},{"__typename":"User","id":"u2"}],"contextualArgument_0":true}}`,
					response: `{"data":{"_entities":[{"__typename":"User","queueView":"u1|includeSuspended=true"},{"__typename":"User","queueView":"u2|includeSuspended=true"}]}}`,
				},
				{
					subgraph: "users",
					body:     `{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename queueView(includeSuspended: $contextualArgument_0)}}}","variables":{"representations":[{"__typename":"User","id":"u5"}],"contextualArgument_0":false}}`,
					response: `{"data":{"_entities":[{"__typename":"User","queueView":"u5|includeSuspended=false"}]}}`,
				},
			},
			expectedResponse: `{"data":{"queues":[` +
				`{"id":"q1","flaggedUsers":[{"id":"u1","queueView":"u1|includeSuspended=true"},{"id":"u2","queueView":"u2|includeSuspended=true"}]},` +
				`{"id":"a1","flaggedUsers":[{"id":"u5","queueView":"u5|includeSuspended=false"}]}]}}`,
		},
		{
			name:  "sibling fragments with and without a setter keep their own users",
			query: `{ queues { ... on ModerationQueue { flaggedUsers { id profile { displayName includeSuspended } } } ... on AppealQueue { flaggedUsers { id profile { displayName includeSuspended } } } } }`,
			runs:  20,
			exchanges: []contextualExchange{
				{
					subgraph: "moderation",
					body:     `{"query":"{queues {__typename ... on ModerationQueue {flaggedUsers {id __typename} __context_0: includeSuspended} ... on AppealQueue {flaggedUsers {id __typename}}}}"}`,
					response: `{"data":{"queues":[` +
						`{"__typename":"ModerationQueue","flaggedUsers":[{"id":"u1","__typename":"User"},{"id":"u2","__typename":"User"}],"__context_0":true},` +
						`{"__typename":"AppealQueue","flaggedUsers":[{"id":"u5","__typename":"User"}]}]}}`,
				},
				{
					subgraph: "users",
					body:     contextualProfileQuery + `{"__typename":"User","id":"u1"},{"__typename":"User","id":"u2"}],"contextualArgument_0":true}}`,
					response: `{"data":{"_entities":[{"__typename":"User","profile":{"displayName":"u1","includeSuspended":true}},{"__typename":"User","profile":{"displayName":"u2","includeSuspended":true}}]}}`,
				},
				{
					subgraph: "users",
					body:     `{"query":"query($representations: [_Any!]!, $a: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $a){displayName includeSuspended}}}}","variables":{"a":null,"representations":[{"__typename":"User","id":"u5"}]}}`,
					response: `{"data":{"_entities":[{"__typename":"User","profile":{"displayName":"u5","includeSuspended":null}}]}}`,
				},
			},
			expectedResponse: `{"data":{"queues":[` +
				`{"flaggedUsers":[{"id":"u1","profile":{"displayName":"u1","includeSuspended":true}},{"id":"u2","profile":{"displayName":"u2","includeSuspended":true}}]},` +
				`{"flaggedUsers":[{"id":"u5","profile":{"displayName":"u5","includeSuspended":null}}]}]}}`,
		},
		{
			name:  "a fetch below sibling fragments waits for their contextual and literal null fetches",
			query: `{ scopes { ... on Queue { flaggedUsers { friends { handle } badge } } } }`,
			runs:  20,
			exchanges: []contextualExchange{
				{
					subgraph: "moderation",
					body:     `{"query":"{scopes {__typename ... on AppealQueue {flaggedUsers {__typename id}} ... on ModerationQueue {flaggedUsers {__typename id} includeSuspended __typename id}}}"}`,
					response: `{"data":{"scopes":[` +
						`{"__typename":"ModerationQueue","flaggedUsers":[{"__typename":"User","id":"u2"},{"__typename":"User","id":"u3"}],"includeSuspended":false,"id":"q2"},` +
						`{"__typename":"AppealQueue","flaggedUsers":[{"__typename":"User","id":"u5"}]},` +
						`{"__typename":"Team"}]}}`,
				},
				{
					subgraph: "public",
					body:     `{"query":"query($representations: [_Any!]!){_entities(representations: $representations){... on ModerationQueue {__typename __context_0: policyLevel}}}","variables":{"representations":[{"__typename":"ModerationQueue","includeSuspended":false,"id":"q2"}]}}`,
					response: `{"data":{"_entities":[{"__typename":"ModerationQueue","__context_0":1}]}}`,
				},
				{
					subgraph: "users",
					body:     `{"query":"query($representations: [_Any!]!, $a: Int){_entities(representations: $representations){... on User {__typename friends {__typename id} badge(level: $a)}}}","variables":{"a":null,"representations":[{"__typename":"User","id":"u5"}]}}`,
					response: `{"data":{"_entities":[{"__typename":"User","friends":[],"badge":"u5|level=null"}]}}`,
				},
				{
					subgraph: "users",
					body:     `{"query":"query($representations: [_Any!]!, $contextualArgument_0: Int){_entities(representations: $representations){... on User {__typename friends {__typename id} badge(level: $contextualArgument_0)}}}","variables":{"representations":[{"__typename":"User","id":"u2"},{"__typename":"User","id":"u3"}],"contextualArgument_0":1}}`,
					response: `{"data":{"_entities":[{"__typename":"User","friends":[{"__typename":"User","id":"u3"}],"badge":"u2|level=1"},{"__typename":"User","friends":[],"badge":"u3|level=1"}]}}`,
				},
				{
					subgraph: "public",
					body:     `{"query":"query($representations: [_Any!]!){_entities(representations: $representations){... on User {__typename handle}}}","variables":{"representations":[{"__typename":"User","id":"u3"}]}}`,
					response: `{"data":{"_entities":[{"__typename":"User","handle":"h-u3"}]}}`,
				},
			},
			expectedResponse: `{"data":{"scopes":[` +
				`{"flaggedUsers":[{"friends":[{"handle":"h-u3"}],"badge":"u2|level=1"},{"friends":[],"badge":"u3|level=1"}]},` +
				`{"flaggedUsers":[{"friends":[],"badge":"u5|level=null"}]},` +
				`{}]}}`,
		},
		{
			name:  "abstract level with partial setter types sends null for the other types",
			query: `{ queues { id flaggedUsers { id profile { displayName includeSuspended } } } }`,
			exchanges: []contextualExchange{
				{
					subgraph: "moderation",
					body:     `{"query":"{queues {__typename id flaggedUsers {id __typename} ... on ModerationQueue {__context_0: includeSuspended}}}"}`,
					response: `{"data":{"queues":[` +
						`{"__typename":"ModerationQueue","id":"q1","flaggedUsers":[{"id":"u1","__typename":"User"},{"id":"u2","__typename":"User"}],"__context_0":true},` +
						`{"__typename":"AppealQueue","id":"a1","flaggedUsers":[{"id":"u5","__typename":"User"}]}]}}`,
				},
				{
					subgraph: "users",
					body:     contextualProfileQuery + `{"__typename":"User","id":"u1"},{"__typename":"User","id":"u2"}],"contextualArgument_0":true}}`,
					response: `{"data":{"_entities":[{"__typename":"User","profile":{"displayName":"u1","includeSuspended":true}},{"__typename":"User","profile":{"displayName":"u2","includeSuspended":true}}]}}`,
				},
				{
					subgraph: "users",
					body:     contextualProfileQuery + `{"__typename":"User","id":"u5"}],"contextualArgument_0":null}}`,
					response: `{"data":{"_entities":[{"__typename":"User","profile":{"displayName":"u5","includeSuspended":null}}]}}`,
				},
			},
			expectedResponse: `{"data":{"queues":[` +
				`{"id":"q1","flaggedUsers":[{"id":"u1","profile":{"displayName":"u1","includeSuspended":true}},{"id":"u2","profile":{"displayName":"u2","includeSuspended":true}}]},` +
				`{"id":"a1","flaggedUsers":[{"id":"u5","profile":{"displayName":"u5","includeSuspended":null}}]}]}}`,
		},
		{
			name: "named fragments and aliases on the path",
			query: `query FragmentsAndAliases { qs: queues { ...QueueUsers } }
				fragment QueueUsers on ModerationQueue { users: flaggedUsers { ... on User { id p: profile { displayName includeSuspended } } } }`,
			exchanges: []contextualExchange{
				{
					subgraph: "moderation",
					body:     `{"query":"{qs: queues {__typename ... on ModerationQueue {users: flaggedUsers {id __typename} __context_0: includeSuspended}}}"}`,
					response: `{"data":{"qs":[` +
						`{"__typename":"ModerationQueue","users":[{"id":"u1","__typename":"User"},{"id":"u2","__typename":"User"}],"__context_0":true},` +
						`{"__typename":"AppealQueue"}]}}`,
				},
				{
					subgraph: "users",
					body:     `{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename p: profile(includeSuspended: $contextualArgument_0){displayName includeSuspended}}}}","variables":{"representations":[{"__typename":"User","id":"u1"},{"__typename":"User","id":"u2"}],"contextualArgument_0":true}}`,
					response: `{"data":{"_entities":[{"__typename":"User","p":{"displayName":"u1","includeSuspended":true}},{"__typename":"User","p":{"displayName":"u2","includeSuspended":true}}]}}`,
				},
			},
			expectedResponse: `{"data":{"qs":[{"users":[{"id":"u1","p":{"displayName":"u1","includeSuspended":true}},{"id":"u2","p":{"displayName":"u2","includeSuspended":true}}]},{}]}}`,
		},
		{
			name:  "client aliases colliding with the setter field and the context alias",
			query: `{ moderationQueue(id: "q1") { includeSuspended: id __context_0: id flaggedUsers { id profile { displayName includeSuspended } } } }`,
			exchanges: []contextualExchange{
				{
					subgraph: "moderation",
					body:     `{"query":"query($a: ID!){moderationQueue(id: $a){includeSuspended: id __context_0: id flaggedUsers {id __typename} __context_0_1: includeSuspended}}","variables":{"a":"q1"}}`,
					response: `{"data":{"moderationQueue":{"includeSuspended":"q1","__context_0":"q1","flaggedUsers":[{"id":"u1","__typename":"User"}],"__context_0_1":true}}}`,
				},
				{
					subgraph: "users",
					body:     contextualProfileQuery + `{"__typename":"User","id":"u1"}],"contextualArgument_0":true}}`,
					response: `{"data":{"_entities":[{"__typename":"User","profile":{"displayName":"u1","includeSuspended":true}}]}}`,
				},
			},
			expectedResponse: `{"data":{"moderationQueue":{"includeSuspended":"q1","__context_0":"q1","flaggedUsers":[{"id":"u1","profile":{"displayName":"u1","includeSuspended":true}}]}}}`,
		},
		{
			name:  "setter field owned by another subgraph is resolved before the consumer",
			query: `{ moderationQueue(id: "q1") { flaggedUsers { id badge } } }`,
			exchanges: []contextualExchange{
				{
					subgraph: "moderation",
					body:     `{"query":"query($a: ID!){moderationQueue(id: $a){flaggedUsers {id __typename} includeSuspended __typename id}}","variables":{"a":"q1"}}`,
					response: `{"data":{"moderationQueue":{"flaggedUsers":[{"id":"u1","__typename":"User"},{"id":"u2","__typename":"User"}],"includeSuspended":true,"__typename":"ModerationQueue","id":"q1"}}}`,
				},
				{
					subgraph: "public",
					body:     `{"query":"query($representations: [_Any!]!){_entities(representations: $representations){... on ModerationQueue {__typename __context_0: policyLevel}}}","variables":{"representations":[{"__typename":"ModerationQueue","includeSuspended":true,"id":"q1"}]}}`,
					response: `{"data":{"_entities":[{"__typename":"ModerationQueue","__context_0":3}]}}`,
				},
				{
					subgraph: "users",
					body:     `{"query":"query($representations: [_Any!]!, $contextualArgument_0: Int){_entities(representations: $representations){... on User {__typename badge(level: $contextualArgument_0)}}}","variables":{"representations":[{"__typename":"User","id":"u1"},{"__typename":"User","id":"u2"}],"contextualArgument_0":3}}`,
					response: `{"data":{"_entities":[{"__typename":"User","badge":"u1|level=3"},{"__typename":"User","badge":"u2|level=3"}]}}`,
				},
			},
			expectedResponse: `{"data":{"moderationQueue":{"flaggedUsers":[{"id":"u1","badge":"u1|level=3"},{"id":"u2","badge":"u2|level=3"}]}}}`,
		},
		{
			name:  "lists of lists between setter and consumer",
			query: `{ queueGrid { flaggedUsers { id profile { displayName includeSuspended } } } moderationQueue(id: "q3") { flaggedUserPages { id profile { displayName includeSuspended } } } }`,
			exchanges: []contextualExchange{
				{
					subgraph: "moderation",
					body:     `{"query":"query($a: ID!){queueGrid {flaggedUsers {id __typename} __context_0: includeSuspended} moderationQueue(id: $a){flaggedUserPages {id __typename} __context_1: includeSuspended}}","variables":{"a":"q3"}}`,
					response: `{"data":{"queueGrid":[[` +
						`{"flaggedUsers":[{"id":"u1","__typename":"User"},{"id":"u2","__typename":"User"}],"__context_0":true},` +
						`{"flaggedUsers":[{"id":"u2","__typename":"User"},{"id":"u3","__typename":"User"}],"__context_0":false}],[` +
						`{"flaggedUsers":[{"id":"u1","__typename":"User"},{"id":"u4","__typename":"User"}],"__context_0":true}]],` +
						`"moderationQueue":{"flaggedUserPages":[[{"id":"u1","__typename":"User"}],[],[{"id":"u4","__typename":"User"}]],"__context_1":true}}}`,
				},
				{
					subgraph: "users",
					body:     contextualProfileQuery + `{"__typename":"User","id":"u1"},{"__typename":"User","id":"u2"},{"__typename":"User","id":"u4"}],"contextualArgument_0":true}}`,
					response: `{"data":{"_entities":[` +
						`{"__typename":"User","profile":{"displayName":"u1","includeSuspended":true}},` +
						`{"__typename":"User","profile":{"displayName":"u2","includeSuspended":true}},` +
						`{"__typename":"User","profile":{"displayName":"u4","includeSuspended":true}}]}}`,
				},
				{
					subgraph: "users",
					body:     contextualProfileQuery + `{"__typename":"User","id":"u2"},{"__typename":"User","id":"u3"}],"contextualArgument_0":false}}`,
					response: `{"data":{"_entities":[{"__typename":"User","profile":{"displayName":"u2","includeSuspended":false}},{"__typename":"User","profile":{"displayName":"u3","includeSuspended":false}}]}}`,
				},
				{
					subgraph: "users",
					body:     contextualProfileQuery + `{"__typename":"User","id":"u1"},{"__typename":"User","id":"u4"}],"contextualArgument_0":true}}`,
					response: `{"data":{"_entities":[{"__typename":"User","profile":{"displayName":"u1","includeSuspended":true}},{"__typename":"User","profile":{"displayName":"u4","includeSuspended":true}}]}}`,
				},
			},
			expectedResponse: `{"data":{"queueGrid":[[` +
				`{"flaggedUsers":[{"id":"u1","profile":{"displayName":"u1","includeSuspended":true}},{"id":"u2","profile":{"displayName":"u2","includeSuspended":true}}]},` +
				`{"flaggedUsers":[{"id":"u2","profile":{"displayName":"u2","includeSuspended":false}},{"id":"u3","profile":{"displayName":"u3","includeSuspended":false}}]}],[` +
				`{"flaggedUsers":[{"id":"u1","profile":{"displayName":"u1","includeSuspended":true}},{"id":"u4","profile":{"displayName":"u4","includeSuspended":true}}]}]],` +
				`"moderationQueue":{"flaggedUserPages":[[{"id":"u1","profile":{"displayName":"u1","includeSuspended":true}}],[],[{"id":"u4","profile":{"displayName":"u4","includeSuspended":true}}]]}}}`,
		},
		{
			name:  "null intermediate ancestors drop out of the walk",
			query: `{ moderationQueues { id team { members { id profile { displayName includeSuspended } } } } }`,
			exchanges: []contextualExchange{
				{
					subgraph: "moderation",
					body:     `{"query":"{moderationQueues {id team {members {id __typename}} __context_0: includeSuspended}}"}`,
					response: `{"data":{"moderationQueues":[` +
						`{"id":"q1","team":{"members":[{"id":"u7","__typename":"User"}]},"__context_0":true},` +
						`{"id":"q2","team":null,"__context_0":false},` +
						`{"id":"q3","team":null,"__context_0":true}]}}`,
				},
				{
					subgraph: "users",
					body:     contextualProfileQuery + `{"__typename":"User","id":"u7"}],"contextualArgument_0":true}}`,
					response: `{"data":{"_entities":[{"__typename":"User","profile":{"displayName":"u7","includeSuspended":true}}]}}`,
				},
			},
			expectedResponse: `{"data":{"moderationQueues":[{"id":"q1","team":{"members":[{"id":"u7","profile":{"displayName":"u7","includeSuspended":true}}]}},{"id":"q2","team":null},{"id":"q3","team":null}]}}`,
		},
		{
			name:  "query as setter is fetched as a root field",
			query: `{ moderationQueue(id: "q1") { flaggedUsers { id viewerView } } }`,
			exchanges: []contextualExchange{
				{
					subgraph: "moderation",
					body:     `{"query":"query($a: ID!){moderationQueue(id: $a){flaggedUsers {id __typename}}}","variables":{"a":"q1"}}`,
					response: `{"data":{"moderationQueue":{"flaggedUsers":[{"id":"u1","__typename":"User"},{"id":"u2","__typename":"User"}]}}}`,
				},
				{
					subgraph: "users",
					body:     `{"query":"{__context_0: viewerIncludeSuspended}"}`,
					response: `{"data":{"__context_0":true}}`,
				},
				{
					subgraph: "users",
					body:     `{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename viewerView(includeSuspended: $contextualArgument_0)}}}","variables":{"representations":[{"__typename":"User","id":"u1"},{"__typename":"User","id":"u2"}],"contextualArgument_0":true}}`,
					response: `{"data":{"_entities":[{"__typename":"User","viewerView":"u1|includeSuspended=true"},{"__typename":"User","viewerView":"u2|includeSuspended=true"}]}}`,
				},
			},
			expectedResponse: `{"data":{"moderationQueue":{"flaggedUsers":[{"id":"u1","viewerView":"u1|includeSuspended=true"},{"id":"u2","viewerView":"u2|includeSuspended=true"}]}}}`,
		},
		{
			name:  "consumer under a mutation payload",
			query: `mutation { flagUser(queueId: "q1", userId: "u3") { flaggedUsers { id profile { displayName includeSuspended } } } }`,
			exchanges: []contextualExchange{
				{
					subgraph: "moderation",
					body:     `{"query":"mutation($a: ID!, $b: ID!){flagUser(queueId: $a, userId: $b){flaggedUsers {id __typename} __context_0: includeSuspended}}","variables":{"b":"u3","a":"q1"}}`,
					response: `{"data":{"flagUser":{"flaggedUsers":[{"id":"u1","__typename":"User"},{"id":"u2","__typename":"User"},{"id":"u3","__typename":"User"}],"__context_0":true}}}`,
				},
				{
					subgraph: "users",
					body:     contextualProfileQuery + `{"__typename":"User","id":"u1"},{"__typename":"User","id":"u2"},{"__typename":"User","id":"u3"}],"contextualArgument_0":true}}`,
					response: `{"data":{"_entities":[` +
						`{"__typename":"User","profile":{"displayName":"u1","includeSuspended":true}},` +
						`{"__typename":"User","profile":{"displayName":"u2","includeSuspended":true}},` +
						`{"__typename":"User","profile":{"displayName":"u3","includeSuspended":true}}]}}`,
				},
			},
			expectedResponse: `{"data":{"flagUser":{"flaggedUsers":[` +
				`{"id":"u1","profile":{"displayName":"u1","includeSuspended":true}},` +
				`{"id":"u2","profile":{"displayName":"u2","includeSuspended":true}},` +
				`{"id":"u3","profile":{"displayName":"u3","includeSuspended":true}}]}}}`,
		},
		{
			name:  "two contextual arguments are grouped by their tuple",
			query: `{ moderationQueues { flaggedUsers { id digest(locale: "en") } } }`,
			exchanges: []contextualExchange{
				{
					subgraph: "moderation",
					body:     `{"query":"{moderationQueues {flaggedUsers {id __typename} __context_0: includeSuspended includeSuspended __typename id}}"}`,
					response: `{"data":{"moderationQueues":[` +
						`{"flaggedUsers":[{"id":"u1","__typename":"User"},{"id":"u2","__typename":"User"}],"__context_0":true,"includeSuspended":true,"__typename":"ModerationQueue","id":"q1"},` +
						`{"flaggedUsers":[{"id":"u2","__typename":"User"},{"id":"u3","__typename":"User"}],"__context_0":false,"includeSuspended":false,"__typename":"ModerationQueue","id":"q2"},` +
						`{"flaggedUsers":[{"id":"u1","__typename":"User"},{"id":"u4","__typename":"User"}],"__context_0":true,"includeSuspended":true,"__typename":"ModerationQueue","id":"q3"}]}}`,
				},
				{
					subgraph: "public",
					body:     `{"query":"query($representations: [_Any!]!){_entities(representations: $representations){... on ModerationQueue {__typename __context_1: policyLevel}}}","variables":{"representations":[{"__typename":"ModerationQueue","includeSuspended":true,"id":"q1"},{"__typename":"ModerationQueue","includeSuspended":false,"id":"q2"},{"__typename":"ModerationQueue","includeSuspended":true,"id":"q3"}]}}`,
					response: `{"data":{"_entities":[{"__typename":"ModerationQueue","__context_1":3},{"__typename":"ModerationQueue","__context_1":1},{"__typename":"ModerationQueue","__context_1":3}]}}`,
				},
				{
					subgraph: "users",
					body:     contextualDigestQuery + `{"__typename":"User","id":"u1"},{"__typename":"User","id":"u2"},{"__typename":"User","id":"u4"}],"contextualArgument_1":3,"contextualArgument_0":true,"a":"en"}}`,
					response: `{"data":{"_entities":[` +
						`{"__typename":"User","digest":"u1|locale=en|includeSuspended=true|level=3"},` +
						`{"__typename":"User","digest":"u2|locale=en|includeSuspended=true|level=3"},` +
						`{"__typename":"User","digest":"u4|locale=en|includeSuspended=true|level=3"}]}}`,
				},
				{
					subgraph: "users",
					body:     contextualDigestQuery + `{"__typename":"User","id":"u2"},{"__typename":"User","id":"u3"}],"contextualArgument_1":1,"contextualArgument_0":false,"a":"en"}}`,
					response: `{"data":{"_entities":[` +
						`{"__typename":"User","digest":"u2|locale=en|includeSuspended=false|level=1"},` +
						`{"__typename":"User","digest":"u3|locale=en|includeSuspended=false|level=1"}]}}`,
				},
			},
			expectedResponse: `{"data":{"moderationQueues":[` +
				`{"flaggedUsers":[{"id":"u1","digest":"u1|locale=en|includeSuspended=true|level=3"},{"id":"u2","digest":"u2|locale=en|includeSuspended=true|level=3"}]},` +
				`{"flaggedUsers":[{"id":"u2","digest":"u2|locale=en|includeSuspended=false|level=1"},{"id":"u3","digest":"u3|locale=en|includeSuspended=false|level=1"}]},` +
				`{"flaggedUsers":[{"id":"u1","digest":"u1|locale=en|includeSuspended=true|level=3"},{"id":"u4","digest":"u4|locale=en|includeSuspended=true|level=3"}]}]}}`,
		},
		{
			name:  "selection crossing a list sends the field of every element",
			query: `{ moderationQueue(id: "q1") { flaggedUsers { id peers } } }`,
			exchanges: []contextualExchange{
				{
					subgraph: "moderation",
					body:     `{"query":"query($a: ID!){moderationQueue(id: $a){flaggedUsers {id __typename} __context_0: flaggedUsers {id}}}","variables":{"a":"q1"}}`,
					response: `{"data":{"moderationQueue":{"flaggedUsers":[{"id":"u1","__typename":"User"},{"id":"u2","__typename":"User"}],"__context_0":[{"id":"u1"},{"id":"u2"}]}}}`,
				},
				{
					subgraph: "users",
					body:     `{"query":"query($representations: [_Any!]!, $contextualArgument_0: [ID]){_entities(representations: $representations){... on User {__typename peers(ids: $contextualArgument_0)}}}","variables":{"representations":[{"__typename":"User","id":"u1"},{"__typename":"User","id":"u2"}],"contextualArgument_0":["u1","u2"]}}`,
					response: `{"data":{"_entities":[{"__typename":"User","peers":"u1|ids=[u1,u2]"},{"__typename":"User","peers":"u2|ids=[u1,u2]"}]}}`,
				},
			},
			expectedResponse: `{"data":{"moderationQueue":{"flaggedUsers":[{"id":"u1","peers":"u1|ids=[u1,u2]"},{"id":"u2","peers":"u2|ids=[u1,u2]"}]}}}`,
		},
		{
			name:  "consumer with requires receives the required fields and the context value",
			query: `{ moderationQueue(id: "q1") { flaggedUsers { id signature } } }`,
			exchanges: []contextualExchange{
				{
					subgraph: "moderation",
					body:     `{"query":"query($a: ID!){moderationQueue(id: $a){flaggedUsers {id __typename} __context_0: includeSuspended}}","variables":{"a":"q1"}}`,
					response: `{"data":{"moderationQueue":{"flaggedUsers":[{"id":"u1","__typename":"User"},{"id":"u2","__typename":"User"}],"__context_0":true}}}`,
				},
				{
					subgraph: "public",
					body:     `{"query":"query($representations: [_Any!]!){_entities(representations: $representations){... on User {__typename handle}}}","variables":{"representations":[{"__typename":"User","id":"u1"},{"__typename":"User","id":"u2"}]}}`,
					response: `{"data":{"_entities":[{"__typename":"User","handle":"h-u1"},{"__typename":"User","handle":"h-u2"}]}}`,
				},
				{
					subgraph: "users",
					body:     `{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename signature(includeSuspended: $contextualArgument_0)}}}","variables":{"representations":[{"__typename":"User","handle":"h-u1","id":"u1"},{"__typename":"User","handle":"h-u2","id":"u2"}],"contextualArgument_0":true}}`,
					response: `{"data":{"_entities":[{"__typename":"User","signature":"u1|handle=h-u1|includeSuspended=true"},{"__typename":"User","signature":"u2|handle=h-u2|includeSuspended=true"}]}}`,
				},
			},
			expectedResponse: `{"data":{"moderationQueue":{"flaggedUsers":[{"id":"u1","signature":"u1|handle=h-u1|includeSuspended=true"},{"id":"u2","signature":"u2|handle=h-u2|includeSuspended=true"}]}}}`,
		},
	}

	for _, tc := range testCases {
		for _, mode := range contextualArgumentsModes {
			t.Run(tc.name+"/"+mode.name, func(t *testing.T) {
				setup := contextualArgumentsSetup{
					withoutPostSetter: tc.withoutSetter,
					multiFetch:        mode.multiFetch,
					scheduleFetches:   mode.scheduleFetches,
					exchanges:         tc.exchanges,
				}
				for range max(tc.runs, 1) {
					response, requests, err := executeContextualArguments(t, setup, graphql.Request{Query: tc.query}, false)
					require.NoError(t, err)
					assert.Equal(t, tc.expectedResponse, response)
					assertContextualRequests(t, tc.exchanges, requests)
				}
			})
		}
	}
}

func TestExecutionEngine_ContextualArgumentsDefer(t *testing.T) {
	moderationBody := `{"query":"query($a: ID!){moderationQueue(id: $a){flaggedUsers {id __typename} __context_0: includeSuspended}}","variables":{"a":"q1"}}`
	moderationResponse := `{"data":{"moderationQueue":{"flaggedUsers":[{"id":"u1","__typename":"User"},{"id":"u2","__typename":"User"}],"__context_0":true}}}`
	users := contextualExchange{
		subgraph: "users",
		body:     contextualProfileQuery + `{"__typename":"User","id":"u1"},{"__typename":"User","id":"u2"}],"contextualArgument_0":true}}`,
		response: `{"data":{"_entities":[{"__typename":"User","profile":{"displayName":"u1","includeSuspended":true}},{"__typename":"User","profile":{"displayName":"u2","includeSuspended":true}}]}}`,
	}

	testCases := []struct {
		name             string
		query            string
		exchanges        []contextualExchange
		expectedResponse string
	}{
		{
			name:  "deferred consumer reads the value of the primary fetch",
			query: `{ moderationQueue(id: "q1") { flaggedUsers { id ... @defer { profile { displayName includeSuspended } } } } }`,
			exchanges: []contextualExchange{
				{subgraph: "moderation", body: moderationBody, response: moderationResponse},
				users,
			},
			expectedResponse: `{"data":{"moderationQueue":{"flaggedUsers":[{"id":"u1"},{"id":"u2"}]}},"pending":[{"id":"1","path":["moderationQueue","flaggedUsers"]}],"hasNext":true}
{"incremental":[{"data":{"profile":{"displayName":"u1","includeSuspended":true}},"id":"1","subPath":[0]},{"data":{"profile":{"displayName":"u2","includeSuspended":true}},"id":"1","subPath":[1]}],"completed":[{"id":"1"}],"hasNext":false}
`,
		},
		{
			name:  "deferred owner fetches the context selection in its own defer scope",
			query: `{ ... @defer { moderationQueue(id: "q1") { flaggedUsers { id profile { displayName includeSuspended } } } } }`,
			exchanges: []contextualExchange{
				{
					subgraph: "moderation",
					body:     `{"query":"query($a: ID!){moderationQueue(id: $a){flaggedUsers {id __typename __internal_id: id} __context_0: includeSuspended}}","variables":{"a":"q1"}}`,
					response: `{"data":{"moderationQueue":{"flaggedUsers":[{"id":"u1","__typename":"User","__internal_id":"u1"},{"id":"u2","__typename":"User","__internal_id":"u2"}],"__context_0":true}}}`,
				},
				users,
			},
			expectedResponse: `{"data":{},"pending":[{"id":"1","path":[]}],"hasNext":true}
{"incremental":[{"data":{"moderationQueue":{"flaggedUsers":[{"id":"u1","profile":{"displayName":"u1","includeSuspended":true}},{"id":"u2","profile":{"displayName":"u2","includeSuspended":true}}]}},"id":"1"}],"completed":[{"id":"1"}],"hasNext":false}
`,
		},
		{
			name:  "client defer on the setter field does not delay the context selection",
			query: `{ moderationQueue(id: "q1") { ... @defer { includeSuspended } flaggedUsers { id profile { displayName includeSuspended } } } }`,
			exchanges: []contextualExchange{
				{
					subgraph: "moderation",
					body:     `{"query":"query($a: ID!){moderationQueue(id: $a){flaggedUsers {id __typename} __context_0: includeSuspended}}","variables":{"a":"q1"}}`,
					response: moderationResponse,
				},
				{
					subgraph: "moderation",
					body:     `{"query":"query($a: ID!){moderationQueue(id: $a){includeSuspended}}","variables":{"a":"q1"}}`,
					response: `{"data":{"moderationQueue":{"includeSuspended":true}}}`,
				},
				users,
			},
			expectedResponse: `{"data":{"moderationQueue":{"flaggedUsers":[{"id":"u1","profile":{"displayName":"u1","includeSuspended":true}},{"id":"u2","profile":{"displayName":"u2","includeSuspended":true}}]}},"pending":[{"id":"1","path":["moderationQueue"]}],"hasNext":true}
{"incremental":[{"data":{"includeSuspended":true},"id":"1"}],"completed":[{"id":"1"}],"hasNext":false}
`,
		},
		{
			name:  "deferred field below a consumer whose parent comes from the consumer's subgraph",
			query: `{ moderationQueue(id: "q1") { flaggedUsers { friends { profile { displayName } ... @defer { profile { includeSuspended } } } } } }`,
			exchanges: []contextualExchange{
				{
					subgraph: "moderation",
					body:     `{"query":"query($a: ID!){moderationQueue(id: $a){flaggedUsers {__typename id} __context_0: includeSuspended}}","variables":{"a":"q1"}}`,
					response: `{"data":{"moderationQueue":{"flaggedUsers":[{"__typename":"User","id":"u1"},{"__typename":"User","id":"u2"}],"__context_0":true}}}`,
				},
				{
					subgraph: "users",
					body:     `{"query":"query($representations: [_Any!]!){_entities(representations: $representations){... on User {__typename friends {__typename id}}}}","variables":{"representations":[{"__typename":"User","id":"u1"},{"__typename":"User","id":"u2"}]}}`,
					response: `{"data":{"_entities":[{"__typename":"User","friends":[{"__typename":"User","id":"u2"}]},{"__typename":"User","friends":[{"__typename":"User","id":"u3"}]}]}}`,
				},
				{
					subgraph: "users",
					body:     `{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $contextualArgument_0){displayName}}}}","variables":{"representations":[{"__typename":"User","id":"u2"},{"__typename":"User","id":"u3"}],"contextualArgument_0":true}}`,
					response: `{"data":{"_entities":[{"__typename":"User","profile":{"displayName":"u2"}},{"__typename":"User","profile":{"displayName":"u3"}}]}}`,
				},
				{
					subgraph: "users",
					body:     `{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $contextualArgument_0){includeSuspended}}}}","variables":{"representations":[{"__typename":"User","id":"u2"},{"__typename":"User","id":"u3"}],"contextualArgument_0":true}}`,
					response: `{"data":{"_entities":[{"__typename":"User","profile":{"includeSuspended":true}},{"__typename":"User","profile":{"includeSuspended":true}}]}}`,
				},
			},
			expectedResponse: `{"data":{"moderationQueue":{"flaggedUsers":[{"friends":[{"profile":{"displayName":"u2"}}]},{"friends":[{"profile":{"displayName":"u3"}}]}]}},"pending":[{"id":"1","path":["moderationQueue","flaggedUsers"]}],"hasNext":true}
{"incremental":[{"data":{"includeSuspended":true},"id":"1","subPath":[0,"friends",0,"profile"]},{"data":{"includeSuspended":true},"id":"1","subPath":[1,"friends",0,"profile"]}],"completed":[{"id":"1"}],"hasNext":false}
`,
		},
		{
			name:  "deferred consumer of a sibling fragment is delivered in its defer scope",
			query: `{ queues { flaggedUsers { id } ... on ModerationQueue { flaggedUsers { viewerView } } ... on AppealQueue { flaggedUsers { ... @defer { viewerView } } } } }`,
			exchanges: []contextualExchange{
				{
					subgraph: "users",
					body:     `{"query":"{__context_0: viewerIncludeSuspended}"}`,
					response: `{"data":{"__context_0":true}}`,
				},
				{
					subgraph: "moderation",
					body:     `{"query":"{queues {__typename flaggedUsers {id} ... on ModerationQueue {flaggedUsers {__typename id}} ... on AppealQueue {flaggedUsers {__internal_typename: __typename __typename id}}}}"}`,
					response: `{"data":{"queues":[` +
						`{"__typename":"ModerationQueue","flaggedUsers":[{"id":"u1","__typename":"User"},{"id":"u2","__typename":"User"}]},` +
						`{"__typename":"AppealQueue","flaggedUsers":[{"id":"u5","__internal_typename":"User","__typename":"User"}]}]}}`,
				},
				{
					subgraph: "users",
					body:     `{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename viewerView(includeSuspended: $contextualArgument_0)}}}","variables":{"representations":[{"__typename":"User","id":"u1"},{"__typename":"User","id":"u2"}],"contextualArgument_0":true}}`,
					response: `{"data":{"_entities":[{"__typename":"User","viewerView":"u1|includeSuspended=true"},{"__typename":"User","viewerView":"u2|includeSuspended=true"}]}}`,
				},
				{
					subgraph: "users",
					body:     `{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename viewerView(includeSuspended: $contextualArgument_0)}}}","variables":{"representations":[{"__typename":"User","id":"u5"}],"contextualArgument_0":true}}`,
					response: `{"data":{"_entities":[{"__typename":"User","viewerView":"u5|includeSuspended=true"}]}}`,
				},
			},
			expectedResponse: `{"data":{"queues":[{"flaggedUsers":[{"id":"u1","viewerView":"u1|includeSuspended=true"},{"id":"u2","viewerView":"u2|includeSuspended=true"}]},{"flaggedUsers":[{"id":"u5"}]}]},"pending":[{"id":"1","path":["queues"]}],"hasNext":true}
{"incremental":[{"data":{"viewerView":"u5|includeSuspended=true"},"id":"1","subPath":[1,"flaggedUsers",0]}],"completed":[{"id":"1"}],"hasNext":false}
`,
		},
	}

	for _, tc := range testCases {
		for _, mode := range contextualArgumentsModes {
			t.Run(tc.name+"/"+mode.name, func(t *testing.T) {
				setup := contextualArgumentsSetup{multiFetch: mode.multiFetch, scheduleFetches: mode.scheduleFetches, exchanges: tc.exchanges}
				response, requests, err := executeContextualArguments(t, setup, graphql.Request{Query: tc.query}, true)
				require.NoError(t, err)
				assert.Equal(t, tc.expectedResponse, response)
				assertContextualRequests(t, tc.exchanges, requests)
			})
		}
	}
}

func TestExecutionEngine_ContextualArgumentsSubscription(t *testing.T) {
	event := func(includeSuspended string) string {
		return `{"data":{"queueUpdated":{"flaggedUsers":[{"id":"u1","__typename":"User"}],"__context_0":` + includeSuspended + `}}}`
	}
	usersBody := func(includeSuspended string) string {
		return contextualProfileQuery + `{"__typename":"User","id":"u1"}],"contextualArgument_0":` + includeSuspended + `}}`
	}
	usersResponse := func(includeSuspended string) string {
		return `{"data":{"_entities":[{"__typename":"User","profile":{"displayName":"u1","includeSuspended":` + includeSuspended + `}}]}}`
	}

	for _, mode := range contextualArgumentsModes {
		t.Run(mode.name, func(t *testing.T) {
			setup := contextualArgumentsSetup{
				multiFetch:         mode.multiFetch,
				scheduleFetches:    mode.scheduleFetches,
				subscriptionEvents: []string{event("true"), event("false"), event("true")},
				exchanges: []contextualExchange{
					{subgraph: "users", body: usersBody("true"), response: usersResponse("true")},
					{subgraph: "users", body: usersBody("false"), response: usersResponse("false")},
				},
			}
			response, requests, err := executeContextualArguments(t, setup,
				graphql.Request{Query: `subscription { queueUpdated(id: "q1") { flaggedUsers { id profile { displayName includeSuspended } } } }`}, true)
			require.NoError(t, err)

			assert.Equal(t, `{"data":{"queueUpdated":{"flaggedUsers":[{"id":"u1","profile":{"displayName":"u1","includeSuspended":true}}]}}}
{"data":{"queueUpdated":{"flaggedUsers":[{"id":"u1","profile":{"displayName":"u1","includeSuspended":false}}]}}}
{"data":{"queueUpdated":{"flaggedUsers":[{"id":"u1","profile":{"displayName":"u1","includeSuspended":true}}]}}}
`, response)
			assert.Equal(t, []string{`subscription($a: ID!){queueUpdated(id: $a){flaggedUsers {id __typename} __context_0: includeSuspended}}`}, requests["subscription"])
			assert.Equal(t, []string{usersBody("true"), usersBody("false"), usersBody("true")}, requests["users"])
		})
	}
}

func TestExecutionEngine_ContextualArgumentsClientValues(t *testing.T) {
	t.Run("client variable named like the contextual variable is not forwarded as the value", func(t *testing.T) {
		exchanges := []contextualExchange{
			{
				subgraph: "moderation",
				body:     `{"query":"query($a: ID!){moderationQueue(id: $a){flaggedUsers {id __typename} __context_0: includeSuspended}}","variables":{"a":"q1"}}`,
				response: `{"data":{"moderationQueue":{"flaggedUsers":[{"id":"u1","__typename":"User"}],"__context_0":true}}}`,
			},
			{
				subgraph: "users",
				body:     contextualProfileQuery + `{"__typename":"User","id":"u1"}],"contextualArgument_0":true}}`,
				response: `{"data":{"_entities":[{"__typename":"User","profile":{"displayName":"u1","includeSuspended":true}}]}}`,
			},
		}
		response, requests, err := executeContextualArguments(t, contextualArgumentsSetup{exchanges: exchanges}, graphql.Request{
			Query:     `query ($contextualArgument_0: ID!) { moderationQueue(id: $contextualArgument_0) { flaggedUsers { id profile { displayName includeSuspended } } } }`,
			Variables: []byte(`{"contextualArgument_0":"q1"}`),
		}, false)
		require.NoError(t, err)
		assert.Equal(t, `{"data":{"moderationQueue":{"flaggedUsers":[{"id":"u1","profile":{"displayName":"u1","includeSuspended":true}}]}}}`, response)
		assertContextualRequests(t, exchanges, requests)
	})

	rejected := []struct {
		name      string
		query     string
		variables string
	}{
		{
			name:  "inline literal is rejected",
			query: `{ moderationQueue(id: "q1") { flaggedUsers { profile(includeSuspended: false) { displayName } } } }`,
		},
		{
			name:      "variable is rejected",
			query:     `query ($includeSuspended: Boolean) { moderationQueue(id: "q1") { flaggedUsers { profile(includeSuspended: $includeSuspended) { displayName } } } }`,
			variables: `{"includeSuspended":false}`,
		},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			request := graphql.Request{Query: tc.query}
			if tc.variables != "" {
				request.Variables = []byte(tc.variables)
			}
			_, requests, err := executeContextualArguments(t, contextualArgumentsSetup{}, request, false)
			require.ErrorContains(t, err, `Unknown argument "includeSuspended" on field "User.profile".`)
			assertContextualRequests(t, nil, requests)
		})
	}
}

func TestExecutionEngine_ContextualArgumentsErrors(t *testing.T) {
	t.Run("missing setter value excludes the items and records an error", func(t *testing.T) {
		exchanges := []contextualExchange{
			{
				subgraph: "moderation",
				body:     `{"query":"query($a: ID!){moderationQueue(id: $a){flaggedUsers {id __typename} includeSuspended __typename id}}","variables":{"a":"q1"}}`,
				response: `{"data":{"moderationQueue":{"flaggedUsers":[{"id":"u1","__typename":"User"},{"id":"u2","__typename":"User"}],"includeSuspended":true,"__typename":"ModerationQueue","id":"q1"}}}`,
			},
			{
				subgraph: "public",
				body:     `{"query":"query($representations: [_Any!]!){_entities(representations: $representations){... on ModerationQueue {__typename __context_0: policyLevel}}}","variables":{"representations":[{"__typename":"ModerationQueue","includeSuspended":true,"id":"q1"}]}}`,
				response: `{"data":{"_entities":[null]},"errors":[{"message":"policy service unavailable"}]}`,
			},
		}
		response, requests, err := executeContextualArguments(t, contextualArgumentsSetup{exchanges: exchanges},
			graphql.Request{Query: `{ moderationQueue(id: "q1") { flaggedUsers { id badge } } }`}, false)
		require.NoError(t, err)
		assert.Equal(t, `{"errors":[`+
			`{"message":"Failed to fetch from Subgraph 'public' at Path 'moderationQueue'."},`+
			`{"message":"Failed to fetch from Subgraph 'users' at Path 'moderationQueue.flaggedUsers', Reason: 2 entities were not fetched because the context value of their 'ModerationQueue' ancestor is missing."}],`+
			`"data":{"moderationQueue":{"flaggedUsers":[{"id":"u1","badge":null},{"id":"u2","badge":null}]}}}`, response)
		assertContextualRequests(t, exchanges, requests)
	})

	t.Run("more value groups than the limit send no request", func(t *testing.T) {
		exchanges := []contextualExchange{
			{
				subgraph: "moderation",
				body:     `{"query":"{moderationQueues {id flaggedUsers {id __typename} __context_0: includeSuspended}}"}`,
				response: `{"data":{"moderationQueues":[` +
					`{"id":"q1","flaggedUsers":[{"id":"u1","__typename":"User"},{"id":"u2","__typename":"User"}],"__context_0":true},` +
					`{"id":"q2","flaggedUsers":[{"id":"u2","__typename":"User"},{"id":"u3","__typename":"User"}],"__context_0":false}]}}`,
			},
		}
		response, requests, err := executeContextualArguments(t, contextualArgumentsSetup{exchanges: exchanges, maxGroups: 1},
			graphql.Request{Query: `{ moderationQueues { id flaggedUsers { id profile { displayName } } } }`}, false)
		require.NoError(t, err)
		assert.Equal(t, `{"errors":[{"message":"Failed to fetch from Subgraph 'users' at Path 'moderationQueues.@.flaggedUsers', Reason: the entities need 2 requests for distinct contextual argument values, which exceeds the limit of 1."}],`+
			`"data":{"moderationQueues":[{"id":"q1","flaggedUsers":[{"id":"u1","profile":null},{"id":"u2","profile":null}]},{"id":"q2","flaggedUsers":[{"id":"u2","profile":null},{"id":"u3","profile":null}]}]}}`, response)
		assertContextualRequests(t, exchanges, requests)
	})
}

func TestExecutionEngine_ContextualArgumentsMultiFetch(t *testing.T) {
	query := `{ moderationQueues { flaggedUsers { id friends { id } } } organization(id: "o1") { members { id friends { id } } } moderationQueue(id: "q1") { flaggedUsers { id profile { displayName includeSuspended } } } }`
	moderation := contextualExchange{
		subgraph: "moderation",
		body:     `{"query":"query($a: ID!, $b: ID!){moderationQueues {flaggedUsers {id __typename}} organization(id: $a){members {id __typename}} moderationQueue(id: $b){flaggedUsers {id __typename} __context_0: includeSuspended}}","variables":{"b":"q1","a":"o1"}}`,
		response: `{"data":{"moderationQueues":[{"flaggedUsers":[{"id":"u1","__typename":"User"}]}],"organization":{"members":[{"id":"u6","__typename":"User"}]},` +
			`"moderationQueue":{"flaggedUsers":[{"id":"u1","__typename":"User"},{"id":"u2","__typename":"User"}],"__context_0":true}}}`,
	}
	profile := contextualExchange{
		subgraph: "users",
		body:     contextualProfileQuery + `{"__typename":"User","id":"u1"},{"__typename":"User","id":"u2"}],"contextualArgument_0":true}}`,
		response: `{"data":{"_entities":[{"__typename":"User","profile":{"displayName":"u1","includeSuspended":true}},{"__typename":"User","profile":{"displayName":"u2","includeSuspended":true}}]}}`,
	}
	expectedResponse := `{"data":{"moderationQueues":[{"flaggedUsers":[{"id":"u1","friends":[{"id":"u2"}]}]}],"organization":{"members":[{"id":"u6","friends":[]}]},` +
		`"moderationQueue":{"flaggedUsers":[{"id":"u1","profile":{"displayName":"u1","includeSuspended":true}},{"id":"u2","profile":{"displayName":"u2","includeSuspended":true}}]}}}`

	t.Run("merges the other fetches of the wave and keeps the contextual fetch apart", func(t *testing.T) {
		exchanges := []contextualExchange{moderation, profile, {
			subgraph: "users",
			body:     `{"query":"query($representations_f1: [_Any!]!, $includeF1: Boolean!, $representations_f2: [_Any!]!, $includeF2: Boolean!){f1: _entities(representations: $representations_f1)@include(if: $includeF1) {... on User {__typename friends {id}}} f2: _entities(representations: $representations_f2)@include(if: $includeF2) {... on User {__typename friends {id}}}}","variables":{"representations_f1":[{"__typename":"User","id":"u1"}],"includeF1":true,"representations_f2":[{"__typename":"User","id":"u6"}],"includeF2":true}}`,
			response: `{"data":{"f1":[{"__typename":"User","friends":[{"id":"u2"}]}],"f2":[{"__typename":"User","friends":[]}]}}`,
		}}
		response, requests, err := executeContextualArguments(t, contextualArgumentsSetup{multiFetch: true, exchanges: exchanges}, graphql.Request{Query: query}, false)
		require.NoError(t, err)
		assert.Equal(t, expectedResponse, response)
		assertContextualRequests(t, exchanges, requests)
	})

	t.Run("disabled sends every fetch on its own", func(t *testing.T) {
		exchanges := []contextualExchange{moderation, profile,
			{
				subgraph: "users",
				body:     `{"query":"query($representations: [_Any!]!){_entities(representations: $representations){... on User {__typename friends {id}}}}","variables":{"representations":[{"__typename":"User","id":"u1"}]}}`,
				response: `{"data":{"_entities":[{"__typename":"User","friends":[{"id":"u2"}]}]}}`,
			},
			{
				subgraph: "users",
				body:     `{"query":"query($representations: [_Any!]!){_entities(representations: $representations){... on User {__typename friends {id}}}}","variables":{"representations":[{"__typename":"User","id":"u6"}]}}`,
				response: `{"data":{"_entities":[{"__typename":"User","friends":[]}]}}`,
			},
		}
		response, requests, err := executeContextualArguments(t, contextualArgumentsSetup{exchanges: exchanges}, graphql.Request{Query: query}, false)
		require.NoError(t, err)
		assert.Equal(t, expectedResponse, response)
		assertContextualRequests(t, exchanges, requests)
	})
}

func TestExecutionEngine_ContextualArgumentsLoaderHooks(t *testing.T) {
	exchanges := []contextualExchange{
		{
			subgraph: "moderation",
			body:     `{"query":"{moderationQueues {id flaggedUsers {id __typename} __context_0: includeSuspended}}"}`,
			response: `{"data":{"moderationQueues":[` +
				`{"id":"q1","flaggedUsers":[{"id":"u1","__typename":"User"}],"__context_0":true},` +
				`{"id":"q2","flaggedUsers":[{"id":"u2","__typename":"User"}],"__context_0":false},` +
				`{"id":"q3","flaggedUsers":[{"id":"u3","__typename":"User"}],"__context_0":null}]}}`,
		},
		{
			subgraph: "users",
			body:     contextualProfileQuery + `{"__typename":"User","id":"u1"}],"contextualArgument_0":true}}`,
			response: `{"data":{"_entities":[{"__typename":"User","profile":{"displayName":"u1","includeSuspended":true}}]}}`,
		},
		{
			subgraph: "users",
			body:     contextualProfileQuery + `{"__typename":"User","id":"u2"}],"contextualArgument_0":false}}`,
			response: `{"data":{"_entities":[{"__typename":"User","profile":{"displayName":"u2","includeSuspended":false}}]}}`,
		},
		{
			subgraph: "users",
			body:     contextualProfileQuery + `{"__typename":"User","id":"u3"}],"contextualArgument_0":null}}`,
			response: `{"data":{"_entities":[{"__typename":"User","profile":{"displayName":"u3","includeSuspended":null}}]}}`,
		},
	}
	limiter := &countingRateLimiter{}
	hooks := &countingLoaderHooks{}

	response, requests, err := executeContextualArguments(t, contextualArgumentsSetup{exchanges: exchanges},
		graphql.Request{Query: `{ moderationQueues { id flaggedUsers { id profile { displayName includeSuspended } } } }`}, false,
		withRateLimiter(limiter), withLoaderHooks(hooks))
	require.NoError(t, err)
	assert.Equal(t, `{"data":{"moderationQueues":[`+
		`{"id":"q1","flaggedUsers":[{"id":"u1","profile":{"displayName":"u1","includeSuspended":true}}]},`+
		`{"id":"q2","flaggedUsers":[{"id":"u2","profile":{"displayName":"u2","includeSuspended":false}}]},`+
		`{"id":"q3","flaggedUsers":[{"id":"u3","profile":{"displayName":"u3","includeSuspended":null}}]}]}}`, response)
	assertContextualRequests(t, exchanges, requests)

	assert.EqualValues(t, 2, limiter.preFetch.Load(), "the rate limiter runs once per planned fetch")
	assert.EqualValues(t, 4, hooks.onLoad.Load(), "the hooks run once per subgraph request")
	assert.EqualValues(t, 4, hooks.onFinished.Load())
}

// contextualRespondingClient answers every request with what answer returns for its body.
func contextualRespondingClient(t *testing.T, cacheControl string, answer func(body *astjson.Value) string) *http.Client {
	return &http.Client{Transport: testRoundTripper(func(req *http.Request) *http.Response {
		body, err := io.ReadAll(req.Body)
		if !assert.NoError(t, err) {
			return &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(bytes.NewBufferString("read error"))}
		}
		header := http.Header{}
		if cacheControl != "" {
			header.Set("Cache-Control", cacheControl)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(bytes.NewBufferString(answer(astjson.MustParseBytes(body))))}
	})}
}

// contextualProfileEntities answers a profile entity request with the value the request carries.
func contextualProfileEntities(body *astjson.Value) string {
	includeSuspended := string(body.Get("variables", "contextualArgument_0").MarshalTo(nil))
	var entities []string
	for _, representation := range body.GetArray("variables", "representations") {
		entities = append(entities, `{"__typename":"User","profile":{"displayName":"`+string(representation.GetStringBytes("id"))+`","includeSuspended":`+includeSuspended+`}}`)
	}
	return `{"data":{"_entities":[` + strings.Join(entities, ",") + `]}}`
}

func TestExecutionEngine_ContextualArgumentsCachedPlan(t *testing.T) {
	clients := map[string]*http.Client{
		"moderation": contextualRespondingClient(t, "", func(body *astjson.Value) string {
			id := string(body.GetStringBytes("variables", "a"))
			includeSuspended := strconv.FormatBool(strings.HasSuffix(id, "even"))
			return `{"data":{"moderationQueue":{"flaggedUsers":[{"id":"` + id + `-u1","__typename":"User"}],"__context_0":` + includeSuspended + `}}}`
		}),
		"public": contextualRespondingClient(t, "", func(*astjson.Value) string { return `{"data":null}` }),
		"users":  contextualRespondingClient(t, "", contextualProfileEntities),
	}
	engine := newContextualArgumentsEngine(t, contextualArgumentsSetup{multiFetch: true, scheduleFetches: true}, clients, &contextualSubscriptionClient{})

	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			id := fmt.Sprintf("q%d-odd", i)
			if i%2 == 0 {
				id = fmt.Sprintf("q%d-even", i)
			}
			resultWriter := graphql.NewEngineResultWriter()
			err := engine.Execute(t.Context(), &graphql.Request{
				Query:     `query ($id: ID!) { moderationQueue(id: $id) { flaggedUsers { id profile { displayName includeSuspended } } } }`,
				Variables: []byte(`{"id":"` + id + `"}`),
			}, &resultWriter)
			assert.NoError(t, err)
			assert.Equal(t, `{"data":{"moderationQueue":{"flaggedUsers":[{"id":"`+id+`-u1","profile":{"displayName":"`+id+`-u1","includeSuspended":`+strconv.FormatBool(i%2 == 0)+`}}]}}}`, resultWriter.String())
		})
	}
	wg.Wait()
}

func TestExecutionEngine_ContextualArgumentsResponseCache(t *testing.T) {
	var mu sync.Mutex
	queueValues := map[string]string{"q1": "true", "q2": "false"}
	usersRecorder := &multiFetchRecorder{}
	clients := map[string]*http.Client{
		"moderation": contextualRespondingClient(t, "", func(*astjson.Value) string {
			mu.Lock()
			defer mu.Unlock()
			return `{"data":{"moderationQueues":[` +
				`{"id":"q1","flaggedUsers":[{"id":"u1","__typename":"User"},{"id":"u2","__typename":"User"}],"__context_0":` + queueValues["q1"] + `},` +
				`{"id":"q2","flaggedUsers":[{"id":"u2","__typename":"User"},{"id":"u3","__typename":"User"}],"__context_0":` + queueValues["q2"] + `}]}}`
		}),
		"public": contextualRespondingClient(t, "", func(*astjson.Value) string { return `{"data":null}` }),
		"users": contextualRespondingClient(t, cacheableForAMinute, func(body *astjson.Value) string {
			usersRecorder.record(string(body.MarshalTo(nil)))
			return contextualProfileEntities(body)
		}),
	}
	engine := newContextualArgumentsEngine(t, contextualArgumentsSetup{multiFetch: true, scheduleFetches: true}, clients, &contextualSubscriptionClient{})
	cache := newMapCache()
	execute := func() string {
		resultWriter := graphql.NewEngineResultWriter()
		require.NoError(t, engine.Execute(t.Context(), &graphql.Request{Query: `{ moderationQueues { id flaggedUsers { id profile { displayName includeSuspended } } } }`}, &resultWriter, withResponseCache(t, cache)))
		return resultWriter.String()
	}
	profile := func(id, includeSuspended string) string {
		return `{"id":"` + id + `","profile":{"displayName":"` + id + `","includeSuspended":` + includeSuspended + `}}`
	}

	first := execute()
	assert.Equal(t, `{"data":{"moderationQueues":[{"id":"q1","flaggedUsers":[`+profile("u1", "true")+`,`+profile("u2", "true")+`]},{"id":"q2","flaggedUsers":[`+profile("u2", "false")+`,`+profile("u3", "false")+`]}]}}`, first)
	assert.ElementsMatch(t, []string{
		contextualProfileQuery + `{"__typename":"User","id":"u1"},{"__typename":"User","id":"u2"}],"contextualArgument_0":true}}`,
		contextualProfileQuery + `{"__typename":"User","id":"u2"},{"__typename":"User","id":"u3"}],"contextualArgument_0":false}}`,
	}, usersRecorder.requests())
	assert.Len(t, cache.keys(), 4, "u2 is cached once per value")

	assert.Equal(t, first, execute())
	assert.Len(t, usersRecorder.requests(), 2, "a warm cache serves every group")

	mu.Lock()
	queueValues["q1"] = "false"
	mu.Unlock()
	assert.Equal(t, `{"data":{"moderationQueues":[{"id":"q1","flaggedUsers":[`+profile("u1", "false")+`,`+profile("u2", "false")+`]},{"id":"q2","flaggedUsers":[`+profile("u2", "false")+`,`+profile("u3", "false")+`]}]}}`, execute())
	requests := usersRecorder.requests()
	require.Len(t, requests, 3)
	assert.Equal(t, contextualProfileQuery+`{"__typename":"User","id":"u1"},{"__typename":"User","id":"u2"},{"__typename":"User","id":"u3"}],"contextualArgument_0":false}}`, requests[2],
		"the cached entities of true are not served for false")
}
