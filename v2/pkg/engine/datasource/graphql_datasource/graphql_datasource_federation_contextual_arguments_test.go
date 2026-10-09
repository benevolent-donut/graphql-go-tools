package graphql_datasource

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jensneuse/abstractlogger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/astnormalization"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/asttransform"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/astvalidation"
	. "github.com/wundergraph/graphql-go-tools/v2/pkg/engine/datasourcetesting"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/postprocess"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafeparser"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/operationreport"
)

const contextualArgumentsMotivatingDefinition = `
	type Query {
		moderationQueue(id: ID!): ModerationQueue
		post(id: ID!): Post
	}

	type ModerationQueue {
		id: ID!
		includeSuspended: Boolean!
		flaggedUsers: [User!]!
	}

	type Post {
		id: ID!
		author: User!
	}

	type User {
		id: ID!
		profile: Profile
	}

	type Profile {
		displayName: String!
	}
`

func contextualArgumentsMotivatingConfiguration(t *testing.T) plan.Configuration {
	moderationSDL := `
		type Query {
			moderationQueue(id: ID!): ModerationQueue
		}

		type ModerationQueue @key(fields: "id") {
			id: ID!
			includeSuspended: Boolean!
			flaggedUsers: [User!]!
		}

		type User @key(fields: "id", resolvable: false) {
			id: ID!
		}
	`

	publicSDL := `
		type Query {
			post(id: ID!): Post
		}

		type Post @key(fields: "id") {
			id: ID!
			author: User!
		}

		type User @key(fields: "id", resolvable: false) {
			id: ID!
		}
	`

	usersSDL := `
		type ModerationQueue @key(fields: "id") @context(name: "userScope") {
			id: ID!
			includeSuspended: Boolean! @external
		}

		type User @key(fields: "id") {
			id: ID!
			profile(includeSuspended: Boolean @fromContext(field: "$userScope { includeSuspended }")): Profile
		}

		type Profile {
			displayName: String!
		}
	`

	moderation := mustDataSourceConfiguration(t, "moderation",
		&plan.DataSourceMetadata{
			RootNodes: []plan.TypeField{
				{TypeName: "Query", FieldNames: []string{"moderationQueue"}},
				{TypeName: "ModerationQueue", FieldNames: []string{"id", "includeSuspended", "flaggedUsers"}},
				{TypeName: "User", FieldNames: []string{"id"}},
			},
			FederationMetaData: plan.FederationMetaData{
				Keys: plan.FederationFieldConfigurations{
					{TypeName: "ModerationQueue", SelectionSet: "id"},
					{TypeName: "User", SelectionSet: "id", DisableEntityResolver: true},
				},
			},
		},
		mustCustomConfiguration(t, ConfigurationInput{
			Fetch:               &FetchConfiguration{URL: "http://moderation"},
			SchemaConfiguration: mustSchema(t, &FederationConfiguration{Enabled: true, ServiceSDL: moderationSDL}, moderationSDL),
		}),
	)

	public := mustDataSourceConfiguration(t, "public",
		&plan.DataSourceMetadata{
			RootNodes: []plan.TypeField{
				{TypeName: "Query", FieldNames: []string{"post"}},
				{TypeName: "Post", FieldNames: []string{"id", "author"}},
				{TypeName: "User", FieldNames: []string{"id"}},
			},
			FederationMetaData: plan.FederationMetaData{
				Keys: plan.FederationFieldConfigurations{
					{TypeName: "Post", SelectionSet: "id"},
					{TypeName: "User", SelectionSet: "id", DisableEntityResolver: true},
				},
			},
		},
		mustCustomConfiguration(t, ConfigurationInput{
			Fetch:               &FetchConfiguration{URL: "http://public"},
			SchemaConfiguration: mustSchema(t, &FederationConfiguration{Enabled: true, ServiceSDL: publicSDL}, publicSDL),
		}),
	)

	users := mustDataSourceConfiguration(t, "users",
		&plan.DataSourceMetadata{
			RootNodes: []plan.TypeField{
				{TypeName: "ModerationQueue", FieldNames: []string{"id"}, ExternalFieldNames: []string{"includeSuspended"}},
				{TypeName: "User", FieldNames: []string{"id", "profile"}},
			},
			ChildNodes: []plan.TypeField{
				{TypeName: "Profile", FieldNames: []string{"displayName"}},
			},
			FederationMetaData: plan.FederationMetaData{
				Keys: plan.FederationFieldConfigurations{
					{TypeName: "ModerationQueue", SelectionSet: "id"},
					{TypeName: "User", SelectionSet: "id"},
				},
				ContextualArguments: plan.ContextualArgumentConfigurations{
					{
						TypeName:     "User",
						FieldName:    "profile",
						ArgumentName: "includeSuspended",
						ContextName:  "userScope",
						Setters:      []plan.ContextSetterConfiguration{{TypeName: "ModerationQueue", SelectionSet: "includeSuspended"}},
					},
				},
			},
		},
		mustCustomConfiguration(t, ConfigurationInput{
			Fetch:               &FetchConfiguration{URL: "http://users"},
			SchemaConfiguration: mustSchema(t, &FederationConfiguration{Enabled: true, ServiceSDL: usersSDL}, usersSDL),
		}),
	)

	return plan.Configuration{
		DataSources:                  []plan.DataSource{moderation, public, users},
		DisableResolveFieldPositions: true,
		Fields: plan.FieldConfigurations{
			{TypeName: "Query", FieldName: "moderationQueue", Arguments: plan.ArgumentsConfigurations{{Name: "id", SourceType: plan.FieldArgumentSource}}},
			{TypeName: "Query", FieldName: "post", Arguments: plan.ArgumentsConfigurations{{Name: "id", SourceType: plan.FieldArgumentSource}}},
			{TypeName: "User", FieldName: "profile", Arguments: plan.ArgumentsConfigurations{{Name: "includeSuspended", SourceType: plan.ContextualArgumentSource}}},
		},
	}
}

const contextualArgumentsDefinition = `
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
		setting(key: String!): Boolean
		policyLevel: Int
		riskLevel: Int
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
		risk: String
		peers: String
		queueView: String
		scopeView: String
		viewerView: String
		digest(locale: String): String
		signature: String
		preference: String
	}

	type Profile {
		displayName: String!
		includeSuspended: Boolean
	}
`

func contextualArgumentsSetters(typeNames ...string) []plan.ContextSetterConfiguration {
	setters := make([]plan.ContextSetterConfiguration, 0, len(typeNames))
	for _, typeName := range typeNames {
		setters = append(setters, plan.ContextSetterConfiguration{TypeName: typeName, SelectionSet: "includeSuspended"})
	}
	return setters
}

// contextualArgumentsFlatteningFactory plans like a gRPC data source, which flattens every inline fragment
// into fragments on concrete types.
type contextualArgumentsFlatteningFactory struct {
	*Factory[Configuration]
}

func (f contextualArgumentsFlatteningFactory) PlanningBehavior() plan.DataSourcePlanningBehavior {
	behavior := f.Factory.PlanningBehavior()
	behavior.AlwaysFlattenFragments = true
	return behavior
}

func contextualArgumentsConfiguration(t *testing.T) plan.Configuration {
	return contextualArgumentsConfigurationWithModerationFactory(t, &Factory[Configuration]{})
}

func contextualArgumentsConfigurationWithModerationFactory(t *testing.T, moderationFactory plan.PlannerFactory[Configuration]) plan.Configuration {
	moderationSDL := `
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
			setting(key: String!): Boolean
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

	publicSDL := `
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
			riskLevel: Int
		}

		type User @key(fields: "id") {
			id: ID!
			handle: String
		}
	`

	usersSDL := `
		type Query @context(name: "viewer") {
			viewerIncludeSuspended: Boolean
		}

		type ModerationQueue @key(fields: "id") @context(name: "userScope") @context(name: "policyScope") @context(name: "riskScope") @context(name: "peerScope") @context(name: "settingScope") {
			id: ID!
			includeSuspended: Boolean! @external
			policyLevel: Int @external
			riskLevel: Int @external
			flaggedUsers: [User!]! @external
			setting(key: String!): Boolean @external
			summary(includeSuspended: Boolean @fromContext(field: "$userScope { includeSuspended }")): String
		}

		type Post @key(fields: "id") @context(name: "userScope") {
			id: ID!
			includeSuspended: Boolean @external
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
			risk(level: Int @fromContext(field: "$riskScope { riskLevel }")): String
			peers(ids: [ID] @fromContext(field: "$peerScope { flaggedUsers { id } }")): String
			queueView(includeSuspended: Boolean @fromContext(field: "$queueScope { includeSuspended }")): String
			scopeView(show: Boolean @fromContext(field: "$scope ... on ModerationQueue { includeSuspended } ... on AppealQueue { showSuspended }")): String
			viewerView(includeSuspended: Boolean @fromContext(field: "$viewer { viewerIncludeSuspended }")): String
			digest(locale: String, includeSuspended: Boolean @fromContext(field: "$userScope { includeSuspended }"), level: Int @fromContext(field: "$policyScope { policyLevel }")): String
			signature(includeSuspended: Boolean @fromContext(field: "$userScope { includeSuspended }")): String @requires(fields: "handle")
			preference(enabled: Boolean @fromContext(field: "$settingScope { setting(key: \"suspended\") }")): String
		}

		type Profile {
			displayName: String!
			includeSuspended: Boolean
		}
	`

	moderation, err := plan.NewDataSourceConfiguration[Configuration]("moderation", moderationFactory,
		&plan.DataSourceMetadata{
			RootNodes: []plan.TypeField{
				{TypeName: "Query", FieldNames: []string{"moderationQueue", "moderationQueues", "queueGrid", "queues", "scopes", "organization"}},
				{TypeName: "Mutation", FieldNames: []string{"flagUser"}},
				{TypeName: "Subscription", FieldNames: []string{"queueUpdated"}},
				{TypeName: "ModerationQueue", FieldNames: []string{"id", "includeSuspended", "flaggedUsers", "flaggedUserPages", "team", "setting"}},
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
		mustCustomConfiguration(t, ConfigurationInput{
			Fetch:               &FetchConfiguration{URL: "http://moderation"},
			Subscription:        &SubscriptionConfiguration{URL: "ws://moderation"},
			SchemaConfiguration: mustSchema(t, &FederationConfiguration{Enabled: true, ServiceSDL: moderationSDL}, moderationSDL),
		}),
	)
	require.NoError(t, err)

	public := mustDataSourceConfiguration(t, "public",
		&plan.DataSourceMetadata{
			RootNodes: []plan.TypeField{
				{TypeName: "Query", FieldNames: []string{"post"}},
				{TypeName: "Post", FieldNames: []string{"id", "author", "includeSuspended"}},
				{TypeName: "ModerationQueue", FieldNames: []string{"id", "policyLevel", "riskLevel"}, ExternalFieldNames: []string{"includeSuspended"}},
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
		mustCustomConfiguration(t, ConfigurationInput{
			Fetch:               &FetchConfiguration{URL: "http://public"},
			SchemaConfiguration: mustSchema(t, &FederationConfiguration{Enabled: true, ServiceSDL: publicSDL}, publicSDL),
		}),
	)

	users := mustDataSourceConfiguration(t, "users",
		&plan.DataSourceMetadata{
			RootNodes: []plan.TypeField{
				{TypeName: "Query", FieldNames: []string{"viewerIncludeSuspended"}},
				{TypeName: "ModerationQueue", FieldNames: []string{"id", "summary"}, ExternalFieldNames: []string{"includeSuspended", "policyLevel", "riskLevel", "flaggedUsers", "setting"}},
				{TypeName: "Post", FieldNames: []string{"id"}, ExternalFieldNames: []string{"includeSuspended"}},
				{TypeName: "Organization", FieldNames: []string{"id"}, ExternalFieldNames: []string{"includeSuspended"}},
				{TypeName: "AppealQueue", FieldNames: []string{"id"}, ExternalFieldNames: []string{"showSuspended"}},
				{TypeName: "Team", FieldNames: []string{"id"}},
				{
					TypeName:           "User",
					FieldNames:         []string{"id", "friends", "profile", "badge", "risk", "peers", "queueView", "scopeView", "viewerView", "digest", "signature", "preference"},
					ExternalFieldNames: []string{"handle"},
				},
			},
			ChildNodes: []plan.TypeField{
				{TypeName: "Profile", FieldNames: []string{"displayName", "includeSuspended"}},
				{TypeName: "Queue", FieldNames: []string{"id", "includeSuspended"}},
			},
			FederationMetaData: plan.FederationMetaData{
				Keys: plan.FederationFieldConfigurations{
					{TypeName: "ModerationQueue", SelectionSet: "id"},
					{TypeName: "Post", SelectionSet: "id"},
					{TypeName: "Organization", SelectionSet: "id"},
					{TypeName: "AppealQueue", SelectionSet: "id"},
					{TypeName: "Team", SelectionSet: "id"},
					{TypeName: "User", SelectionSet: "id"},
				},
				Requires: plan.FederationFieldConfigurations{
					{TypeName: "User", FieldName: "signature", SelectionSet: "handle"},
				},
				ContextualArguments: plan.ContextualArgumentConfigurations{
					{TypeName: "ModerationQueue", FieldName: "summary", ArgumentName: "includeSuspended", ContextName: "userScope", Setters: contextualArgumentsSetters("ModerationQueue", "Post", "Organization")},
					{TypeName: "User", FieldName: "profile", ArgumentName: "includeSuspended", ContextName: "userScope", Setters: contextualArgumentsSetters("ModerationQueue", "Post", "Organization")},
					{TypeName: "User", FieldName: "badge", ArgumentName: "level", ContextName: "policyScope", Setters: []plan.ContextSetterConfiguration{{TypeName: "ModerationQueue", SelectionSet: "policyLevel"}}},
					{TypeName: "User", FieldName: "risk", ArgumentName: "level", ContextName: "riskScope", Setters: []plan.ContextSetterConfiguration{{TypeName: "ModerationQueue", SelectionSet: "riskLevel"}}},
					{TypeName: "User", FieldName: "peers", ArgumentName: "ids", ContextName: "peerScope", Setters: []plan.ContextSetterConfiguration{{TypeName: "ModerationQueue", SelectionSet: "flaggedUsers { id }"}}},
					{TypeName: "User", FieldName: "queueView", ArgumentName: "includeSuspended", ContextName: "queueScope", Setters: contextualArgumentsSetters("AppealQueue", "ModerationQueue")},
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
					{TypeName: "User", FieldName: "digest", ArgumentName: "includeSuspended", ContextName: "userScope", Setters: contextualArgumentsSetters("ModerationQueue", "Post", "Organization")},
					{TypeName: "User", FieldName: "digest", ArgumentName: "level", ContextName: "policyScope", Setters: []plan.ContextSetterConfiguration{{TypeName: "ModerationQueue", SelectionSet: "policyLevel"}}},
					{TypeName: "User", FieldName: "signature", ArgumentName: "includeSuspended", ContextName: "userScope", Setters: contextualArgumentsSetters("ModerationQueue", "Post", "Organization")},
					{TypeName: "User", FieldName: "preference", ArgumentName: "enabled", ContextName: "settingScope", Setters: []plan.ContextSetterConfiguration{{TypeName: "ModerationQueue", SelectionSet: `setting(key: "suspended")`}}},
				},
			},
		},
		mustCustomConfiguration(t, ConfigurationInput{
			Fetch:               &FetchConfiguration{URL: "http://users"},
			SchemaConfiguration: mustSchema(t, &FederationConfiguration{Enabled: true, ServiceSDL: usersSDL}, usersSDL),
		}),
	)

	return plan.Configuration{
		DataSources:                  []plan.DataSource{moderation, public, users},
		DisableResolveFieldPositions: true,
		Fields: plan.FieldConfigurations{
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
			{TypeName: "User", FieldName: "risk", Arguments: plan.ArgumentsConfigurations{{Name: "level", SourceType: plan.ContextualArgumentSource}}},
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
			{TypeName: "User", FieldName: "preference", Arguments: plan.ArgumentsConfigurations{{Name: "enabled", SourceType: plan.ContextualArgumentSource}}},
			{TypeName: "ModerationQueue", FieldName: "setting", Arguments: plan.ArgumentsConfigurations{{Name: "key", SourceType: plan.FieldArgumentSource}}},
		},
	}
}

const contextualArgumentsSameSubgraphDefinition = `
	type Query {
		moderationQueue(id: ID!): ModerationQueue
	}

	type ModerationQueue {
		id: ID!
		includeSuspended: Boolean!
		flaggedUsers: [User!]!
	}

	type User {
		id: ID!
		name: String
		friends: [User!]!
		profile: Profile
	}

	type Profile {
		displayName: String!
	}
`

func contextualArgumentsSameSubgraphConfiguration(t *testing.T) plan.Configuration {
	usersSDL := `
		type Query {
			moderationQueue(id: ID!): ModerationQueue
		}

		type ModerationQueue @key(fields: "id") @context(name: "userScope") {
			id: ID!
			includeSuspended: Boolean!
			flaggedUsers: [User!]!
		}

		type User @key(fields: "id") {
			id: ID!
			name: String
			friends: [User!]!
			profile(includeSuspended: Boolean @fromContext(field: "$userScope { includeSuspended }")): Profile
		}

		type Profile {
			displayName: String!
		}
	`

	users := mustDataSourceConfiguration(t, "users",
		&plan.DataSourceMetadata{
			RootNodes: []plan.TypeField{
				{TypeName: "Query", FieldNames: []string{"moderationQueue"}},
				{TypeName: "ModerationQueue", FieldNames: []string{"id", "includeSuspended", "flaggedUsers"}},
				{TypeName: "User", FieldNames: []string{"id", "name", "friends", "profile"}},
			},
			ChildNodes: []plan.TypeField{
				{TypeName: "Profile", FieldNames: []string{"displayName"}},
			},
			FederationMetaData: plan.FederationMetaData{
				Keys: plan.FederationFieldConfigurations{
					{TypeName: "ModerationQueue", SelectionSet: "id"},
					{TypeName: "User", SelectionSet: "id"},
				},
				ContextualArguments: plan.ContextualArgumentConfigurations{
					{TypeName: "User", FieldName: "profile", ArgumentName: "includeSuspended", ContextName: "userScope", Setters: contextualArgumentsSetters("ModerationQueue")},
				},
			},
		},
		mustCustomConfiguration(t, ConfigurationInput{
			Fetch:               &FetchConfiguration{URL: "http://users"},
			SchemaConfiguration: mustSchema(t, &FederationConfiguration{Enabled: true, ServiceSDL: usersSDL}, usersSDL),
		}),
	)

	return plan.Configuration{
		DataSources:                  []plan.DataSource{users},
		DisableResolveFieldPositions: true,
		Fields: plan.FieldConfigurations{
			{TypeName: "Query", FieldName: "moderationQueue", Arguments: plan.ArgumentsConfigurations{{Name: "id", SourceType: plan.FieldArgumentSource}}},
			{TypeName: "User", FieldName: "profile", Arguments: plan.ArgumentsConfigurations{{Name: "includeSuspended", SourceType: plan.ContextualArgumentSource}}},
		},
	}
}

const contextualArgumentsContributedImplementationDefinition = `
	type Query {
		queues: [Queue!]!
	}

	interface Queue {
		id: ID!
		members: [User!]!
	}

	type ModerationQueue implements Queue {
		id: ID!
		includeSuspended: Boolean!
		members: [User!]!
	}

	type EscalationQueue implements Queue {
		id: ID!
		includeSuspended: Boolean!
		members: [User!]!
	}

	type User {
		id: ID!
		profile: Profile
	}

	type Profile {
		displayName: String!
	}
`

// contextualArgumentsContributedImplementationConfiguration has an implementation of Queue that only the escalation
// subgraph defines, so objects of that type never occur under the queues field resolved by moderation.
func contextualArgumentsContributedImplementationConfiguration(t *testing.T) plan.Configuration {
	moderationSDL := `
		type Query {
			queues: [Queue!]!
		}

		interface Queue {
			id: ID!
			members: [User!]!
		}

		type ModerationQueue implements Queue @key(fields: "id") {
			id: ID!
			includeSuspended: Boolean!
			members: [User!]!
		}

		type User @key(fields: "id", resolvable: false) {
			id: ID!
		}
	`

	escalationSDL := `
		interface Queue {
			id: ID!
			members: [User!]!
		}

		type EscalationQueue implements Queue @key(fields: "id") {
			id: ID!
			includeSuspended: Boolean!
			members: [User!]!
		}

		type User @key(fields: "id", resolvable: false) {
			id: ID!
		}
	`

	usersSDL := `
		interface Queue @context(name: "queueScope") {
			id: ID!
			includeSuspended: Boolean!
		}

		type ModerationQueue implements Queue @key(fields: "id") {
			id: ID!
			includeSuspended: Boolean! @external
		}

		type EscalationQueue implements Queue @key(fields: "id") {
			id: ID!
			includeSuspended: Boolean! @external
		}

		type User @key(fields: "id") {
			id: ID!
			profile(includeSuspended: Boolean @fromContext(field: "$queueScope { includeSuspended }")): Profile
		}

		type Profile {
			displayName: String!
		}
	`

	moderation := mustDataSourceConfiguration(t, "moderation",
		&plan.DataSourceMetadata{
			RootNodes: []plan.TypeField{
				{TypeName: "Query", FieldNames: []string{"queues"}},
				{TypeName: "ModerationQueue", FieldNames: []string{"id", "includeSuspended", "members"}},
				{TypeName: "User", FieldNames: []string{"id"}},
			},
			ChildNodes: []plan.TypeField{
				{TypeName: "Queue", FieldNames: []string{"id", "members"}},
			},
			FederationMetaData: plan.FederationMetaData{
				Keys: plan.FederationFieldConfigurations{
					{TypeName: "ModerationQueue", SelectionSet: "id"},
					{TypeName: "User", SelectionSet: "id", DisableEntityResolver: true},
				},
			},
		},
		mustCustomConfiguration(t, ConfigurationInput{
			Fetch:               &FetchConfiguration{URL: "http://moderation"},
			SchemaConfiguration: mustSchema(t, &FederationConfiguration{Enabled: true, ServiceSDL: moderationSDL}, moderationSDL),
		}),
	)

	escalation := mustDataSourceConfiguration(t, "escalation",
		&plan.DataSourceMetadata{
			RootNodes: []plan.TypeField{
				{TypeName: "EscalationQueue", FieldNames: []string{"id", "includeSuspended", "members"}},
				{TypeName: "User", FieldNames: []string{"id"}},
			},
			ChildNodes: []plan.TypeField{
				{TypeName: "Queue", FieldNames: []string{"id", "members"}},
			},
			FederationMetaData: plan.FederationMetaData{
				Keys: plan.FederationFieldConfigurations{
					{TypeName: "EscalationQueue", SelectionSet: "id"},
					{TypeName: "User", SelectionSet: "id", DisableEntityResolver: true},
				},
			},
		},
		mustCustomConfiguration(t, ConfigurationInput{
			Fetch:               &FetchConfiguration{URL: "http://escalation"},
			SchemaConfiguration: mustSchema(t, &FederationConfiguration{Enabled: true, ServiceSDL: escalationSDL}, escalationSDL),
		}),
	)

	users := mustDataSourceConfiguration(t, "users",
		&plan.DataSourceMetadata{
			RootNodes: []plan.TypeField{
				{TypeName: "ModerationQueue", FieldNames: []string{"id"}, ExternalFieldNames: []string{"includeSuspended"}},
				{TypeName: "EscalationQueue", FieldNames: []string{"id"}, ExternalFieldNames: []string{"includeSuspended"}},
				{TypeName: "User", FieldNames: []string{"id", "profile"}},
			},
			ChildNodes: []plan.TypeField{
				{TypeName: "Queue", FieldNames: []string{"id", "includeSuspended"}},
				{TypeName: "Profile", FieldNames: []string{"displayName"}},
			},
			FederationMetaData: plan.FederationMetaData{
				Keys: plan.FederationFieldConfigurations{
					{TypeName: "ModerationQueue", SelectionSet: "id"},
					{TypeName: "EscalationQueue", SelectionSet: "id"},
					{TypeName: "User", SelectionSet: "id"},
				},
				ContextualArguments: plan.ContextualArgumentConfigurations{
					{TypeName: "User", FieldName: "profile", ArgumentName: "includeSuspended", ContextName: "queueScope", Setters: contextualArgumentsSetters("EscalationQueue", "ModerationQueue")},
				},
			},
		},
		mustCustomConfiguration(t, ConfigurationInput{
			Fetch:               &FetchConfiguration{URL: "http://users"},
			SchemaConfiguration: mustSchema(t, &FederationConfiguration{Enabled: true, ServiceSDL: usersSDL}, usersSDL),
		}),
	)

	return plan.Configuration{
		DataSources:                  []plan.DataSource{moderation, escalation, users},
		DisableResolveFieldPositions: true,
		Fields: plan.FieldConfigurations{
			{TypeName: "User", FieldName: "profile", Arguments: plan.ArgumentsConfigurations{{Name: "includeSuspended", SourceType: plan.ContextualArgumentSource}}},
		},
	}
}

const contextualArgumentsNestedSelectionDefinition = `
	type Query {
		queues: [Queue!]!
	}

	interface Queue {
		id: ID!
		subqueues: [Queue!]!
		flaggedUsers: [User!]!
	}

	type ModerationQueue implements Queue {
		id: ID!
		team: Team
		subqueues: [Queue!]!
		flaggedUsers: [User!]!
	}

	type AppealQueue implements Queue {
		id: ID!
		subqueues: [Queue!]!
		flaggedUsers: [User!]!
	}

	type Team {
		id: ID!
		name: String
	}

	type User {
		id: ID!
		teamView: String
		teamTypeView: String
	}
`

// contextualArgumentsNestedSelectionConfiguration plans the setter subgraph with flattened fragments, and its context
// selections read a nested field: Team.name is owned by a third subgraph, Team.__typename by the setter subgraph.
func contextualArgumentsNestedSelectionConfiguration(t *testing.T) plan.Configuration {
	moderationSDL := `
		type Query {
			queues: [Queue!]!
		}

		interface Queue {
			id: ID!
			subqueues: [Queue!]!
			flaggedUsers: [User!]!
		}

		type ModerationQueue implements Queue @key(fields: "id") {
			id: ID!
			team: Team
			subqueues: [Queue!]!
			flaggedUsers: [User!]!
		}

		type AppealQueue implements Queue @key(fields: "id") {
			id: ID!
			subqueues: [Queue!]!
			flaggedUsers: [User!]!
		}

		type Team @key(fields: "id") {
			id: ID!
		}

		type User @key(fields: "id", resolvable: false) {
			id: ID!
		}
	`

	publicSDL := `
		type Team @key(fields: "id") {
			id: ID!
			name: String
		}
	`

	usersSDL := `
		type ModerationQueue @key(fields: "id") @context(name: "teamScope") @context(name: "teamTypeScope") {
			id: ID!
			team: Team @external
		}

		type Team @key(fields: "id") {
			id: ID!
			name: String @external
		}

		type User @key(fields: "id") {
			id: ID!
			teamView(teamName: String @fromContext(field: "$teamScope { team { name } }")): String
			teamTypeView(typeName: String @fromContext(field: "$teamTypeScope { team { __typename } }")): String
		}
	`

	moderation, err := plan.NewDataSourceConfiguration[Configuration]("moderation", contextualArgumentsFlatteningFactory{&Factory[Configuration]{}},
		&plan.DataSourceMetadata{
			RootNodes: []plan.TypeField{
				{TypeName: "Query", FieldNames: []string{"queues"}},
				{TypeName: "ModerationQueue", FieldNames: []string{"id", "team", "subqueues", "flaggedUsers"}},
				{TypeName: "AppealQueue", FieldNames: []string{"id", "subqueues", "flaggedUsers"}},
				{TypeName: "Team", FieldNames: []string{"id"}},
				{TypeName: "User", FieldNames: []string{"id"}},
			},
			ChildNodes: []plan.TypeField{
				{TypeName: "Queue", FieldNames: []string{"id", "subqueues", "flaggedUsers"}},
			},
			FederationMetaData: plan.FederationMetaData{
				Keys: plan.FederationFieldConfigurations{
					{TypeName: "ModerationQueue", SelectionSet: "id"},
					{TypeName: "AppealQueue", SelectionSet: "id"},
					{TypeName: "Team", SelectionSet: "id"},
					{TypeName: "User", SelectionSet: "id", DisableEntityResolver: true},
				},
			},
		},
		mustCustomConfiguration(t, ConfigurationInput{
			Fetch:               &FetchConfiguration{URL: "http://moderation"},
			SchemaConfiguration: mustSchema(t, &FederationConfiguration{Enabled: true, ServiceSDL: moderationSDL}, moderationSDL),
		}),
	)
	require.NoError(t, err)

	public := mustDataSourceConfiguration(t, "public",
		&plan.DataSourceMetadata{
			RootNodes: []plan.TypeField{
				{TypeName: "Team", FieldNames: []string{"id", "name"}},
			},
			FederationMetaData: plan.FederationMetaData{
				Keys: plan.FederationFieldConfigurations{
					{TypeName: "Team", SelectionSet: "id"},
				},
			},
		},
		mustCustomConfiguration(t, ConfigurationInput{
			Fetch:               &FetchConfiguration{URL: "http://public"},
			SchemaConfiguration: mustSchema(t, &FederationConfiguration{Enabled: true, ServiceSDL: publicSDL}, publicSDL),
		}),
	)

	users := mustDataSourceConfiguration(t, "users",
		&plan.DataSourceMetadata{
			RootNodes: []plan.TypeField{
				{TypeName: "ModerationQueue", FieldNames: []string{"id"}, ExternalFieldNames: []string{"team"}},
				{TypeName: "Team", FieldNames: []string{"id"}, ExternalFieldNames: []string{"name"}},
				{TypeName: "User", FieldNames: []string{"id", "teamView", "teamTypeView"}},
			},
			FederationMetaData: plan.FederationMetaData{
				Keys: plan.FederationFieldConfigurations{
					{TypeName: "ModerationQueue", SelectionSet: "id"},
					{TypeName: "Team", SelectionSet: "id"},
					{TypeName: "User", SelectionSet: "id"},
				},
				ContextualArguments: plan.ContextualArgumentConfigurations{
					{TypeName: "User", FieldName: "teamView", ArgumentName: "teamName", ContextName: "teamScope", Setters: []plan.ContextSetterConfiguration{{TypeName: "ModerationQueue", SelectionSet: "team { name }"}}},
					{TypeName: "User", FieldName: "teamTypeView", ArgumentName: "typeName", ContextName: "teamTypeScope", Setters: []plan.ContextSetterConfiguration{{TypeName: "ModerationQueue", SelectionSet: "team { __typename }"}}},
				},
			},
		},
		mustCustomConfiguration(t, ConfigurationInput{
			Fetch:               &FetchConfiguration{URL: "http://users"},
			SchemaConfiguration: mustSchema(t, &FederationConfiguration{Enabled: true, ServiceSDL: usersSDL}, usersSDL),
		}),
	)

	return plan.Configuration{
		DataSources:                  []plan.DataSource{moderation, public, users},
		DisableResolveFieldPositions: true,
		Fields: plan.FieldConfigurations{
			{TypeName: "User", FieldName: "teamView", Arguments: plan.ArgumentsConfigurations{{Name: "teamName", SourceType: plan.ContextualArgumentSource}}},
			{TypeName: "User", FieldName: "teamTypeView", Arguments: plan.ArgumentsConfigurations{{Name: "typeName", SourceType: plan.ContextualArgumentSource}}},
		},
	}
}

func contextualField(name string, value resolve.Node, onTypeNames ...string) *resolve.Field {
	field := &resolve.Field{Name: []byte(name), Value: value}
	for _, typeName := range onTypeNames {
		field.OnTypeNames = append(field.OnTypeNames, []byte(typeName))
	}
	return field
}

func contextualObject(path string, nullable bool, typeName string, possibleTypes []string, fields ...*resolve.Field) *resolve.Object {
	object := &resolve.Object{
		Nullable:      nullable,
		TypeName:      typeName,
		PossibleTypes: make(map[string]struct{}, len(possibleTypes)),
		Fields:        fields,
	}
	if path != "" {
		object.Path = []string{path}
	}
	for _, possibleType := range possibleTypes {
		object.PossibleTypes[possibleType] = struct{}{}
	}
	return object
}

func contextualList(path string, item resolve.Node) *resolve.Array {
	array := &resolve.Array{Item: item}
	if path != "" {
		array.Path = []string{path}
	}
	return array
}

func contextualFetch(fetchID int, dependsOn []int, input string, variables ...resolve.Variable) *resolve.SingleFetch {
	return &resolve.SingleFetch{
		FetchDependencies:    resolve.FetchDependencies{FetchID: fetchID, DependsOnFetchIDs: dependsOn},
		DataSourceIdentifier: []byte("graphql_datasource.Source"),
		FetchConfiguration: resolve.FetchConfiguration{
			Input:          input,
			DataSource:     &Source{},
			Variables:      variables,
			PostProcessing: DefaultPostProcessingConfiguration,
		},
	}
}

func contextualEntityFetch(fetchID int, dependsOn []int, batch bool, input string, variables ...resolve.Variable) *resolve.SingleFetch {
	fetch := contextualFetch(fetchID, dependsOn, input, variables...)
	fetch.SetTemplateOutputToNullOnVariableNull = true
	if batch {
		fetch.RequiresEntityBatchFetch = true
		fetch.PostProcessing = EntitiesPostProcessingConfiguration
	} else {
		fetch.RequiresEntityFetch = true
		fetch.PostProcessing = SingleEntityPostProcessingConfiguration
	}
	return fetch
}

func contextualDeferredFetch(fetch *resolve.SingleFetch, deferID int) *resolve.SingleFetch {
	fetch.DeferID = deferID
	return fetch
}

func contextualResponsePath(path []resolve.FetchItemPathElement) string {
	var responsePath []string
	for i, element := range path {
		responsePath = append(responsePath, element.Path...)
		if element.Kind == resolve.FetchItemPathElementKindArray && i < len(path)-1 {
			responsePath = append(responsePath, "@")
		}
	}
	return strings.Join(responsePath, ".")
}

func contextualClientVariable(name string) *resolve.ContextVariable {
	return &resolve.ContextVariable{Path: []string{name}, Renderer: resolve.NewJSONVariableRenderer()}
}

// contextualRepresentation renders __typename, the required fields and the id key, the order of the representation builder.
func contextualRepresentation(typeName string, requiredFields ...*resolve.Field) *resolve.ResolvableObjectVariable {
	representationFields := []*resolve.Field{
		contextualField("__typename", &resolve.String{Path: []string{"__typename"}}, typeName),
	}
	for _, field := range requiredFields {
		field.OnTypeNames = [][]byte{[]byte(typeName)}
		representationFields = append(representationFields, field)
	}
	representationFields = append(representationFields, contextualField("id", &resolve.Scalar{Path: []string{"id"}}, typeName))
	return &resolve.ResolvableObjectVariable{
		Renderer: resolve.NewGraphQLVariableResolveRenderer(&resolve.Object{Nullable: true, Fields: representationFields}),
	}
}

func contextualVariable(name string, levels ...resolve.ContextualArgumentLevel) *resolve.ContextualArgumentVariable {
	return &resolve.ContextualArgumentVariable{Capture: resolve.ContextualArgumentCapture{VariableName: name, Levels: levels}}
}

func contextualStaticLevel(depth int, typeName string, path ...string) resolve.ContextualArgumentLevel {
	return resolve.ContextualArgumentLevel{Depth: depth, Setters: []resolve.ContextualArgumentSetter{{TypeName: typeName, Path: path}}}
}

func contextualTypenameLevel(depth int, setters ...resolve.ContextualArgumentSetter) resolve.ContextualArgumentLevel {
	return resolve.ContextualArgumentLevel{Depth: depth, MatchByTypename: true, Setters: setters}
}

func contextualSetter(typeName string, path ...string) resolve.ContextualArgumentSetter {
	return resolve.ContextualArgumentSetter{TypeName: typeName, Path: path}
}

func contextualProfile() *resolve.Field {
	return contextualField("profile", contextualObject("profile", true, "Profile", []string{"Profile"},
		contextualField("displayName", &resolve.String{Path: []string{"displayName"}}),
	))
}

func contextualUsers(path string, fields ...*resolve.Field) *resolve.Array {
	return contextualList(path, contextualObject("", false, "User", []string{"User"}, fields...))
}

func contextualQueue(path string, nullable bool, fields ...*resolve.Field) *resolve.Object {
	return contextualObject(path, nullable, "ModerationQueue", []string{"ModerationQueue"}, fields...)
}

func TestGraphQLDataSourceFederation_ContextualArguments(t *testing.T) {
	t.Run("motivating fixture", func(t *testing.T) {
		planConfiguration := contextualArgumentsMotivatingConfiguration(t)

		t.Run("flagged users read the queue value in a dependent entity fetch", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsMotivatingDefinition, `
				query FlaggedUsers {
					moderationQueue(id: "q1") {
						flaggedUsers {
							id
							profile {
								displayName
							}
						}
					}
				}`,
				"FlaggedUsers",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://moderation","body":{"query":"query($a: ID!){moderationQueue(id: $a){flaggedUsers {id __typename} __context_0: includeSuspended}}","variables":{"a":$$0$$}}}`,
								contextualClientVariable("a"),
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $contextualArgument_0){displayName}}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_0")),
								contextualRepresentation("User"),
							), "moderationQueue.flaggedUsers", resolve.ObjectPath("moderationQueue"), resolve.ArrayPath("flaggedUsers")),
						),
						Data: &resolve.Object{
							Fields: []*resolve.Field{
								contextualField("moderationQueue", contextualQueue("moderationQueue", true,
									contextualField("flaggedUsers", contextualUsers("flaggedUsers",
										contextualField("id", &resolve.Scalar{Path: []string{"id"}}),
										contextualProfile(),
									)),
								)),
							},
						},
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})

		t.Run("fetch reasons and dependencies mark the context selection", func(t *testing.T) {
			fieldInfo := func(name, parentTypeName, namedType, dataSourceName string) *resolve.FieldInfo {
				return &resolve.FieldInfo{
					Name:                name,
					ExactParentTypeName: parentTypeName,
					ParentTypeNames:     []string{parentTypeName},
					NamedType:           namedType,
					Source:              resolve.TypeFieldSource{IDs: []string{dataSourceName}, Names: []string{dataSourceName}},
				}
			}

			moderationFetch := contextualFetch(0, nil,
				`{"method":"POST","url":"http://moderation","body":{"query":"query($a: ID!){moderationQueue(id: $a){flaggedUsers {id __typename} __context_0: includeSuspended}}","variables":{"a":$$0$$}}}`,
				contextualClientVariable("a"),
			)
			moderationFetch.Info = &resolve.FetchInfo{
				DataSourceID:   "moderation",
				DataSourceName: "moderation",
				RootFields:     []resolve.GraphCoordinate{{TypeName: "Query", FieldName: "moderationQueue"}},
				OperationType:  ast.OperationTypeQuery,
				FetchReasons: []resolve.FetchReason{
					{TypeName: "ModerationQueue", FieldName: "flaggedUsers", ByUser: true},
					{TypeName: "ModerationQueue", FieldName: "includeSuspended", BySubgraphs: []string{"users"}, IsContext: true},
					{TypeName: "Query", FieldName: "moderationQueue", ByUser: true, Nullable: true},
					{TypeName: "User", FieldName: "id", BySubgraphs: []string{"users"}, ByUser: true, IsKey: true},
				},
			}

			usersFetch := contextualEntityFetch(1, []int{0}, true,
				`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $contextualArgument_0){displayName}}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
				contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_0")),
				contextualRepresentation("User"),
			)
			usersFetch.Info = &resolve.FetchInfo{
				DataSourceID:   "users",
				DataSourceName: "users",
				RootFields:     []resolve.GraphCoordinate{{TypeName: "User", FieldName: "profile"}},
				OperationType:  ast.OperationTypeQuery,
				CoordinateDependencies: []resolve.FetchDependency{
					{
						Coordinate:      resolve.GraphCoordinate{TypeName: "User", FieldName: "profile"},
						IsUserRequested: true,
						DependsOn: []resolve.FetchDependencyOrigin{
							{FetchID: 0, Subgraph: "moderation", Coordinate: resolve.GraphCoordinate{TypeName: "User", FieldName: "id"}, IsKey: true},
							{FetchID: 0, Subgraph: "moderation", Coordinate: resolve.GraphCoordinate{TypeName: "ModerationQueue", FieldName: "includeSuspended"}, IsContext: true},
						},
					},
				},
				FetchReasons: []resolve.FetchReason{
					{TypeName: "ModerationQueue", FieldName: "flaggedUsers", ByUser: true},
					{TypeName: "Profile", FieldName: "displayName", ByUser: true},
					{TypeName: "User", FieldName: "profile", ByUser: true, Nullable: true},
				},
			}

			users := contextualUsers("flaggedUsers",
				&resolve.Field{
					Name:  []byte("id"),
					Value: &resolve.Scalar{Path: []string{"id"}},
					Info:  fieldInfo("id", "User", "ID", "moderation"),
				},
				&resolve.Field{
					Name: []byte("profile"),
					Value: contextualObject("profile", true, "Profile", []string{"Profile"},
						&resolve.Field{
							Name:  []byte("displayName"),
							Value: &resolve.String{Path: []string{"displayName"}},
							Info:  fieldInfo("displayName", "Profile", "String", "users"),
						},
					),
					Info: fieldInfo("profile", "User", "Profile", "users"),
				},
			)
			users.Item.(*resolve.Object).SourceName = "moderation"
			users.Item.(*resolve.Object).Fields[1].Value.(*resolve.Object).SourceName = "users"
			queue := contextualQueue("moderationQueue", true, &resolve.Field{
				Name:  []byte("flaggedUsers"),
				Value: users,
				Info:  fieldInfo("flaggedUsers", "ModerationQueue", "User", "moderation"),
			})
			queue.SourceName = "moderation"

			RunWithPermutations(t, contextualArgumentsMotivatingDefinition, `
				query FlaggedUsers {
					moderationQueue(id: "q1") {
						flaggedUsers {
							id
							profile {
								displayName
							}
						}
					}
				}`,
				"FlaggedUsers",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Info: &resolve.GraphQLResponseInfo{OperationType: ast.OperationTypeQuery},
						Fetches: resolve.Sequence(
							resolve.Single(moderationFetch),
							resolve.SingleWithPath(usersFetch, "moderationQueue.flaggedUsers", resolve.ObjectPath("moderationQueue"), resolve.ArrayPath("flaggedUsers")),
						),
						Data: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name:  []byte("moderationQueue"),
									Value: queue,
									Info:  fieldInfo("moderationQueue", "Query", "ModerationQueue", "moderation"),
								},
							},
						},
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
				WithFetchReasons(),
			)
		})

		t.Run("post author receives a literal null without a setter", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsMotivatingDefinition, `
				query PostAuthor {
					post(id: "p1") {
						author {
							id
							profile {
								displayName
							}
						}
					}
				}`,
				"PostAuthor",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://public","body":{"query":"query($a: ID!){post(id: $a){author {id __typename}}}","variables":{"a":$$0$$}}}`,
								contextualClientVariable("a"),
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, false,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $a: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $a){displayName}}}}","variables":{"a":null,"representations":[$$0$$]}}}`,
								contextualRepresentation("User"),
							), "post.author", resolve.ObjectPath("post"), resolve.ObjectPath("author")),
						),
						Data: &resolve.Object{
							Fields: []*resolve.Field{
								contextualField("post", contextualObject("post", true, "Post", []string{"Post"},
									contextualField("author", contextualObject("author", false, "User", []string{"User"},
										contextualField("id", &resolve.Scalar{Path: []string{"id"}}),
										contextualProfile(),
									)),
								)),
							},
						},
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})
	})

	t.Run("same subgraph setter", func(t *testing.T) {
		planConfiguration := contextualArgumentsSameSubgraphConfiguration(t)

		t.Run("forces an entity fetch for the consumer", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsSameSubgraphDefinition, `
				query SameSubgraph {
					moderationQueue(id: "q1") {
						flaggedUsers {
							name
							profile {
								displayName
							}
						}
					}
				}`,
				"SameSubgraph",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://users","body":{"query":"query($a: ID!){moderationQueue(id: $a){flaggedUsers {name __typename id} __context_0: includeSuspended}}","variables":{"a":$$0$$}}}`,
								contextualClientVariable("a"),
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $contextualArgument_0){displayName}}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_0")),
								contextualRepresentation("User"),
							), "moderationQueue.flaggedUsers", resolve.ObjectPath("moderationQueue"), resolve.ArrayPath("flaggedUsers")),
						),
						Data: &resolve.Object{
							Fields: []*resolve.Field{
								contextualField("moderationQueue", contextualQueue("moderationQueue", true,
									contextualField("flaggedUsers", contextualUsers("flaggedUsers",
										contextualField("name", &resolve.String{Path: []string{"name"}, Nullable: true}),
										contextualProfile(),
									)),
								)),
							},
						},
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})

		t.Run("consumer deeper inside a fetch of its own subgraph gets its own entity fetch", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsSameSubgraphDefinition, `
				query DeeperConsumer {
					moderationQueue(id: "q1") {
						flaggedUsers {
							friends {
								profile {
									displayName
								}
							}
						}
					}
				}`,
				"DeeperConsumer",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://users","body":{"query":"query($a: ID!){moderationQueue(id: $a){flaggedUsers {friends {__typename id}} __context_0: includeSuspended}}","variables":{"a":$$0$$}}}`,
								contextualClientVariable("a"),
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $contextualArgument_0){displayName}}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_0")),
								contextualRepresentation("User"),
							), "moderationQueue.flaggedUsers.@.friends", resolve.ObjectPath("moderationQueue"), resolve.ArrayPath("flaggedUsers"), resolve.ArrayPath("friends")),
						),
						Data: &resolve.Object{
							Fields: []*resolve.Field{
								contextualField("moderationQueue", contextualQueue("moderationQueue", true,
									contextualField("flaggedUsers", contextualUsers("flaggedUsers",
										contextualField("friends", contextualUsers("friends", contextualProfile())),
									)),
								)),
							},
						},
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})

		t.Run("client variable named like the contextual variable keeps its name", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsSameSubgraphDefinition, `
				query ClientVariableName($contextualArgument_0: ID!) {
					moderationQueue(id: $contextualArgument_0) {
						flaggedUsers {
							profile {
								displayName
							}
						}
					}
				}`,
				"ClientVariableName",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://users","body":{"query":"query($contextualArgument_0: ID!){moderationQueue(id: $contextualArgument_0){flaggedUsers {__typename id} __context_0: includeSuspended}}","variables":{"contextualArgument_0":$$0$$}}}`,
								contextualClientVariable("contextualArgument_0"),
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $_contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $_contextualArgument_0){displayName}}}}","variables":{"representations":[$$1$$],"_contextualArgument_0":$$0$$}}}`,
								contextualVariable("_contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_0")),
								contextualRepresentation("User"),
							), "moderationQueue.flaggedUsers", resolve.ObjectPath("moderationQueue"), resolve.ArrayPath("flaggedUsers")),
						),
						Data: &resolve.Object{
							Fields: []*resolve.Field{
								contextualField("moderationQueue", contextualQueue("moderationQueue", true,
									contextualField("flaggedUsers", contextualUsers("flaggedUsers", contextualProfile())),
								)),
							},
						},
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})

		t.Run("client supplied argument is ignored when validation is skipped", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsSameSubgraphDefinition, `
				query ClientArgument {
					moderationQueue(id: "q1") {
						flaggedUsers {
							profile(includeSuspended: true) {
								displayName
							}
						}
					}
				}`,
				"ClientArgument",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://users","body":{"query":"query($a: ID!){moderationQueue(id: $a){flaggedUsers {__typename id} __context_0: includeSuspended}}","variables":{"a":$$0$$}}}`,
								contextualClientVariable("a"),
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $contextualArgument_0){displayName}}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_0")),
								contextualRepresentation("User"),
							), "moderationQueue.flaggedUsers", resolve.ObjectPath("moderationQueue"), resolve.ArrayPath("flaggedUsers")),
						),
						Data: &resolve.Object{
							Fields: []*resolve.Field{
								contextualField("moderationQueue", contextualQueue("moderationQueue", true,
									contextualField("flaggedUsers", contextualUsers("flaggedUsers", contextualProfile())),
								)),
							},
						},
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
				WithoutValidation(),
			)
		})

		t.Run("deferred field below a consumer deeper inside a fetch of its own subgraph", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsSameSubgraphDefinition, `
				query DeferredBelowConsumer {
					moderationQueue(id: "q1") {
						flaggedUsers {
							friends {
								profile {
									displayName
								}
								... @defer {
									profile {
										deferredName: displayName
									}
								}
							}
						}
					}
				}`,
				"DeferredBelowConsumer",
				&plan.DeferResponsePlan{
					Response: &resolve.GraphQLDeferResponse{
						DeferDescriptors: map[int]resolve.DeferDescriptor{
							1: {ID: 1, Path: []string{"moderationQueue", "flaggedUsers"}},
						},
						Response: &resolve.GraphQLResponse{
							Fetches: resolve.Sequence(
								resolve.Single(contextualFetch(0, nil,
									`{"method":"POST","url":"http://users","body":{"query":"query($a: ID!){moderationQueue(id: $a){flaggedUsers {friends {__typename id}} __context_0: includeSuspended}}","variables":{"a":$$0$$}}}`,
									contextualClientVariable("a"),
								)),
								resolve.SingleWithPath(contextualEntityFetch(2, []int{0}, true,
									`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $contextualArgument_0){displayName}}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
									contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_0")),
									contextualRepresentation("User"),
								), "moderationQueue.flaggedUsers.@.friends", resolve.ObjectPath("moderationQueue"), resolve.ArrayPath("flaggedUsers"), resolve.ArrayPath("friends")),
							),
							Data: &resolve.Object{
								Fields: []*resolve.Field{
									contextualField("moderationQueue", contextualQueue("moderationQueue", true,
										contextualField("flaggedUsers", contextualUsers("flaggedUsers",
											contextualField("friends", contextualUsers("friends",
												contextualField("profile", contextualObject("profile", true, "Profile", []string{"Profile"},
													contextualField("displayName", &resolve.String{Path: []string{"displayName"}}),
													&resolve.Field{
														Name:  []byte("deferredName"),
														Defer: &resolve.DeferField{DeferID: 1},
														Value: &resolve.String{Path: []string{"deferredName"}},
													},
												)),
											)),
										)),
									)),
								},
							},
						},
						DeferTree: resolve.DeferSingle(&resolve.DeferFetchGroup{
							DeferID: 1,
							Fetches: resolve.Sequence(
								resolve.SingleWithPath(contextualDeferredFetch(contextualEntityFetch(1, []int{0}, true,
									`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $contextualArgument_0){deferredName: displayName}}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
									contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_0")),
									contextualRepresentation("User"),
								), 1), "moderationQueue.flaggedUsers.@.friends", resolve.ObjectPath("moderationQueue"), resolve.ArrayPath("flaggedUsers"), resolve.ArrayPath("friends")),
							),
						}),
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
				WithDefer(),
				WithCalculateFieldDependencies(),
			)
		})
	})

	t.Run("candidate levels", func(t *testing.T) {
		planConfiguration := contextualArgumentsConfiguration(t)

		t.Run("nested setters are collected nearest first up to the first static level", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsDefinition, `
				query NestedSetters {
					organization(id: "o1") {
						allQueues {
							flaggedUsers {
								profile {
									displayName
								}
							}
						}
					}
				}`,
				"NestedSetters",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://moderation","body":{"query":"query($a: ID!){organization(id: $a){allQueues {__typename flaggedUsers {__typename id} ... on ModerationQueue {__context_0: includeSuspended}} __context_1: includeSuspended}}","variables":{"a":$$0$$}}}`,
								contextualClientVariable("a"),
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $contextualArgument_0){displayName}}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0",
									contextualTypenameLevel(2, contextualSetter("ModerationQueue", "__context_0")),
									contextualStaticLevel(1, "Organization", "__context_1"),
								),
								contextualRepresentation("User"),
							), "organization.allQueues.@.flaggedUsers", resolve.ObjectPath("organization"), resolve.ArrayPath("allQueues"), resolve.ArrayPath("flaggedUsers")),
						),
						Data: &resolve.Object{
							Fields: []*resolve.Field{
								contextualField("organization", contextualObject("organization", true, "Organization", []string{"Organization"},
									contextualField("allQueues", contextualList("allQueues", contextualObject("", false, "Queue", []string{"AppealQueue", "ModerationQueue"},
										contextualField("flaggedUsers", contextualUsers("flaggedUsers", contextualProfile())),
									))),
								)),
							},
						},
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})

		t.Run("collection stops at the nearest static level", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsDefinition, `
				query StaticLevel {
					organization(id: "o1") {
						queues {
							flaggedUsers {
								profile {
									displayName
								}
							}
						}
					}
				}`,
				"StaticLevel",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://moderation","body":{"query":"query($a: ID!){organization(id: $a){queues {flaggedUsers {__typename id} __context_0: includeSuspended}}}","variables":{"a":$$0$$}}}`,
								contextualClientVariable("a"),
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $contextualArgument_0){displayName}}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(2, "ModerationQueue", "__context_0")),
								contextualRepresentation("User"),
							), "organization.queues.@.flaggedUsers", resolve.ObjectPath("organization"), resolve.ArrayPath("queues"), resolve.ArrayPath("flaggedUsers")),
						),
						Data: &resolve.Object{
							Fields: []*resolve.Field{
								contextualField("organization", contextualObject("organization", true, "Organization", []string{"Organization"},
									contextualField("queues", contextualList("queues", contextualQueue("", false,
										contextualField("flaggedUsers", contextualUsers("flaggedUsers", contextualProfile())),
									))),
								)),
							},
						},
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})

		t.Run("setter does not feed its own field", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsDefinition, `
				query SetterOwnField {
					moderationQueue(id: "q1") {
						summary
					}
					organization(id: "o1") {
						queues {
							summary
						}
					}
				}`,
				"SetterOwnField",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://moderation","body":{"query":"query($a: ID!, $b: ID!){moderationQueue(id: $a){__typename id} organization(id: $b){queues {__typename id} __context_0: includeSuspended}}","variables":{"b":$$1$$,"a":$$0$$}}}`,
								contextualClientVariable("a"),
								contextualClientVariable("b"),
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, false,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $a: Boolean){_entities(representations: $representations){... on ModerationQueue {__typename summary(includeSuspended: $a)}}}","variables":{"a":null,"representations":[$$0$$]}}}`,
								contextualRepresentation("ModerationQueue"),
							), "moderationQueue", resolve.ObjectPath("moderationQueue")),
							resolve.SingleWithPath(contextualEntityFetch(2, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on ModerationQueue {__typename summary(includeSuspended: $contextualArgument_0)}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(1, "Organization", "__context_0")),
								contextualRepresentation("ModerationQueue"),
							), "organization.queues", resolve.ObjectPath("organization"), resolve.ArrayPath("queues")),
						),
						Data: &resolve.Object{
							Fields: []*resolve.Field{
								contextualField("moderationQueue", contextualQueue("moderationQueue", true,
									contextualField("summary", &resolve.String{Path: []string{"summary"}, Nullable: true}),
								)),
								contextualField("organization", contextualObject("organization", true, "Organization", []string{"Organization"},
									contextualField("queues", contextualList("queues", contextualQueue("", false,
										contextualField("summary", &resolve.String{Path: []string{"summary"}, Nullable: true}),
									))),
								)),
							},
						},
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})
	})

	t.Run("setter types", func(t *testing.T) {
		planConfiguration := contextualArgumentsConfiguration(t)

		queuesData := func(fields ...*resolve.Field) *resolve.Object {
			return &resolve.Object{
				Fields: []*resolve.Field{
					contextualField("queues", contextualList("queues", contextualObject("", false, "Queue", []string{"AppealQueue", "ModerationQueue"},
						contextualField("flaggedUsers", contextualUsers("flaggedUsers", fields...)),
					))),
				},
			}
		}

		t.Run("interface setter at an abstract level is matched by typename", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsDefinition, `
				query InterfaceSetter {
					queues {
						flaggedUsers {
							queueView
						}
					}
				}`,
				"InterfaceSetter",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://moderation","body":{"query":"{queues {__typename flaggedUsers {__typename id} ... on AppealQueue {__context_0: includeSuspended} ... on ModerationQueue {__context_1: includeSuspended}}}"}}`,
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename queueView(includeSuspended: $contextualArgument_0)}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualTypenameLevel(1,
									contextualSetter("AppealQueue", "__context_0"),
									contextualSetter("ModerationQueue", "__context_1"),
								)),
								contextualRepresentation("User"),
							), "queues.@.flaggedUsers", resolve.ArrayPath("queues"), resolve.ArrayPath("flaggedUsers")),
						),
						Data: queuesData(contextualField("queueView", &resolve.String{Path: []string{"queueView"}, Nullable: true})),
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})

		t.Run("type conditioned selections of a union context at an abstract level", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsDefinition, `
				query UnionContextAtAbstractLevel {
					queues {
						flaggedUsers {
							scopeView
						}
					}
				}`,
				"UnionContextAtAbstractLevel",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://moderation","body":{"query":"{queues {__typename flaggedUsers {__typename id} ... on AppealQueue {__context_0: showSuspended} ... on ModerationQueue {__context_1: includeSuspended}}}"}}`,
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename scopeView(show: $contextualArgument_0)}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualTypenameLevel(1,
									contextualSetter("AppealQueue", "__context_0"),
									contextualSetter("ModerationQueue", "__context_1"),
								)),
								contextualRepresentation("User"),
							), "queues.@.flaggedUsers", resolve.ArrayPath("queues"), resolve.ArrayPath("flaggedUsers")),
						),
						Data: queuesData(contextualField("scopeView", &resolve.String{Path: []string{"scopeView"}, Nullable: true})),
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})

		t.Run("abstract ancestor with partial setter types injects only the setters", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsDefinition, `
				query PartialSetterTypes {
					queues {
						flaggedUsers {
							profile {
								displayName
							}
						}
					}
				}`,
				"PartialSetterTypes",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://moderation","body":{"query":"{queues {__typename flaggedUsers {__typename id} ... on ModerationQueue {__context_0: includeSuspended}}}"}}`,
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $contextualArgument_0){displayName}}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualTypenameLevel(1, contextualSetter("ModerationQueue", "__context_0"))),
								contextualRepresentation("User"),
							), "queues.@.flaggedUsers", resolve.ArrayPath("queues"), resolve.ArrayPath("flaggedUsers")),
						),
						Data: queuesData(contextualProfile()),
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})

		t.Run("union setter with type conditioned selections and a member without a selection", func(t *testing.T) {
			scopeView := contextualField("scopeView", &resolve.String{Path: []string{"scopeView"}, Nullable: true})
			RunWithPermutations(t, contextualArgumentsDefinition, `
				query UnionSetter {
					scopes {
						... on Team {
							members {
								scopeView
							}
						}
						... on ModerationQueue {
							flaggedUsers {
								scopeView
							}
						}
						... on AppealQueue {
							flaggedUsers {
								scopeView
							}
						}
					}
				}`,
				"UnionSetter",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://moderation","body":{"query":"{scopes {__typename ... on Team {members {__typename id}} ... on ModerationQueue {flaggedUsers {__typename id} __context_0: includeSuspended} ... on AppealQueue {flaggedUsers {__typename id} __context_1: showSuspended}}}"}}`,
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename scopeView(show: $contextualArgument_0)}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(1, "Team")),
								contextualRepresentation("User"),
							), "scopes.@.members", resolve.ArrayPath("scopes"), resolve.PathElementWithTypeNames(resolve.ArrayPath("members"), []string{"Team"})),
							resolve.SingleWithPath(contextualEntityFetch(2, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename scopeView(show: $contextualArgument_0)}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_0")),
								contextualRepresentation("User"),
							), "scopes.@.flaggedUsers", resolve.ArrayPath("scopes"), resolve.PathElementWithTypeNames(resolve.ArrayPath("flaggedUsers"), []string{"ModerationQueue"})),
							resolve.SingleWithPath(contextualEntityFetch(3, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename scopeView(show: $contextualArgument_0)}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(1, "AppealQueue", "__context_1")),
								contextualRepresentation("User"),
							), "scopes.@.flaggedUsers", resolve.ArrayPath("scopes"), resolve.PathElementWithTypeNames(resolve.ArrayPath("flaggedUsers"), []string{"AppealQueue"})),
						),
						Data: &resolve.Object{
							Fields: []*resolve.Field{
								contextualField("scopes", contextualList("scopes", contextualObject("", false, "Scope", []string{"AppealQueue", "ModerationQueue", "Team"},
									contextualField("members", contextualUsers("members", scopeView), "Team"),
									contextualField("flaggedUsers", contextualUsers("flaggedUsers", scopeView), "ModerationQueue"),
									contextualField("flaggedUsers", contextualUsers("flaggedUsers", scopeView), "AppealQueue"),
								))),
							},
						},
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})

		t.Run("setter type missing from the level's subgraph is dropped", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsContributedImplementationDefinition, `
				query ContributedImplementation {
					queues {
						members {
							profile {
								displayName
							}
						}
					}
				}`,
				"ContributedImplementation",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://moderation","body":{"query":"{queues {__typename members {__typename id} ... on ModerationQueue {__context_0: includeSuspended}}}"}}`,
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $contextualArgument_0){displayName}}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualTypenameLevel(1, contextualSetter("ModerationQueue", "__context_0"))),
								contextualRepresentation("User"),
							), "queues.@.members", resolve.ArrayPath("queues"), resolve.ArrayPath("members")),
						),
						Data: &resolve.Object{
							Fields: []*resolve.Field{
								contextualField("queues", contextualList("queues", contextualObject("", false, "Queue", []string{"EscalationQueue", "ModerationQueue"},
									contextualField("members", contextualUsers("members", contextualProfile())),
								))),
							},
						},
					},
				},
				contextualArgumentsContributedImplementationConfiguration(t),
				WithDefaultPostProcessor(),
			)
		})
	})

	t.Run("operation path", func(t *testing.T) {
		planConfiguration := contextualArgumentsConfiguration(t)

		t.Run("named fragments and aliases on the path", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsDefinition, `
				query FragmentsAndAliases {
					qs: queues {
						...QueueUsers
					}
				}

				fragment QueueUsers on ModerationQueue {
					users: flaggedUsers {
						... on User {
							p: profile {
								displayName
							}
						}
					}
				}`,
				"FragmentsAndAliases",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://moderation","body":{"query":"{qs: queues {__typename ... on ModerationQueue {users: flaggedUsers {__typename id} __context_0: includeSuspended}}}"}}`,
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename p: profile(includeSuspended: $contextualArgument_0){displayName}}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_0")),
								contextualRepresentation("User"),
							), "qs.@.users", resolve.ArrayPath("qs"), resolve.PathElementWithTypeNames(resolve.ArrayPath("users"), []string{"ModerationQueue"})),
						),
						Data: &resolve.Object{
							Fields: []*resolve.Field{
								contextualField("qs", contextualList("qs", contextualObject("", false, "Queue", []string{"AppealQueue", "ModerationQueue"},
									contextualField("users", contextualUsers("users",
										contextualField("p", contextualObject("p", true, "Profile", []string{"Profile"},
											contextualField("displayName", &resolve.String{Path: []string{"displayName"}}),
										)),
									), "ModerationQueue"),
								))),
							},
						},
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})

		t.Run("client aliases colliding with the setter field and the context alias", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsDefinition, `
				query AliasCollision {
					moderationQueue(id: "q1") {
						includeSuspended: id
						__context_0: id
						flaggedUsers {
							profile {
								displayName
							}
						}
					}
				}`,
				"AliasCollision",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://moderation","body":{"query":"query($a: ID!){moderationQueue(id: $a){includeSuspended: id __context_0: id flaggedUsers {__typename id} __context_0_1: includeSuspended}}","variables":{"a":$$0$$}}}`,
								contextualClientVariable("a"),
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $contextualArgument_0){displayName}}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_0_1")),
								contextualRepresentation("User"),
							), "moderationQueue.flaggedUsers", resolve.ObjectPath("moderationQueue"), resolve.ArrayPath("flaggedUsers")),
						),
						Data: &resolve.Object{
							Fields: []*resolve.Field{
								contextualField("moderationQueue", contextualQueue("moderationQueue", true,
									contextualField("includeSuspended", &resolve.Scalar{Path: []string{"includeSuspended"}}),
									contextualField("__context_0", &resolve.Scalar{Path: []string{"__context_0"}}),
									contextualField("flaggedUsers", contextualUsers("flaggedUsers", contextualProfile())),
								)),
							},
						},
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})

		t.Run("client field equal to a context selection is not reused", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsDefinition, `
				query ClientContextAlias {
					moderationQueue(id: "q1") {
						__context_0: includeSuspended
						flaggedUsers {
							profile {
								displayName
							}
						}
					}
				}`,
				"ClientContextAlias",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://moderation","body":{"query":"query($a: ID!){moderationQueue(id: $a){__context_0: includeSuspended flaggedUsers {__typename id} __context_0_1: includeSuspended}}","variables":{"a":$$0$$}}}`,
								contextualClientVariable("a"),
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $contextualArgument_0){displayName}}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_0_1")),
								contextualRepresentation("User"),
							), "moderationQueue.flaggedUsers", resolve.ObjectPath("moderationQueue"), resolve.ArrayPath("flaggedUsers")),
						),
						Data: &resolve.Object{
							Fields: []*resolve.Field{
								contextualField("moderationQueue", contextualQueue("moderationQueue", true,
									contextualField("__context_0", &resolve.Boolean{Path: []string{"__context_0"}}),
									contextualField("flaggedUsers", contextualUsers("flaggedUsers", contextualProfile())),
								)),
							},
						},
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})

		t.Run("client field aliased like a context selection with a nested alias is not reused", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsDefinition, `
				query NestedClientAlias {
					moderationQueue(id: "q1") {
						__context_0: flaggedUsers {
							uid: id
						}
						flaggedUsers {
							peers
						}
					}
				}`,
				"NestedClientAlias",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://moderation","body":{"query":"query($a: ID!){moderationQueue(id: $a){__context_0: flaggedUsers {uid: id} flaggedUsers {__typename id} __context_0_1: flaggedUsers {id}}}","variables":{"a":$$0$$}}}`,
								contextualClientVariable("a"),
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: [ID]){_entities(representations: $representations){... on User {__typename peers(ids: $contextualArgument_0)}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_0_1", "id")),
								contextualRepresentation("User"),
							), "moderationQueue.flaggedUsers", resolve.ObjectPath("moderationQueue"), resolve.ArrayPath("flaggedUsers")),
						),
						Data: &resolve.Object{
							Fields: []*resolve.Field{
								contextualField("moderationQueue", contextualQueue("moderationQueue", true,
									contextualField("__context_0", contextualUsers("__context_0", contextualField("uid", &resolve.Scalar{Path: []string{"uid"}}))),
									contextualField("flaggedUsers", contextualUsers("flaggedUsers", contextualField("peers", &resolve.String{Path: []string{"peers"}, Nullable: true}))),
								)),
							},
						},
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})
	})

	t.Run("dependencies", func(t *testing.T) {
		planConfiguration := contextualArgumentsConfiguration(t)

		flaggedUsersData := func(fields ...*resolve.Field) *resolve.Object {
			return &resolve.Object{
				Fields: []*resolve.Field{
					contextualField("moderationQueue", contextualQueue("moderationQueue", true,
						contextualField("flaggedUsers", contextualUsers("flaggedUsers", fields...)),
					)),
				},
			}
		}

		t.Run("setter field owned by another subgraph adds a dependency", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsDefinition, `
				query SetterFieldOwnedElsewhere {
					moderationQueue(id: "q1") {
						flaggedUsers {
							risk
						}
					}
				}`,
				"SetterFieldOwnedElsewhere",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://moderation","body":{"query":"query($a: ID!){moderationQueue(id: $a){flaggedUsers {__typename id} __typename id}}","variables":{"a":$$0$$}}}`,
								contextualClientVariable("a"),
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, false,
								`{"method":"POST","url":"http://public","body":{"query":"query($representations: [_Any!]!){_entities(representations: $representations){... on ModerationQueue {__typename __context_0: riskLevel}}}","variables":{"representations":[$$0$$]}}}`,
								contextualRepresentation("ModerationQueue"),
							), "moderationQueue", resolve.ObjectPath("moderationQueue")),
							resolve.SingleWithPath(contextualEntityFetch(2, []int{0, 1}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Int){_entities(representations: $representations){... on User {__typename risk(level: $contextualArgument_0)}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_0")),
								contextualRepresentation("User"),
							), "moderationQueue.flaggedUsers", resolve.ObjectPath("moderationQueue"), resolve.ArrayPath("flaggedUsers")),
						),
						Data: flaggedUsersData(contextualField("risk", &resolve.String{Path: []string{"risk"}, Nullable: true})),
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})

		t.Run("setter field with requires", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsDefinition, `
				query SetterFieldWithRequires {
					moderationQueue(id: "q1") {
						flaggedUsers {
							badge
						}
					}
				}`,
				"SetterFieldWithRequires",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://moderation","body":{"query":"query($a: ID!){moderationQueue(id: $a){flaggedUsers {__typename id} includeSuspended __typename id}}","variables":{"a":$$0$$}}}`,
								contextualClientVariable("a"),
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, false,
								`{"method":"POST","url":"http://public","body":{"query":"query($representations: [_Any!]!){_entities(representations: $representations){... on ModerationQueue {__typename __context_0: policyLevel}}}","variables":{"representations":[$$0$$]}}}`,
								contextualRepresentation("ModerationQueue", contextualField("includeSuspended", &resolve.Boolean{Path: []string{"includeSuspended"}})),
							), "moderationQueue", resolve.ObjectPath("moderationQueue")),
							resolve.SingleWithPath(contextualEntityFetch(2, []int{0, 1}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Int){_entities(representations: $representations){... on User {__typename badge(level: $contextualArgument_0)}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_0")),
								contextualRepresentation("User"),
							), "moderationQueue.flaggedUsers", resolve.ObjectPath("moderationQueue"), resolve.ArrayPath("flaggedUsers")),
						),
						Data: flaggedUsersData(contextualField("badge", &resolve.String{Path: []string{"badge"}, Nullable: true})),
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})

		t.Run("consumer with requires and a contextual argument", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsDefinition, `
				query ConsumerWithRequires {
					moderationQueue(id: "q1") {
						flaggedUsers {
							signature
						}
					}
				}`,
				"ConsumerWithRequires",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://moderation","body":{"query":"query($a: ID!){moderationQueue(id: $a){flaggedUsers {__typename id} __context_0: includeSuspended}}","variables":{"a":$$0$$}}}`,
								contextualClientVariable("a"),
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, true,
								`{"method":"POST","url":"http://public","body":{"query":"query($representations: [_Any!]!){_entities(representations: $representations){... on User {__typename handle}}}","variables":{"representations":[$$0$$]}}}`,
								contextualRepresentation("User"),
							), "moderationQueue.flaggedUsers", resolve.ObjectPath("moderationQueue"), resolve.ArrayPath("flaggedUsers")),
							resolve.SingleWithPath(contextualEntityFetch(2, []int{0, 1}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename signature(includeSuspended: $contextualArgument_0)}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_0")),
								contextualRepresentation("User", contextualField("handle", &resolve.String{Path: []string{"handle"}, Nullable: true})),
							), "moderationQueue.flaggedUsers", resolve.ObjectPath("moderationQueue"), resolve.ArrayPath("flaggedUsers")),
						),
						Data: flaggedUsersData(contextualField("signature", &resolve.String{Path: []string{"signature"}, Nullable: true})),
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})

		t.Run("query as setter is fetched as a root field the consumer depends on", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsDefinition, `
				query QuerySetter {
					moderationQueue(id: "q1") {
						flaggedUsers {
							viewerView
						}
					}
				}`,
				"QuerySetter",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://moderation","body":{"query":"query($a: ID!){moderationQueue(id: $a){flaggedUsers {__typename id}}}","variables":{"a":$$0$$}}}`,
								contextualClientVariable("a"),
							)),
							resolve.Single(contextualFetch(1, nil,
								`{"method":"POST","url":"http://users","body":{"query":"{__context_0: viewerIncludeSuspended}"}}`,
							)),
							resolve.SingleWithPath(contextualEntityFetch(2, []int{0, 1}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename viewerView(includeSuspended: $contextualArgument_0)}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(0, "Query", "__context_0")),
								contextualRepresentation("User"),
							), "moderationQueue.flaggedUsers", resolve.ObjectPath("moderationQueue"), resolve.ArrayPath("flaggedUsers")),
						),
						Data: flaggedUsersData(contextualField("viewerView", &resolve.String{Path: []string{"viewerView"}, Nullable: true})),
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})

		t.Run("consumer under a mutation payload", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsDefinition, `
				mutation FlagUser {
					flagUser(queueId: "q1", userId: "u1") {
						flaggedUsers {
							profile {
								displayName
							}
						}
					}
				}`,
				"FlagUser",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://moderation","body":{"query":"mutation($a: ID!, $b: ID!){flagUser(queueId: $a, userId: $b){flaggedUsers {__typename id} __context_0: includeSuspended}}","variables":{"b":$$1$$,"a":$$0$$}}}`,
								contextualClientVariable("a"),
								contextualClientVariable("b"),
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $contextualArgument_0){displayName}}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_0")),
								contextualRepresentation("User"),
							), "flagUser.flaggedUsers", resolve.ObjectPath("flagUser"), resolve.ArrayPath("flaggedUsers")),
						),
						Data: &resolve.Object{
							Fields: []*resolve.Field{
								contextualField("flagUser", contextualQueue("flagUser", false,
									contextualField("flaggedUsers", contextualUsers("flaggedUsers", contextualProfile())),
								)),
							},
						},
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})
	})

	t.Run("selections and lists", func(t *testing.T) {
		planConfiguration := contextualArgumentsConfiguration(t)

		t.Run("lists of lists between setter and consumer", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsDefinition, `
				query ListsOfLists {
					queueGrid {
						flaggedUsers {
							profile {
								displayName
							}
						}
					}
					moderationQueue(id: "q1") {
						flaggedUserPages {
							profile {
								displayName
							}
						}
					}
				}`,
				"ListsOfLists",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://moderation","body":{"query":"query($a: ID!){queueGrid {flaggedUsers {__typename id} __context_0: includeSuspended} moderationQueue(id: $a){flaggedUserPages {__typename id} __context_1: includeSuspended}}","variables":{"a":$$0$$}}}`,
								contextualClientVariable("a"),
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $contextualArgument_0){displayName}}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_0")),
								contextualRepresentation("User"),
							), "queueGrid.@.flaggedUsers", resolve.ArrayPath("queueGrid"), resolve.ArrayPath("flaggedUsers")),
							resolve.SingleWithPath(contextualEntityFetch(2, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $contextualArgument_0){displayName}}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_1")),
								contextualRepresentation("User"),
							), "moderationQueue.flaggedUserPages", resolve.ObjectPath("moderationQueue"), resolve.ArrayPath("flaggedUserPages")),
						),
						Data: &resolve.Object{
							Fields: []*resolve.Field{
								contextualField("queueGrid", contextualList("queueGrid", contextualList("", contextualQueue("", false,
									contextualField("flaggedUsers", contextualUsers("flaggedUsers", contextualProfile())),
								)))),
								contextualField("moderationQueue", contextualQueue("moderationQueue", true,
									contextualField("flaggedUserPages", contextualList("flaggedUserPages", contextualUsers("", contextualProfile()))),
								)),
							},
						},
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})

		t.Run("selection crossing a list reads the nested field", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsDefinition, `
				query ListSelection {
					moderationQueue(id: "q1") {
						flaggedUsers {
							peers
						}
					}
				}`,
				"ListSelection",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://moderation","body":{"query":"query($a: ID!){moderationQueue(id: $a){flaggedUsers {__typename id} __context_0: flaggedUsers {id}}}","variables":{"a":$$0$$}}}`,
								contextualClientVariable("a"),
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: [ID]){_entities(representations: $representations){... on User {__typename peers(ids: $contextualArgument_0)}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_0", "id")),
								contextualRepresentation("User"),
							), "moderationQueue.flaggedUsers", resolve.ObjectPath("moderationQueue"), resolve.ArrayPath("flaggedUsers")),
						),
						Data: &resolve.Object{
							Fields: []*resolve.Field{
								contextualField("moderationQueue", contextualQueue("moderationQueue", true,
									contextualField("flaggedUsers", contextualUsers("flaggedUsers", contextualField("peers", &resolve.String{Path: []string{"peers"}, Nullable: true}))),
								)),
							},
						},
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})

		t.Run("selection with field arguments stays apart from the client selection", func(t *testing.T) {
			preference := func(name string) *resolve.Field {
				return contextualField(name, &resolve.String{Path: []string{name}, Nullable: true})
			}
			RunWithPermutations(t, contextualArgumentsDefinition, `
				query SelectionArguments {
					moderationQueue(id: "q1") {
						setting(key: "visible")
						flaggedUsers {
							a: preference
							b: preference
						}
					}
				}`,
				"SelectionArguments",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://moderation","body":{"query":"query($a: ID!, $b: String!, $c: String!){moderationQueue(id: $a){setting(key: $b) flaggedUsers {__typename id} __context_0: setting(key: $c)}}","variables":{"c":"suspended","b":$$1$$,"a":$$0$$}}}`,
								contextualClientVariable("a"),
								contextualClientVariable("b"),
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename a: preference(enabled: $contextualArgument_0) b: preference(enabled: $contextualArgument_0)}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_0")),
								contextualRepresentation("User"),
							), "moderationQueue.flaggedUsers", resolve.ObjectPath("moderationQueue"), resolve.ArrayPath("flaggedUsers")),
						),
						Data: &resolve.Object{
							Fields: []*resolve.Field{
								contextualField("moderationQueue", contextualQueue("moderationQueue", true,
									contextualField("setting", &resolve.Boolean{Path: []string{"setting"}, Nullable: true}),
									contextualField("flaggedUsers", contextualUsers("flaggedUsers", preference("a"), preference("b"))),
								)),
							},
						},
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})

		t.Run("identical captures share a variable and different captures get their own", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsDefinition, `
				query SharedVariables {
					moderationQueue(id: "q1") {
						flaggedUsers {
							a: profile {
								displayName
							}
							b: profile {
								displayName
							}
							digest(locale: "en")
						}
					}
				}`,
				"SharedVariables",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://moderation","body":{"query":"query($a: ID!){moderationQueue(id: $a){flaggedUsers {__typename id} __context_0: includeSuspended includeSuspended __typename id}}","variables":{"a":$$0$$}}}`,
								contextualClientVariable("a"),
							)),
							resolve.SingleWithPath(contextualEntityFetch(2, []int{0}, false,
								`{"method":"POST","url":"http://public","body":{"query":"query($representations: [_Any!]!){_entities(representations: $representations){... on ModerationQueue {__typename __context_1: policyLevel}}}","variables":{"representations":[$$0$$]}}}`,
								contextualRepresentation("ModerationQueue", contextualField("includeSuspended", &resolve.Boolean{Path: []string{"includeSuspended"}})),
							), "moderationQueue", resolve.ObjectPath("moderationQueue")),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0, 2}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean, $b: String, $contextualArgument_1: Int){_entities(representations: $representations){... on User {__typename a: profile(includeSuspended: $contextualArgument_0){displayName} b: profile(includeSuspended: $contextualArgument_0){displayName} digest(locale: $b, includeSuspended: $contextualArgument_0, level: $contextualArgument_1)}}}","variables":{"representations":[$$3$$],"contextualArgument_1":$$2$$,"b":$$1$$,"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_0")),
								contextualClientVariable("b"),
								contextualVariable("contextualArgument_1", contextualStaticLevel(1, "ModerationQueue", "__context_1")),
								contextualRepresentation("User"),
							), "moderationQueue.flaggedUsers", resolve.ObjectPath("moderationQueue"), resolve.ArrayPath("flaggedUsers")),
						),
						Data: &resolve.Object{
							Fields: []*resolve.Field{
								contextualField("moderationQueue", contextualQueue("moderationQueue", true,
									contextualField("flaggedUsers", contextualUsers("flaggedUsers",
										contextualField("a", contextualObject("a", true, "Profile", []string{"Profile"},
											contextualField("displayName", &resolve.String{Path: []string{"displayName"}}),
										)),
										contextualField("b", contextualObject("b", true, "Profile", []string{"Profile"},
											contextualField("displayName", &resolve.String{Path: []string{"displayName"}}),
										)),
										contextualField("digest", &resolve.String{Path: []string{"digest"}, Nullable: true}),
									)),
								)),
							},
						},
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})

		t.Run("sibling fragments with and without a setter get typed fetch paths", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsDefinition, `
				query SiblingFragments {
					queues {
						... on ModerationQueue {
							flaggedUsers {
								profile {
									displayName
								}
							}
						}
						... on AppealQueue {
							flaggedUsers {
								profile {
									displayName
								}
							}
						}
					}
				}`,
				"SiblingFragments",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://moderation","body":{"query":"{queues {__typename ... on ModerationQueue {flaggedUsers {__typename id} __context_0: includeSuspended} ... on AppealQueue {flaggedUsers {__typename id}}}}"}}`,
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $contextualArgument_0){displayName}}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_0")),
								contextualRepresentation("User"),
							), "queues.@.flaggedUsers", resolve.ArrayPath("queues"), resolve.PathElementWithTypeNames(resolve.ArrayPath("flaggedUsers"), []string{"ModerationQueue"})),
							resolve.SingleWithPath(contextualEntityFetch(2, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $a: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $a){displayName}}}}","variables":{"a":null,"representations":[$$0$$]}}}`,
								contextualRepresentation("User"),
							), "queues.@.flaggedUsers", resolve.ArrayPath("queues"), resolve.PathElementWithTypeNames(resolve.ArrayPath("flaggedUsers"), []string{"AppealQueue"})),
						),
						Data: &resolve.Object{
							Fields: []*resolve.Field{
								contextualField("queues", contextualList("queues", contextualObject("", false, "Queue", []string{"AppealQueue", "ModerationQueue"},
									contextualField("flaggedUsers", contextualUsers("flaggedUsers", contextualProfile()), "ModerationQueue"),
									contextualField("flaggedUsers", contextualUsers("flaggedUsers", contextualProfile()), "AppealQueue"),
								))),
							},
						},
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})
	})

	t.Run("defer", func(t *testing.T) {
		planConfiguration := contextualArgumentsConfiguration(t)

		t.Run("deferred consumer depends on the context selection of the primary fetch", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsDefinition, `
				query DeferredConsumer {
					moderationQueue(id: "q1") {
						flaggedUsers {
							id
							... @defer {
								profile {
									displayName
								}
							}
						}
					}
				}`,
				"DeferredConsumer",
				&plan.DeferResponsePlan{
					Response: &resolve.GraphQLDeferResponse{
						DeferDescriptors: map[int]resolve.DeferDescriptor{
							1: {ID: 1, Path: []string{"moderationQueue", "flaggedUsers"}},
						},
						Response: &resolve.GraphQLResponse{
							Fetches: resolve.Sequence(
								resolve.Single(contextualFetch(0, nil,
									`{"method":"POST","url":"http://moderation","body":{"query":"query($a: ID!){moderationQueue(id: $a){flaggedUsers {id __typename} __context_0: includeSuspended}}","variables":{"a":$$0$$}}}`,
									contextualClientVariable("a"),
								)),
							),
							Data: &resolve.Object{
								Fields: []*resolve.Field{
									contextualField("moderationQueue", contextualQueue("moderationQueue", true,
										contextualField("flaggedUsers", contextualUsers("flaggedUsers",
											contextualField("id", &resolve.Scalar{Path: []string{"id"}}),
											&resolve.Field{
												Name:  []byte("profile"),
												Defer: &resolve.DeferField{DeferID: 1},
												Value: contextualObject("profile", true, "Profile", []string{"Profile"},
													&resolve.Field{
														Name:  []byte("displayName"),
														Defer: &resolve.DeferField{DeferID: 1},
														Value: &resolve.String{Path: []string{"displayName"}},
													},
												),
											},
										)),
									)),
								},
							},
						},
						DeferTree: resolve.DeferSingle(&resolve.DeferFetchGroup{
							DeferID: 1,
							Fetches: resolve.Sequence(
								resolve.SingleWithPath(contextualDeferredFetch(contextualEntityFetch(1, []int{0}, true,
									`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $contextualArgument_0){displayName}}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
									contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_0")),
									contextualRepresentation("User"),
								), 1), "moderationQueue.flaggedUsers", resolve.ObjectPath("moderationQueue"), resolve.ArrayPath("flaggedUsers")),
							),
						}),
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
				WithDefer(),
				WithCalculateFieldDependencies(),
			)
		})

		t.Run("client defer on the setter field does not delay the context selection", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsDefinition, `
				query DeferredSetterField {
					moderationQueue(id: "q1") {
						... @defer {
							includeSuspended
						}
						flaggedUsers {
							profile {
								displayName
							}
						}
					}
				}`,
				"DeferredSetterField",
				&plan.DeferResponsePlan{
					Response: &resolve.GraphQLDeferResponse{
						DeferDescriptors: map[int]resolve.DeferDescriptor{
							1: {ID: 1, Path: []string{"moderationQueue"}},
						},
						Response: &resolve.GraphQLResponse{
							Fetches: resolve.Sequence(
								resolve.Single(contextualFetch(1, nil,
									`{"method":"POST","url":"http://moderation","body":{"query":"query($a: ID!){moderationQueue(id: $a){flaggedUsers {__typename id} __context_0: includeSuspended}}","variables":{"a":$$0$$}}}`,
									contextualClientVariable("a"),
								)),
								resolve.SingleWithPath(contextualEntityFetch(2, []int{1}, true,
									`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $contextualArgument_0){displayName}}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
									contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_0")),
									contextualRepresentation("User"),
								), "moderationQueue.flaggedUsers", resolve.ObjectPath("moderationQueue"), resolve.ArrayPath("flaggedUsers")),
							),
							Data: &resolve.Object{
								Fields: []*resolve.Field{
									contextualField("moderationQueue", contextualQueue("moderationQueue", true,
										&resolve.Field{
											Name:  []byte("includeSuspended"),
											Defer: &resolve.DeferField{DeferID: 1},
											Value: &resolve.Boolean{Path: []string{"includeSuspended"}},
										},
										contextualField("flaggedUsers", contextualUsers("flaggedUsers", contextualProfile())),
									)),
								},
							},
						},
						DeferTree: resolve.DeferSingle(&resolve.DeferFetchGroup{
							DeferID: 1,
							Fetches: resolve.Sequence(
								resolve.Single(contextualDeferredFetch(contextualFetch(0, nil,
									`{"method":"POST","url":"http://moderation","body":{"query":"query($a: ID!){moderationQueue(id: $a){includeSuspended}}","variables":{"a":$$0$$}}}`,
									contextualClientVariable("a"),
								), 1)),
							),
						}),
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
				WithDefer(),
				WithCalculateFieldDependencies(),
			)
		})

		t.Run("client field aliased like a context selection in a deferred fragment is not reused", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsDefinition, `
				query DeferredClientAlias {
					moderationQueue(id: "q1") {
						... @defer {
							__context_0: includeSuspended
						}
						flaggedUsers {
							profile {
								displayName
							}
						}
					}
				}`,
				"DeferredClientAlias",
				&plan.DeferResponsePlan{
					Response: &resolve.GraphQLDeferResponse{
						DeferDescriptors: map[int]resolve.DeferDescriptor{
							1: {ID: 1, Path: []string{"moderationQueue"}},
						},
						Response: &resolve.GraphQLResponse{
							Fetches: resolve.Sequence(
								resolve.Single(contextualFetch(1, nil,
									`{"method":"POST","url":"http://moderation","body":{"query":"query($a: ID!){moderationQueue(id: $a){flaggedUsers {__typename id} __context_0_1: includeSuspended}}","variables":{"a":$$0$$}}}`,
									contextualClientVariable("a"),
								)),
								resolve.SingleWithPath(contextualEntityFetch(2, []int{1}, true,
									`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $contextualArgument_0){displayName}}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
									contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_0_1")),
									contextualRepresentation("User"),
								), "moderationQueue.flaggedUsers", resolve.ObjectPath("moderationQueue"), resolve.ArrayPath("flaggedUsers")),
							),
							Data: &resolve.Object{
								Fields: []*resolve.Field{
									contextualField("moderationQueue", contextualQueue("moderationQueue", true,
										&resolve.Field{
											Name:  []byte("__context_0"),
											Defer: &resolve.DeferField{DeferID: 1},
											Value: &resolve.Boolean{Path: []string{"__context_0"}},
										},
										contextualField("flaggedUsers", contextualUsers("flaggedUsers", contextualProfile())),
									)),
								},
							},
						},
						DeferTree: resolve.DeferSingle(&resolve.DeferFetchGroup{
							DeferID: 1,
							Fetches: resolve.Sequence(
								resolve.Single(contextualDeferredFetch(contextualFetch(0, nil,
									`{"method":"POST","url":"http://moderation","body":{"query":"query($a: ID!){moderationQueue(id: $a){__context_0: includeSuspended}}","variables":{"a":$$0$$}}}`,
									contextualClientVariable("a"),
								), 1)),
							),
						}),
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
				WithDefer(),
				WithCalculateFieldDependencies(),
			)
		})

		t.Run("injection under a deferred owner is fetched in the owner's defer scope", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsDefinition, `
				query DeferredOwner {
					... @defer {
						moderationQueue(id: "q1") {
							flaggedUsers {
								profile {
									displayName
								}
							}
						}
					}
				}`,
				"DeferredOwner",
				&plan.DeferResponsePlan{
					Response: &resolve.GraphQLDeferResponse{
						DeferDescriptors: map[int]resolve.DeferDescriptor{
							1: {ID: 1, Path: []string{}},
						},
						Response: &resolve.GraphQLResponse{
							Fetches: resolve.Sequence(),
							Data: &resolve.Object{
								Fields: []*resolve.Field{
									{
										Name:  []byte("moderationQueue"),
										Defer: &resolve.DeferField{DeferID: 1},
										Value: contextualQueue("moderationQueue", true,
											&resolve.Field{
												Name:  []byte("flaggedUsers"),
												Defer: &resolve.DeferField{DeferID: 1},
												Value: contextualUsers("flaggedUsers",
													&resolve.Field{
														Name:  []byte("profile"),
														Defer: &resolve.DeferField{DeferID: 1},
														Value: contextualObject("profile", true, "Profile", []string{"Profile"},
															&resolve.Field{
																Name:  []byte("displayName"),
																Defer: &resolve.DeferField{DeferID: 1},
																Value: &resolve.String{Path: []string{"displayName"}},
															},
														),
													},
												),
											},
										),
									},
								},
							},
						},
						DeferTree: resolve.DeferSingle(&resolve.DeferFetchGroup{
							DeferID: 1,
							Fetches: resolve.Sequence(
								resolve.Single(contextualDeferredFetch(contextualFetch(0, nil,
									`{"method":"POST","url":"http://moderation","body":{"query":"query($a: ID!){moderationQueue(id: $a){flaggedUsers {__typename id} __context_0: includeSuspended}}","variables":{"a":$$0$$}}}`,
									contextualClientVariable("a"),
								), 1)),
								resolve.SingleWithPath(contextualDeferredFetch(contextualEntityFetch(1, []int{0}, true,
									`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $contextualArgument_0){displayName}}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
									contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_0")),
									contextualRepresentation("User"),
								), 1), "moderationQueue.flaggedUsers", resolve.ObjectPath("moderationQueue"), resolve.ArrayPath("flaggedUsers")),
							),
						}),
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
				WithDefer(),
				WithCalculateFieldDependencies(),
			)
		})
	})

	t.Run("flattening setter subgraph", func(t *testing.T) {
		planConfiguration := contextualArgumentsConfigurationWithModerationFactory(t, contextualArgumentsFlatteningFactory{&Factory[Configuration]{}})

		literalNullProfileFetch := func(fetchID int, path ...resolve.FetchItemPathElement) *resolve.FetchTreeNode {
			return resolve.SingleWithPath(contextualEntityFetch(fetchID, []int{0}, true,
				`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $a: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $a){displayName}}}}","variables":{"a":null,"representations":[$$0$$]}}}`,
				contextualRepresentation("User"),
			), contextualResponsePath(path), path...)
		}

		t.Run("abstract level rewritten after the injection reuses the copied context selection", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsDefinition, `
				query FlattenedLevel {
					queues {
						flaggedUsers {
							profile {
								displayName
							}
						}
					}
				}`,
				"FlattenedLevel",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://moderation","body":{"query":"{queues {__typename ... on AppealQueue {flaggedUsers {__typename id} __typename} ... on ModerationQueue {flaggedUsers {__typename id} __typename __context_0: includeSuspended}}}"}}`,
							)),
							literalNullProfileFetch(1, resolve.ArrayPath("queues"), resolve.PathElementWithTypeNames(resolve.ArrayPath("flaggedUsers"), []string{"AppealQueue"})),
							resolve.SingleWithPath(contextualEntityFetch(2, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $contextualArgument_0){displayName}}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_0")),
								contextualRepresentation("User"),
							), "queues.@.flaggedUsers", resolve.ArrayPath("queues"), resolve.PathElementWithTypeNames(resolve.ArrayPath("flaggedUsers"), []string{"ModerationQueue"})),
						),
						Data: &resolve.Object{
							Fields: []*resolve.Field{
								contextualField("queues", contextualList("queues", contextualObject("", false, "Queue", []string{"AppealQueue", "ModerationQueue"},
									contextualField("flaggedUsers", contextualUsers("flaggedUsers", contextualProfile()), "AppealQueue"),
									contextualField("flaggedUsers", contextualUsers("flaggedUsers", contextualProfile()), "ModerationQueue"),
								))),
							},
						},
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})

		t.Run("ancestor rewritten before and after the injection", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsDefinition, `
				query RewrittenAncestor {
					queues {
						... on ModerationQueue {
							flaggedUsers {
								profile {
									displayName
								}
							}
						}
					}
				}`,
				"RewrittenAncestor",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://moderation","body":{"query":"{queues {__typename ... on ModerationQueue {flaggedUsers {__typename id} __context_0: includeSuspended}}}"}}`,
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $contextualArgument_0){displayName}}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_0")),
								contextualRepresentation("User"),
							), "queues.@.flaggedUsers", resolve.ArrayPath("queues"), resolve.PathElementWithTypeNames(resolve.ArrayPath("flaggedUsers"), []string{"ModerationQueue"})),
						),
						Data: &resolve.Object{
							Fields: []*resolve.Field{
								contextualField("queues", contextualList("queues", contextualObject("", false, "Queue", []string{"AppealQueue", "ModerationQueue"},
									contextualField("flaggedUsers", contextualUsers("flaggedUsers", contextualProfile()), "ModerationQueue"),
								))),
							},
						},
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})

		t.Run("branch without a setter falls back to the outer static level", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsDefinition, `
				query FlattenedNestedLevels {
					organization(id: "o1") {
						allQueues {
							flaggedUsers {
								profile {
									displayName
								}
							}
						}
					}
				}`,
				"FlattenedNestedLevels",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://moderation","body":{"query":"query($a: ID!){organization(id: $a){allQueues {__typename ... on AppealQueue {flaggedUsers {__typename id} __typename} ... on ModerationQueue {flaggedUsers {__typename id} __typename __context_0: includeSuspended}} __context_1: includeSuspended}}","variables":{"a":$$0$$}}}`,
								contextualClientVariable("a"),
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $contextualArgument_0){displayName}}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(1, "Organization", "__context_1")),
								contextualRepresentation("User"),
							), "organization.allQueues.@.flaggedUsers", resolve.ObjectPath("organization"), resolve.ArrayPath("allQueues"), resolve.PathElementWithTypeNames(resolve.ArrayPath("flaggedUsers"), []string{"AppealQueue"})),
							resolve.SingleWithPath(contextualEntityFetch(2, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename profile(includeSuspended: $contextualArgument_0){displayName}}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(2, "ModerationQueue", "__context_0")),
								contextualRepresentation("User"),
							), "organization.allQueues.@.flaggedUsers", resolve.ObjectPath("organization"), resolve.ArrayPath("allQueues"), resolve.PathElementWithTypeNames(resolve.ArrayPath("flaggedUsers"), []string{"ModerationQueue"})),
						),
						Data: &resolve.Object{
							Fields: []*resolve.Field{
								contextualField("organization", contextualObject("organization", true, "Organization", []string{"Organization"},
									contextualField("allQueues", contextualList("allQueues", contextualObject("", false, "Queue", []string{"AppealQueue", "ModerationQueue"},
										contextualField("flaggedUsers", contextualUsers("flaggedUsers", contextualProfile()), "AppealQueue"),
										contextualField("flaggedUsers", contextualUsers("flaggedUsers", contextualProfile()), "ModerationQueue"),
									))),
								)),
							},
						},
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})
	})

	t.Run("nested selections under flattened levels", func(t *testing.T) {
		planConfiguration := contextualArgumentsNestedSelectionConfiguration(t)

		users := func(fieldName string) *resolve.Array {
			return contextualUsers("flaggedUsers", contextualField(fieldName, &resolve.String{Path: []string{fieldName}, Nullable: true}))
		}
		queue := func(fields ...*resolve.Field) *resolve.Object {
			return contextualObject("", false, "Queue", []string{"AppealQueue", "ModerationQueue"}, fields...)
		}
		usersOfBothTypes := func(fieldName string) []*resolve.Field {
			return []*resolve.Field{
				contextualField("flaggedUsers", users(fieldName), "AppealQueue"),
				contextualField("flaggedUsers", users(fieldName), "ModerationQueue"),
			}
		}

		t.Run("typename leaf of a nested selection is reused after the rewrite", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsNestedSelectionDefinition, `
				query TeamType {
					queues {
						flaggedUsers {
							teamTypeView
						}
					}
				}`,
				"TeamType",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://moderation","body":{"query":"{queues {__typename ... on AppealQueue {flaggedUsers {__typename id} __typename} ... on ModerationQueue {flaggedUsers {__typename id} __typename __context_0: team {__typename}}}}"}}`,
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $a: String){_entities(representations: $representations){... on User {__typename teamTypeView(typeName: $a)}}}","variables":{"a":null,"representations":[$$0$$]}}}`,
								contextualRepresentation("User"),
							), "queues.@.flaggedUsers", resolve.ArrayPath("queues"), resolve.PathElementWithTypeNames(resolve.ArrayPath("flaggedUsers"), []string{"AppealQueue"})),
							resolve.SingleWithPath(contextualEntityFetch(2, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: String){_entities(representations: $representations){... on User {__typename teamTypeView(typeName: $contextualArgument_0)}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_0", "__typename")),
								contextualRepresentation("User"),
							), "queues.@.flaggedUsers", resolve.ArrayPath("queues"), resolve.PathElementWithTypeNames(resolve.ArrayPath("flaggedUsers"), []string{"ModerationQueue"})),
						),
						Data: &resolve.Object{
							Fields: []*resolve.Field{
								contextualField("queues", contextualList("queues", queue(usersOfBothTypes("teamTypeView")...))),
							},
						},
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})

		t.Run("nested field owned by another subgraph is reused under two flattened levels", func(t *testing.T) {
			teamNameFetch := func(fetchID int, path ...resolve.FetchItemPathElement) *resolve.FetchTreeNode {
				return resolve.SingleWithPath(contextualEntityFetch(fetchID, []int{0}, true,
					`{"method":"POST","url":"http://public","body":{"query":"query($representations: [_Any!]!){_entities(representations: $representations){... on Team {__typename name}}}","variables":{"representations":[$$0$$]}}}`,
					contextualRepresentation("Team"),
				), contextualResponsePath(path), path...)
			}

			RunWithPermutations(t, contextualArgumentsNestedSelectionDefinition, `
				query TeamName {
					queues {
						subqueues {
							flaggedUsers {
								teamView
							}
						}
					}
				}`,
				"TeamName",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Fetches: resolve.Sequence(
							resolve.Single(contextualFetch(0, nil,
								`{"method":"POST","url":"http://moderation","body":{"query":"{queues {__typename ... on AppealQueue {subqueues {__typename ... on AppealQueue {flaggedUsers {__typename id} __typename} ... on ModerationQueue {flaggedUsers {__typename id} __typename __context_0: team {__typename id}}} __typename} ... on ModerationQueue {subqueues {__typename ... on AppealQueue {flaggedUsers {__typename id} __typename} ... on ModerationQueue {flaggedUsers {__typename id} __typename __context_0: team {__typename id}}} __typename __context_1: team {__typename id}}}}"}}`,
							)),
							resolve.SingleWithPath(contextualEntityFetch(1, []int{0}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $a: String){_entities(representations: $representations){... on User {__typename teamView(teamName: $a)}}}","variables":{"a":null,"representations":[$$0$$]}}}`,
								contextualRepresentation("User"),
							), "queues.@.subqueues.@.flaggedUsers", resolve.ArrayPath("queues"), resolve.PathElementWithTypeNames(resolve.ArrayPath("subqueues"), []string{"AppealQueue"}), resolve.PathElementWithTypeNames(resolve.ArrayPath("flaggedUsers"), []string{"AppealQueue"})),
							teamNameFetch(2, resolve.ArrayPath("queues"), resolve.ArrayPath("subqueues"), resolve.PathElementWithTypeNames(resolve.ObjectPath("__context_0"), []string{"ModerationQueue"})),
							teamNameFetch(4, resolve.ArrayPath("queues"), resolve.PathElementWithTypeNames(resolve.ObjectPath("__context_1"), []string{"ModerationQueue"})),
							resolve.SingleWithPath(contextualEntityFetch(5, []int{0, 2}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: String){_entities(representations: $representations){... on User {__typename teamView(teamName: $contextualArgument_0)}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(2, "ModerationQueue", "__context_0", "name")),
								contextualRepresentation("User"),
							), "queues.@.subqueues.@.flaggedUsers", resolve.ArrayPath("queues"), resolve.PathElementWithTypeNames(resolve.ArrayPath("subqueues"), []string{"AppealQueue", "ModerationQueue"}), resolve.PathElementWithTypeNames(resolve.ArrayPath("flaggedUsers"), []string{"ModerationQueue"})),
							resolve.SingleWithPath(contextualEntityFetch(6, []int{0, 4}, true,
								`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: String){_entities(representations: $representations){... on User {__typename teamView(teamName: $contextualArgument_0)}}}","variables":{"representations":[$$1$$],"contextualArgument_0":$$0$$}}}`,
								contextualVariable("contextualArgument_0", contextualStaticLevel(1, "ModerationQueue", "__context_1", "name")),
								contextualRepresentation("User"),
							), "queues.@.subqueues.@.flaggedUsers", resolve.ArrayPath("queues"), resolve.PathElementWithTypeNames(resolve.ArrayPath("subqueues"), []string{"ModerationQueue"}), resolve.PathElementWithTypeNames(resolve.ArrayPath("flaggedUsers"), []string{"AppealQueue"})),
						),
						Data: &resolve.Object{
							Fields: []*resolve.Field{
								contextualField("queues", contextualList("queues", queue(
									contextualField("subqueues", contextualList("subqueues", queue(usersOfBothTypes("teamView")...)), "AppealQueue"),
									contextualField("subqueues", contextualList("subqueues", queue(usersOfBothTypes("teamView")...)), "ModerationQueue"),
								))),
							},
						},
					},
				},
				planConfiguration,
				WithDefaultPostProcessor(),
			)
		})
	})

	t.Run("multi fetch", func(t *testing.T) {
		planConfiguration := contextualArgumentsConfiguration(t)
		planConfiguration.EnableMultiFetch = true

		fieldInfo := func(name, parentTypeName, namedType, dataSourceName string) *resolve.FieldInfo {
			return &resolve.FieldInfo{
				Name:                name,
				ExactParentTypeName: parentTypeName,
				ParentTypeNames:     []string{parentTypeName},
				NamedType:           namedType,
				Source:              resolve.TypeFieldSource{IDs: []string{dataSourceName}, Names: []string{dataSourceName}},
			}
		}

		contextualBatchFetch := func(fetchID int, queueTypeName, alias string) *resolve.FetchTreeNode {
			return &resolve.FetchTreeNode{
				Kind: resolve.FetchTreeNodeKindSingle,
				Item: &resolve.FetchItem{
					Fetch: &resolve.BatchEntityFetch{
						FetchDependencies: resolve.FetchDependencies{FetchID: fetchID, DependsOnFetchIDs: []int{0}},
						Input: resolve.BatchInput{
							Header: resolve.InputTemplate{
								Segments: []resolve.TemplateSegment{
									{
										SegmentType: resolve.StaticSegmentType,
										Data:        []byte(`{"method":"POST","url":"http://users","body":{"query":"query($representations: [_Any!]!, $contextualArgument_0: Boolean){_entities(representations: $representations){... on User {__typename scopeView(show: $contextualArgument_0)}}}","variables":{"representations":[`),
									},
								},
								SetTemplateOutputToNullOnVariableNull: true,
							},
							Items: []resolve.InputTemplate{
								{
									Segments: []resolve.TemplateSegment{
										{
											SegmentType:  resolve.VariableSegmentType,
											VariableKind: resolve.ResolvableObjectVariableKind,
											Renderer:     contextualRepresentation("User").Renderer,
										},
									},
									SetTemplateOutputToNullOnVariableNull: true,
								},
							},
							SkipNullItems:        true,
							SkipEmptyObjectItems: true,
							SkipErrItems:         true,
							Separator: resolve.InputTemplate{
								Segments: []resolve.TemplateSegment{
									{SegmentType: resolve.StaticSegmentType, Data: []byte(`,`)},
								},
							},
							Footer: resolve.InputTemplate{
								Segments: []resolve.TemplateSegment{
									{SegmentType: resolve.StaticSegmentType, Data: []byte(`],"contextualArgument_0":`)},
									{
										SegmentType:        resolve.VariableSegmentType,
										VariableKind:       resolve.ContextualArgumentVariableKind,
										VariableSourcePath: []string{"contextualArgument_0"},
									},
									{SegmentType: resolve.StaticSegmentType, Data: []byte(`}}}`)},
								},
								SetTemplateOutputToNullOnVariableNull: true,
							},
						},
						DataSource:     &Source{},
						PostProcessing: EntitiesPostProcessingConfiguration,
						Info: &resolve.FetchInfo{
							DataSourceID:   "users",
							DataSourceName: "users",
							RootFields:     []resolve.GraphCoordinate{{TypeName: "User", FieldName: "scopeView"}},
							OperationType:  ast.OperationTypeQuery,
						},
						ContextualArguments: []resolve.ContextualArgumentCapture{
							{VariableName: "contextualArgument_0", Levels: []resolve.ContextualArgumentLevel{contextualStaticLevel(1, queueTypeName, alias)}},
						},
					},
					FetchPath:            []resolve.FetchItemPathElement{resolve.ArrayPath("scopes"), resolve.PathElementWithTypeNames(resolve.ArrayPath("flaggedUsers"), []string{queueTypeName})},
					ResponsePath:         "scopes.@.flaggedUsers",
					ResponsePathElements: []string{"scopes", "@", "flaggedUsers"},
				},
			}
		}

		flaggedUsers := func(queueTypeName string) *resolve.Field {
			users := contextualUsers("flaggedUsers", &resolve.Field{
				Name:              []byte("scopeView"),
				Value:             &resolve.String{Path: []string{"scopeView"}, Nullable: true},
				ParentOnTypeNames: []resolve.ParentOnTypeNames{{Depth: 1, Names: [][]byte{[]byte(queueTypeName)}}},
				Info:              fieldInfo("scopeView", "User", "String", "users"),
			})
			users.Item.(*resolve.Object).SourceName = "moderation"
			return &resolve.Field{
				Name:        []byte("flaggedUsers"),
				Value:       users,
				OnTypeNames: [][]byte{[]byte(queueTypeName)},
				Info:        fieldInfo("flaggedUsers", queueTypeName, "User", "moderation"),
			}
		}

		scopes := contextualObject("", false, "Scope", []string{"AppealQueue", "ModerationQueue", "Team"},
			flaggedUsers("ModerationQueue"),
			flaggedUsers("AppealQueue"),
		)
		scopes.SourceName = "moderation"

		t.Run("contextual fetches of one wave are neither merged nor deduplicated", func(t *testing.T) {
			RunWithPermutations(t, contextualArgumentsDefinition, `
				query MultiFetchExclusion {
					scopes {
						... on ModerationQueue {
							flaggedUsers {
								scopeView
							}
						}
						... on AppealQueue {
							flaggedUsers {
								scopeView
							}
						}
					}
				}`,
				"MultiFetchExclusion",
				&plan.SynchronousResponsePlan{
					Response: &resolve.GraphQLResponse{
						Info: &resolve.GraphQLResponseInfo{OperationType: ast.OperationTypeQuery},
						Fetches: resolve.Sequence(
							&resolve.FetchTreeNode{
								Kind: resolve.FetchTreeNodeKindSingle,
								Item: &resolve.FetchItem{
									Fetch: &resolve.SingleFetch{
										FetchConfiguration: resolve.FetchConfiguration{
											DataSource:     &Source{},
											PostProcessing: DefaultPostProcessingConfiguration,
										},
										InputTemplate: resolve.InputTemplate{
											Segments: []resolve.TemplateSegment{
												{
													SegmentType: resolve.StaticSegmentType,
													Data:        []byte(`{"method":"POST","url":"http://moderation","body":{"query":"{scopes {__typename ... on ModerationQueue {flaggedUsers {__typename id} __context_0: includeSuspended} ... on AppealQueue {flaggedUsers {__typename id} __context_1: showSuspended}}}"}}`),
												},
											},
										},
										DataSourceIdentifier: []byte("graphql_datasource.Source"),
										Info: &resolve.FetchInfo{
											DataSourceID:   "moderation",
											DataSourceName: "moderation",
											RootFields:     []resolve.GraphCoordinate{{TypeName: "Query", FieldName: "scopes"}},
											OperationType:  ast.OperationTypeQuery,
										},
									},
								},
							},
							resolve.Parallel(
								contextualBatchFetch(1, "ModerationQueue", "__context_0"),
								contextualBatchFetch(2, "AppealQueue", "__context_1"),
							),
						),
						Data: &resolve.Object{
							Fields: []*resolve.Field{
								{
									Name:  []byte("scopes"),
									Value: contextualList("scopes", scopes),
									Info:  fieldInfo("scopes", "Query", "Scope", "moderation"),
								},
							},
						},
					},
				},
				planConfiguration,
				WithFieldInfo(),
				WithPostProcessor(postprocess.EnableMultiFetch()),
			)
		})
	})
}

// contextualArgumentsPlanDroppingFactory registers its planners without the contextual argument plans of the path builder.
type contextualArgumentsPlanDroppingFactory struct {
	*Factory[Configuration]
}

func (f contextualArgumentsPlanDroppingFactory) Planner(logger abstractlogger.Logger) plan.DataSourcePlanner[Configuration] {
	return contextualArgumentsPlanDroppingPlanner{f.Factory.Planner(logger).(*Planner[Configuration])}
}

type contextualArgumentsPlanDroppingPlanner struct {
	*Planner[Configuration]
}

func (p contextualArgumentsPlanDroppingPlanner) Register(visitor *plan.Visitor, configuration plan.DataSourceConfiguration[Configuration], dataSourcePlannerConfiguration plan.DataSourcePlannerConfiguration) error {
	dataSourcePlannerConfiguration.ContextualArguments = nil
	return p.Planner.Register(visitor, configuration, dataSourcePlannerConfiguration)
}

// contextualArgumentsGuardConfiguration builds the same subgraph setter with the given planner factory, federation
// setting and upstream profile field, to reach planning states that a composed configuration rules out.
func contextualArgumentsGuardConfiguration(t *testing.T, factory plan.PlannerFactory[Configuration], federationEnabled bool, upstreamProfileField string) plan.Configuration {
	usersSDL := `
		type Query {
			moderationQueue(id: ID!): ModerationQueue
		}

		type ModerationQueue @key(fields: "id") @context(name: "userScope") {
			id: ID!
			includeSuspended: Boolean!
			flaggedUsers: [User!]!
		}

		type User @key(fields: "id") {
			id: ID!
			` + upstreamProfileField + `
		}

		type Profile {
			displayName: String!
		}
	`

	users, err := plan.NewDataSourceConfiguration[Configuration]("users", factory,
		&plan.DataSourceMetadata{
			RootNodes: []plan.TypeField{
				{TypeName: "Query", FieldNames: []string{"moderationQueue"}},
				{TypeName: "ModerationQueue", FieldNames: []string{"id", "includeSuspended", "flaggedUsers"}},
				{TypeName: "User", FieldNames: []string{"id", "profile"}},
			},
			ChildNodes: []plan.TypeField{
				{TypeName: "Profile", FieldNames: []string{"displayName"}},
			},
			FederationMetaData: plan.FederationMetaData{
				Keys: plan.FederationFieldConfigurations{
					{TypeName: "ModerationQueue", SelectionSet: "id"},
					{TypeName: "User", SelectionSet: "id"},
				},
				ContextualArguments: plan.ContextualArgumentConfigurations{
					{TypeName: "User", FieldName: "profile", ArgumentName: "includeSuspended", ContextName: "userScope", Setters: contextualArgumentsSetters("ModerationQueue")},
				},
			},
		},
		mustCustomConfiguration(t, ConfigurationInput{
			Fetch:               &FetchConfiguration{URL: "http://users"},
			SchemaConfiguration: mustSchema(t, &FederationConfiguration{Enabled: federationEnabled, ServiceSDL: usersSDL}, usersSDL),
		}),
	)
	require.NoError(t, err)

	return plan.Configuration{
		DataSources:                  []plan.DataSource{users},
		DisableResolveFieldPositions: true,
		Fields: plan.FieldConfigurations{
			{TypeName: "Query", FieldName: "moderationQueue", Arguments: plan.ArgumentsConfigurations{{Name: "id", SourceType: plan.FieldArgumentSource}}},
			{TypeName: "User", FieldName: "profile", Arguments: plan.ArgumentsConfigurations{{Name: "includeSuspended", SourceType: plan.ContextualArgumentSource}}},
		},
	}
}

func contextualArgumentsPlanningError(t *testing.T, configuration plan.Configuration) string {
	t.Helper()

	definition := unsafeparser.ParseGraphqlDocumentString(contextualArgumentsSameSubgraphDefinition)
	require.NoError(t, asttransform.MergeDefinitionWithBaseSchema(&definition))
	operation := unsafeparser.ParseGraphqlDocumentString(`query Guard { moderationQueue(id: "q1") { flaggedUsers { profile { displayName } } } }`)

	report := &operationreport.Report{}
	astnormalization.NewWithOpts(
		astnormalization.WithExtractVariables(),
		astnormalization.WithInlineFragmentSpreads(),
		astnormalization.WithRemoveFragmentDefinitions(),
		astnormalization.WithRemoveUnusedVariables(),
	).NormalizeOperation(&operation, &definition, report)
	astvalidation.DefaultOperationValidator().Validate(&operation, &definition, report)
	require.False(t, report.HasErrors(), report.Error())

	planner, err := plan.NewPlanner(configuration)
	require.NoError(t, err)
	planner.Plan(&operation, &definition, "Guard", report)
	require.True(t, report.HasErrors())
	return report.Error()
}

func TestGraphQLDataSourceFederation_ContextualArgumentsPlanningGuards(t *testing.T) {
	const profileField = `profile(includeSuspended: Boolean @fromContext(field: "$userScope { includeSuspended }")): Profile`

	t.Run("a contextual argument without a plan stops planning", func(t *testing.T) {
		configuration := contextualArgumentsGuardConfiguration(t, contextualArgumentsPlanDroppingFactory{&Factory[Configuration]{}}, true, profileField)
		assert.Contains(t, contextualArgumentsPlanningError(t, configuration), "the contextual argument User.profile(includeSuspended:) was not planned")
	})

	t.Run("a captured argument outside an entity fetch stops planning", func(t *testing.T) {
		configuration := contextualArgumentsGuardConfiguration(t, &Factory[Configuration]{}, false, profileField)
		assert.Contains(t, contextualArgumentsPlanningError(t, configuration), "the contextual argument User.profile(includeSuspended:) is not planned in an entity fetch")
	})

	t.Run("an argument missing from the upstream schema stops planning", func(t *testing.T) {
		configuration := contextualArgumentsGuardConfiguration(t, &Factory[Configuration]{}, true, `profile: Profile`)
		assert.Contains(t, contextualArgumentsPlanningError(t, configuration), "the argument User.profile(includeSuspended:) is not defined in the upstream schema")
	})
}

func contextualArgumentsBenchmarkConfiguration(b *testing.B, contextualFields int) (definition string, configuration plan.Configuration) {
	var userFields, upstreamUserFields strings.Builder
	for i := range 10 {
		fmt.Fprintf(&userFields, " f%d: String", i)
		fmt.Fprintf(&upstreamUserFields, " f%d(includeSuspended: Boolean): String", i)
	}

	definition = `
		type Query { moderationQueue(id: ID!): ModerationQueue }
		type ModerationQueue { id: ID! includeSuspended: Boolean! flaggedUsers: [User!]! }
		type User { id: ID!` + userFields.String() + ` }
	`

	moderationSDL := `
		type Query { moderationQueue(id: ID!): ModerationQueue }
		type ModerationQueue @key(fields: "id") { id: ID! includeSuspended: Boolean! flaggedUsers: [User!]! }
		type User @key(fields: "id", resolvable: false) { id: ID! }
	`
	usersSDL := `
		type ModerationQueue @key(fields: "id") { id: ID! includeSuspended: Boolean! @external }
		type User @key(fields: "id") { id: ID!` + upstreamUserFields.String() + ` }
	`

	userFieldNames := []string{"id"}
	fields := plan.FieldConfigurations{
		{TypeName: "Query", FieldName: "moderationQueue", Arguments: plan.ArgumentsConfigurations{{Name: "id", SourceType: plan.FieldArgumentSource}}},
	}
	var contextualArguments plan.ContextualArgumentConfigurations
	for i := range 10 {
		fieldName := fmt.Sprintf("f%d", i)
		userFieldNames = append(userFieldNames, fieldName)
		if i >= contextualFields {
			continue
		}
		contextualArguments = append(contextualArguments, plan.ContextualArgumentConfiguration{
			TypeName:     "User",
			FieldName:    fieldName,
			ArgumentName: "includeSuspended",
			ContextName:  "userScope",
			Setters:      contextualArgumentsSetters("ModerationQueue"),
		})
		fields = append(fields, plan.FieldConfiguration{
			TypeName:  "User",
			FieldName: fieldName,
			Arguments: plan.ArgumentsConfigurations{{Name: "includeSuspended", SourceType: plan.ContextualArgumentSource}},
		})
	}

	dataSource := func(id, url, sdl string, metadata *plan.DataSourceMetadata) plan.DataSource {
		schemaConfiguration, err := NewSchemaConfiguration(sdl, &FederationConfiguration{Enabled: true, ServiceSDL: sdl})
		require.NoError(b, err)
		customConfiguration, err := NewConfiguration(ConfigurationInput{
			Fetch:               &FetchConfiguration{URL: url},
			SchemaConfiguration: schemaConfiguration,
		})
		require.NoError(b, err)
		dataSourceConfiguration, err := plan.NewDataSourceConfiguration[Configuration](id, &Factory[Configuration]{}, metadata, customConfiguration)
		require.NoError(b, err)
		return dataSourceConfiguration
	}

	moderation := dataSource("moderation", "http://moderation", moderationSDL,
		&plan.DataSourceMetadata{
			RootNodes: []plan.TypeField{
				{TypeName: "Query", FieldNames: []string{"moderationQueue"}},
				{TypeName: "ModerationQueue", FieldNames: []string{"id", "includeSuspended", "flaggedUsers"}},
				{TypeName: "User", FieldNames: []string{"id"}},
			},
			FederationMetaData: plan.FederationMetaData{
				Keys: plan.FederationFieldConfigurations{
					{TypeName: "ModerationQueue", SelectionSet: "id"},
					{TypeName: "User", SelectionSet: "id", DisableEntityResolver: true},
				},
			},
		},
	)

	users := dataSource("users", "http://users", usersSDL,
		&plan.DataSourceMetadata{
			RootNodes: []plan.TypeField{
				{TypeName: "ModerationQueue", FieldNames: []string{"id"}, ExternalFieldNames: []string{"includeSuspended"}},
				{TypeName: "User", FieldNames: userFieldNames},
			},
			FederationMetaData: plan.FederationMetaData{
				Keys: plan.FederationFieldConfigurations{
					{TypeName: "ModerationQueue", SelectionSet: "id"},
					{TypeName: "User", SelectionSet: "id"},
				},
				ContextualArguments: contextualArguments,
			},
		},
	)

	return definition, plan.Configuration{
		DataSources:                  []plan.DataSource{moderation, users},
		DisableResolveFieldPositions: true,
		Fields:                       fields,
	}
}

func BenchmarkPlannerContextualArguments(b *testing.B) {
	operation := `query Bench { moderationQueue(id: "q1") { flaggedUsers { id f0 f1 f2 f3 f4 f5 f6 f7 f8 f9 } } }`

	for _, contextualFields := range []int{0, 1, 10} {
		b.Run(fmt.Sprintf("fields=%d", contextualFields), func(b *testing.B) {
			definition, planConfiguration := contextualArgumentsBenchmarkConfiguration(b, contextualFields)

			def := unsafeparser.ParseGraphqlDocumentString(definition)
			if err := asttransform.MergeDefinitionWithBaseSchema(&def); err != nil {
				b.Fatal(err)
			}

			norm := astnormalization.NewWithOpts(astnormalization.WithExtractVariables(), astnormalization.WithInlineFragmentSpreads(), astnormalization.WithRemoveFragmentDefinitions(), astnormalization.WithRemoveUnusedVariables())
			var report operationreport.Report

			b.ReportAllocs()
			for b.Loop() {
				// planning adds fields to the operation and leaves field dependency state on the planner,
				// so every iteration plans a fresh operation with a fresh planner
				b.StopTimer()
				op := unsafeparser.ParseGraphqlDocumentString(operation)
				norm.NormalizeOperation(&op, &def, &report)
				p, err := plan.NewPlanner(planConfiguration)
				if err != nil {
					b.Fatal(err)
				}
				b.StartTimer()

				_ = p.Plan(&op, &def, "Bench", &report)
				if report.HasErrors() {
					b.Fatal(report.Error())
				}
			}
		})
	}
}
