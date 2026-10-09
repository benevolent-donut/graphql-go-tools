package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/jensneuse/abstractlogger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wundergraph/graphql-go-tools/execution/graphql"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/datasource/graphql_datasource"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/plan"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

const mergeFieldsSchema = `
	type Query {
		queues: [Queue!]!
		communities: [Community!]!
	}
	interface Community {
		id: ID!
		queues: [Queue!]!
	}
	type Guild implements Community {
		id: ID!
		queues: [Queue!]!
	}
	type Channel implements Community {
		id: ID!
		queues: [Queue!]!
	}
	interface Queue {
		id: ID!
		name: String!
		flaggedUsers: [User!]!
		flagged: [Flagged!]!
		named: [Named!]!
	}
	interface Reviewable {
		id: ID!
	}
	type ModerationQueue implements Queue & Reviewable {
		id: ID!
		name: String!
		flaggedUsers: [User!]!
		flagged: [Flagged!]!
		named: [Named!]!
	}
	type AppealQueue implements Queue & Reviewable {
		id: ID!
		name: String!
		flaggedUsers: [User!]!
		flagged: [Flagged!]!
		named: [Named!]!
	}
	union Flagged = User | Bot
	interface Named {
		id: ID!
		name: String!
	}
	type User implements Named {
		id: ID!
		name: String!
		friends: [User!]!
		grid: [[User!]!]!
	}
	type Bot implements Named {
		id: ID!
		name: String!
	}
`

func TestExecutionEngine_MergeFieldsOfTypeConditions(t *testing.T) {
	testCases := []struct {
		name             string
		query            string
		body             string
		response         string
		deferred         map[string]string
		expectedResponse string
	}{
		{
			name:             "object fields of sibling type conditions next to an untyped field",
			query:            `{ queues { flaggedUsers { id } ... on ModerationQueue { flaggedUsers { friends { id } } } ... on AppealQueue { flaggedUsers { friends { id } } } } }`,
			body:             `{"query":"{queues {__typename flaggedUsers {id} ... on ModerationQueue {flaggedUsers {friends {id}}} ... on AppealQueue {flaggedUsers {friends {id}}}}}"}`,
			response:         `{"data":{"queues":[{"__typename":"ModerationQueue","flaggedUsers":[{"id":"u1","friends":[{"id":"u2"}]},{"id":"u2","friends":[{"id":"u3"}]}]},{"__typename":"AppealQueue","flaggedUsers":[{"id":"u5","friends":[{"id":"u1"}]}]}]}}`,
			expectedResponse: `{"data":{"queues":[{"flaggedUsers":[{"id":"u1","friends":[{"id":"u2"}]},{"id":"u2","friends":[{"id":"u3"}]}]},{"flaggedUsers":[{"id":"u5","friends":[{"id":"u1"}]}]}]}}`,
		},
		{
			name:             "different object and scalar fields of sibling type conditions",
			query:            `{ queues { flaggedUsers { id } ... on ModerationQueue { flaggedUsers { friends { id } name } } ... on AppealQueue { flaggedUsers { friends { name } name } } } }`,
			body:             `{"query":"{queues {__typename flaggedUsers {id} ... on ModerationQueue {flaggedUsers {friends {id} name}} ... on AppealQueue {flaggedUsers {friends {name} name}}}}"}`,
			response:         `{"data":{"queues":[{"__typename":"ModerationQueue","flaggedUsers":[{"id":"u1","friends":[{"id":"u2"}],"name":"Ada"}]},{"__typename":"AppealQueue","flaggedUsers":[{"id":"u5","friends":[{"name":"Ada"}],"name":"Eve"}]}]}}`,
			expectedResponse: `{"data":{"queues":[{"flaggedUsers":[{"id":"u1","friends":[{"id":"u2"}],"name":"Ada"}]},{"flaggedUsers":[{"id":"u5","friends":[{"name":"Ada"}],"name":"Eve"}]}]}}`,
		},
		{
			name:             "nested object fields of sibling type conditions",
			query:            `{ queues { flaggedUsers { id } ... on ModerationQueue { flaggedUsers { friends { friends { id } } } } ... on AppealQueue { flaggedUsers { friends { friends { name } } } } } }`,
			body:             `{"query":"{queues {__typename flaggedUsers {id} ... on ModerationQueue {flaggedUsers {friends {friends {id}}}} ... on AppealQueue {flaggedUsers {friends {friends {name}}}}}}"}`,
			response:         `{"data":{"queues":[{"__typename":"ModerationQueue","flaggedUsers":[{"id":"u1","friends":[{"friends":[{"id":"u3"}]}]}]},{"__typename":"AppealQueue","flaggedUsers":[{"id":"u5","friends":[{"friends":[{"name":"Bo"}]}]}]}]}}`,
			expectedResponse: `{"data":{"queues":[{"flaggedUsers":[{"id":"u1","friends":[{"friends":[{"id":"u3"}]}]}]},{"flaggedUsers":[{"id":"u5","friends":[{"friends":[{"name":"Bo"}]}]}]}]}}`,
		},
		{
			name:             "object fields of union members below sibling type conditions",
			query:            `{ queues { flagged { ... on Bot { id } } ... on ModerationQueue { flagged { ... on User { friends { id } } } } ... on AppealQueue { flagged { ... on User { friends { id } } } } } }`,
			body:             `{"query":"{queues {__typename flagged {__typename ... on Bot {id}} ... on ModerationQueue {flagged {__typename ... on User {friends {id}}}} ... on AppealQueue {flagged {__typename ... on User {friends {id}}}}}}"}`,
			response:         `{"data":{"queues":[{"__typename":"ModerationQueue","flagged":[{"__typename":"User","friends":[{"id":"u2"}]},{"__typename":"Bot","id":"b1"}]},{"__typename":"AppealQueue","flagged":[{"__typename":"User","friends":[{"id":"u1"}]},{"__typename":"Bot","id":"b2"}]}]}}`,
			expectedResponse: `{"data":{"queues":[{"flagged":[{"friends":[{"id":"u2"}]},{"id":"b1"}]},{"flagged":[{"friends":[{"id":"u1"}]},{"id":"b2"}]}]}}`,
		},
		{
			name:             "scalar fields of union members with and without a sibling type condition",
			query:            `{ queues { flagged { ... on Bot { id } ... on User { id } } ... on AppealQueue { flagged { ... on Bot { id } } } } }`,
			body:             `{"query":"{queues {__typename flagged {__typename ... on Bot {id} ... on User {id}} ... on AppealQueue {flagged {__typename ... on Bot {id}}}}}"}`,
			response:         `{"data":{"queues":[{"__typename":"ModerationQueue","flagged":[{"__typename":"User","id":"u1"},{"__typename":"Bot","id":"b1"}]},{"__typename":"AppealQueue","flagged":[{"__typename":"User","id":"u5"},{"__typename":"Bot","id":"b2"}]}]}}`,
			expectedResponse: `{"data":{"queues":[{"flagged":[{"id":"u1"},{"id":"b1"}]},{"flagged":[{"id":"u5"},{"id":"b2"}]}]}}`,
		},
		{
			name:             "object fields of type conditions at two depths",
			query:            `{ communities { queues { flaggedUsers { id } } ... on Guild { queues { ... on ModerationQueue { flaggedUsers { friends { id } } } } } ... on Channel { queues { ... on AppealQueue { flaggedUsers { friends { id } } } } } } }`,
			body:             `{"query":"{communities {__typename queues {__typename flaggedUsers {id}} ... on Guild {queues {__typename ... on ModerationQueue {flaggedUsers {friends {id}}}}} ... on Channel {queues {__typename ... on AppealQueue {flaggedUsers {friends {id}}}}}}}"}`,
			response:         `{"data":{"communities":[{"__typename":"Guild","queues":[{"__typename":"ModerationQueue","flaggedUsers":[{"id":"u1","friends":[{"id":"u2"}]}]},{"__typename":"AppealQueue","flaggedUsers":[{"id":"u5"}]}]},{"__typename":"Channel","queues":[{"__typename":"ModerationQueue","flaggedUsers":[{"id":"u2"}]},{"__typename":"AppealQueue","flaggedUsers":[{"id":"u5","friends":[{"id":"u1"}]}]}]}]}}`,
			expectedResponse: `{"data":{"communities":[{"queues":[{"flaggedUsers":[{"id":"u1","friends":[{"id":"u2"}]}]},{"flaggedUsers":[{"id":"u5"}]}]},{"queues":[{"flaggedUsers":[{"id":"u2"}]},{"flaggedUsers":[{"id":"u5","friends":[{"id":"u1"}]}]}]}]}}`,
		},
		{
			name:             "untyped fields below one type condition and typed fields below another",
			query:            `{ communities { queues { id } ... on Guild { queues { flaggedUsers { id } name } } ... on Channel { queues { ... on ModerationQueue { flaggedUsers { name } name } } } } }`,
			body:             `{"query":"{communities {__typename queues {__typename id} ... on Guild {queues {__typename flaggedUsers {id} name}} ... on Channel {queues {__typename ... on ModerationQueue {flaggedUsers {name} name}}}}}"}`,
			response:         `{"data":{"communities":[{"__typename":"Guild","queues":[{"__typename":"ModerationQueue","id":"q1","flaggedUsers":[{"id":"u1"}],"name":"Spam"},{"__typename":"AppealQueue","id":"a1","flaggedUsers":[{"id":"u5"}],"name":"Appeals"}]},{"__typename":"Channel","queues":[{"__typename":"ModerationQueue","id":"q2","flaggedUsers":[{"name":"Cy"}],"name":"Chat"},{"__typename":"AppealQueue","id":"a2"}]}]}}`,
			expectedResponse: `{"data":{"communities":[{"queues":[{"id":"q1","flaggedUsers":[{"id":"u1"}],"name":"Spam"},{"id":"a1","flaggedUsers":[{"id":"u5"}],"name":"Appeals"}]},{"queues":[{"id":"q2","flaggedUsers":[{"name":"Cy"}],"name":"Chat"},{"id":"a2"}]}]}}`,
		},
		{
			name:             "object fields whose type conditions include those of the other",
			query:            `{ communities { queues { flaggedUsers { id } ... on ModerationQueue { flaggedUsers { friends { id } } } } ... on Guild { queues { ... on ModerationQueue { flaggedUsers { friends { name } } } } } } }`,
			body:             `{"query":"{communities {__typename queues {__typename flaggedUsers {id} ... on ModerationQueue {flaggedUsers {friends {id}}}} ... on Guild {queues {__typename ... on ModerationQueue {flaggedUsers {friends {name}}}}}}}"}`,
			response:         `{"data":{"communities":[{"__typename":"Guild","queues":[{"__typename":"ModerationQueue","flaggedUsers":[{"id":"u1","friends":[{"id":"u2","name":"Bo"}]}]},{"__typename":"AppealQueue","flaggedUsers":[{"id":"u5"}]}]},{"__typename":"Channel","queues":[{"__typename":"ModerationQueue","flaggedUsers":[{"id":"u2","friends":[{"id":"u3"}]}]},{"__typename":"AppealQueue","flaggedUsers":[{"id":"u5"}]}]}]}}`,
			expectedResponse: `{"data":{"communities":[{"queues":[{"flaggedUsers":[{"id":"u1","friends":[{"id":"u2","name":"Bo"}]}]},{"flaggedUsers":[{"id":"u5"}]}]},{"queues":[{"flaggedUsers":[{"id":"u2","friends":[{"id":"u3"}]}]},{"flaggedUsers":[{"id":"u5"}]}]}]}}`,
		},
		{
			name:             "scalar fields of an interface fragment next to a fragment of an implementing type",
			query:            `{ communities { queues { name } ... on Guild { queues { ... on Reviewable { id } ... on AppealQueue { id } } } } }`,
			body:             `{"query":"{communities {__typename queues {__typename name} ... on Guild {queues {__typename ... on Reviewable {__typename id} ... on AppealQueue {id}}}}}"}`,
			response:         `{"data":{"communities":[{"__typename":"Guild","queues":[{"__typename":"ModerationQueue","name":"ngq1","id":"gq1"},{"__typename":"AppealQueue","name":"nga1","id":"ga1"}]},{"__typename":"Channel","queues":[{"__typename":"ModerationQueue","name":"ncq1"},{"__typename":"AppealQueue","name":"nca1"}]}]}}`,
			expectedResponse: `{"data":{"communities":[{"queues":[{"name":"ngq1","id":"gq1"},{"name":"nga1","id":"ga1"}]},{"queues":[{"name":"ncq1"},{"name":"nca1"}]}]}}`,
		},
		{
			name:             "scalar fields of union members duplicated for an interface fragment",
			query:            `{ queues { flagged { ... on User { id } } ... on AppealQueue { flagged { ... on Named { id } ... on Bot { id } } } } }`,
			body:             `{"query":"{queues {__typename flagged {__typename ... on User {id}} ... on AppealQueue {flagged {__typename ... on Named {__typename id} ... on Bot {id}}}}}"}`,
			response:         `{"data":{"queues":[{"__typename":"ModerationQueue","flagged":[{"__typename":"User","id":"q1u1"},{"__typename":"Bot"}]},{"__typename":"AppealQueue","flagged":[{"__typename":"User","id":"a1u1"},{"__typename":"Bot","id":"a1b1"}]}]}}`,
			expectedResponse: `{"data":{"queues":[{"flagged":[{"id":"q1u1"},{}]},{"flagged":[{"id":"a1u1"},{"id":"a1b1"}]}]}}`,
		},
		{
			name:             "one response key for different fields of sibling type conditions",
			query:            `{ communities { queues { name } ... on Channel { queues { al: flaggedUsers { id } } } ... on Guild { queues { al: flagged { ... on User { id } } } } } }`,
			body:             `{"query":"{communities {__typename queues {__typename name} ... on Channel {queues {__typename al: flaggedUsers {id}}} ... on Guild {queues {__typename al: flagged {__typename ... on User {id}}}}}}"}`,
			response:         `{"data":{"communities":[{"__typename":"Guild","queues":[{"__typename":"ModerationQueue","name":"ngq1","al":[{"__typename":"User","id":"gq1u1"},{"__typename":"Bot"}]},{"__typename":"AppealQueue","name":"nga1","al":[{"__typename":"User","id":"ga1u1"},{"__typename":"Bot"}]}]},{"__typename":"Channel","queues":[{"__typename":"ModerationQueue","name":"ncq1","al":[{"id":"cq1u1"},{"id":"cq1u2"}]},{"__typename":"AppealQueue","name":"nca1","al":[{"id":"ca1u1"},{"id":"ca1u2"}]}]}]}}`,
			expectedResponse: `{"data":{"communities":[{"queues":[{"name":"ngq1","al":[{"id":"gq1u1"},{}]},{"name":"nga1","al":[{"id":"ga1u1"},{}]}]},{"queues":[{"name":"ncq1","al":[{"id":"cq1u1"},{"id":"cq1u2"}]},{"name":"nca1","al":[{"id":"ca1u1"},{"id":"ca1u2"}]}]}]}}`,
		},
		{
			name:             "nested list fields of sibling type conditions",
			query:            `{ queues { flaggedUsers { id } ... on ModerationQueue { flaggedUsers { grid { id } } } ... on AppealQueue { flaggedUsers { grid { name } } } } }`,
			body:             `{"query":"{queues {__typename flaggedUsers {id} ... on ModerationQueue {flaggedUsers {grid {id}}} ... on AppealQueue {flaggedUsers {grid {name}}}}}"}`,
			response:         `{"data":{"queues":[{"__typename":"ModerationQueue","flaggedUsers":[{"id":"q1u1","grid":[[{"id":"q1u1g1"},{"id":"q1u1g2"}],[{"id":"q1u1g3"}]]},{"id":"q1u2","grid":[[{"id":"q1u2g1"},{"id":"q1u2g2"}],[{"id":"q1u2g3"}]]}]},{"__typename":"AppealQueue","flaggedUsers":[{"id":"a1u1","grid":[[{"name":"na1u1g1"},{"name":"na1u1g2"}],[{"name":"na1u1g3"}]]},{"id":"a1u2","grid":[[{"name":"na1u2g1"},{"name":"na1u2g2"}],[{"name":"na1u2g3"}]]}]}]}}`,
			expectedResponse: `{"data":{"queues":[{"flaggedUsers":[{"id":"q1u1","grid":[[{"id":"q1u1g1"},{"id":"q1u1g2"}],[{"id":"q1u1g3"}]]},{"id":"q1u2","grid":[[{"id":"q1u2g1"},{"id":"q1u2g2"}],[{"id":"q1u2g3"}]]}]},{"flaggedUsers":[{"id":"a1u1","grid":[[{"name":"na1u1g1"},{"name":"na1u1g2"}],[{"name":"na1u1g3"}]]},{"id":"a1u2","grid":[[{"name":"na1u2g1"},{"name":"na1u2g2"}],[{"name":"na1u2g3"}]]}]}]}}`,
		},
		{
			name:             "scalar fields whose type conditions include those of the other",
			query:            `{ communities { queues { id } ... on Guild { queues { ... on ModerationQueue { flaggedUsers { id } } } } ... on Channel { queues { flaggedUsers { id } ... on AppealQueue { flaggedUsers { id } } } } } }`,
			body:             `{"query":"{communities {__typename queues {__typename id} ... on Guild {queues {__typename ... on ModerationQueue {flaggedUsers {id}}}} ... on Channel {queues {__typename flaggedUsers {id} ... on AppealQueue {flaggedUsers {id}}}}}}"}`,
			response:         `{"data":{"communities":[{"__typename":"Guild","queues":[{"__typename":"ModerationQueue","id":"gq1","flaggedUsers":[{"id":"gq1u1"},{"id":"gq1u2"}]},{"__typename":"AppealQueue","id":"ga1"}]},{"__typename":"Channel","queues":[{"__typename":"ModerationQueue","id":"cq1","flaggedUsers":[{"id":"cq1u1"},{"id":"cq1u2"}]},{"__typename":"AppealQueue","id":"ca1","flaggedUsers":[{"id":"ca1u1"},{"id":"ca1u2"}]}]}]}}`,
			expectedResponse: `{"data":{"communities":[{"queues":[{"id":"gq1","flaggedUsers":[{"id":"gq1u1"},{"id":"gq1u2"}]},{"id":"ga1"}]},{"queues":[{"id":"cq1","flaggedUsers":[{"id":"cq1u1"},{"id":"cq1u2"}]},{"id":"ca1","flaggedUsers":[{"id":"ca1u1"},{"id":"ca1u2"}]}]}]}}`,
		},
		{
			name:             "scalar fields whose type conditions exclude each other at two depths",
			query:            `{ communities { queues { named { id } } ... on Guild { queues { ... on ModerationQueue { named { name } } ... on AppealQueue { named { ... on User { name } } } } } ... on Channel { queues { ... on AppealQueue { named { name } } } } } }`,
			body:             `{"query":"{communities {__typename queues {__typename named {__typename id}} ... on Guild {queues {__typename ... on ModerationQueue {named {__typename name}} ... on AppealQueue {named {__typename ... on User {name}}}}} ... on Channel {queues {__typename ... on AppealQueue {named {__typename name}}}}}}"}`,
			response:         `{"data":{"communities":[{"__typename":"Guild","queues":[{"__typename":"ModerationQueue","named":[{"__typename":"User","id":"gq1u1","name":"ngq1u1"},{"__typename":"Bot","id":"gq1b1","name":"ngq1b1"}]},{"__typename":"AppealQueue","named":[{"__typename":"User","id":"ga1u1","name":"nga1u1"},{"__typename":"Bot","id":"ga1b1"}]}]},{"__typename":"Channel","queues":[{"__typename":"ModerationQueue","named":[{"__typename":"User","id":"cq1u1"},{"__typename":"Bot","id":"cq1b1"}]},{"__typename":"AppealQueue","named":[{"__typename":"User","id":"ca1u1","name":"nca1u1"},{"__typename":"Bot","id":"ca1b1","name":"nca1b1"}]}]}]}}`,
			expectedResponse: `{"data":{"communities":[{"queues":[{"named":[{"id":"gq1u1","name":"ngq1u1"},{"id":"gq1b1","name":"ngq1b1"}]},{"named":[{"id":"ga1u1","name":"nga1u1"},{"id":"ga1b1"}]}]},{"queues":[{"named":[{"id":"cq1u1"},{"id":"cq1b1"}]},{"named":[{"id":"ca1u1","name":"nca1u1"},{"id":"ca1b1","name":"nca1b1"}]}]}]}}`,
		},
		{
			name:             "object fields that overlap a field kept apart before",
			query:            `{ communities { queues { id } ... on Guild { queues { flaggedUsers { id } ... on ModerationQueue { flaggedUsers { friends { id } } } } } ... on Channel { queues { flaggedUsers { friends { name } } ... on ModerationQueue { flaggedUsers { friends { id } } } } } } }`,
			body:             `{"query":"{communities {__typename queues {__typename id} ... on Guild {queues {__typename flaggedUsers {id} ... on ModerationQueue {flaggedUsers {friends {id}}}}} ... on Channel {queues {__typename flaggedUsers {friends {name}} ... on ModerationQueue {flaggedUsers {friends {id}}}}}}}"}`,
			response:         `{"data":{"communities":[{"__typename":"Guild","queues":[{"__typename":"ModerationQueue","id":"gq1","flaggedUsers":[{"id":"gq1u1","friends":[{"id":"gq1u1f1"},{"id":"gq1u1f2"}]},{"id":"gq1u2","friends":[{"id":"gq1u2f1"},{"id":"gq1u2f2"}]}]},{"__typename":"AppealQueue","id":"ga1","flaggedUsers":[{"id":"ga1u1"},{"id":"ga1u2"}]}]},{"__typename":"Channel","queues":[{"__typename":"ModerationQueue","id":"cq1","flaggedUsers":[{"friends":[{"name":"ncq1u1f1","id":"cq1u1f1"},{"name":"ncq1u1f2","id":"cq1u1f2"}]},{"friends":[{"name":"ncq1u2f1","id":"cq1u2f1"},{"name":"ncq1u2f2","id":"cq1u2f2"}]}]},{"__typename":"AppealQueue","id":"ca1","flaggedUsers":[{"friends":[{"name":"nca1u1f1"},{"name":"nca1u1f2"}]},{"friends":[{"name":"nca1u2f1"},{"name":"nca1u2f2"}]}]}]}]}}`,
			expectedResponse: `{"data":{"communities":[{"queues":[{"id":"gq1","flaggedUsers":[{"id":"gq1u1","friends":[{"id":"gq1u1f1"},{"id":"gq1u1f2"}]},{"id":"gq1u2","friends":[{"id":"gq1u2f1"},{"id":"gq1u2f2"}]}]},{"id":"ga1","flaggedUsers":[{"id":"ga1u1"},{"id":"ga1u2"}]}]},{"queues":[{"id":"cq1","flaggedUsers":[{"friends":[{"name":"ncq1u1f1","id":"cq1u1f1"},{"name":"ncq1u1f2","id":"cq1u1f2"}]},{"friends":[{"name":"ncq1u2f1","id":"cq1u2f1"},{"name":"ncq1u2f2","id":"cq1u2f2"}]}]},{"id":"ca1","flaggedUsers":[{"friends":[{"name":"nca1u1f1"},{"name":"nca1u1f2"}]},{"friends":[{"name":"nca1u2f1"},{"name":"nca1u2f2"}]}]}]}]}}`,
		},
		{
			name:     "object fields of sibling type conditions where one is deferred",
			query:    `{ queues { flaggedUsers { id } ... on ModerationQueue { flaggedUsers { friends { id } } } ... on AppealQueue { ... @defer { flaggedUsers { friends { id } } } } } }`,
			body:     `{"query":"{queues {__typename flaggedUsers {id} ... on ModerationQueue {flaggedUsers {friends {id}}} ... on AppealQueue {__internal_typename: __typename}}}"}`,
			response: `{"data":{"queues":[{"__typename":"ModerationQueue","flaggedUsers":[{"id":"q1u1","friends":[{"id":"q1u1f1"},{"id":"q1u1f2"}]},{"id":"q1u2","friends":[{"id":"q1u2f1"},{"id":"q1u2f2"}]}]},{"__typename":"AppealQueue","flaggedUsers":[{"id":"a1u1"},{"id":"a1u2"}],"__internal_typename":"AppealQueue"}]}}`,
			deferred: map[string]string{
				`{"query":"{queues {__typename ... on AppealQueue {flaggedUsers {friends {id}}}}}"}`: `{"data":{"queues":[{"__typename":"ModerationQueue"},{"__typename":"AppealQueue","flaggedUsers":[{"friends":[{"id":"a1u1f1"},{"id":"a1u1f2"}]},{"friends":[{"id":"a1u2f1"},{"id":"a1u2f2"}]}]}]}}`,
			},
			expectedResponse: `{"data":{"queues":[{"flaggedUsers":[{"id":"q1u1","friends":[{"id":"q1u1f1"},{"id":"q1u1f2"}]},{"id":"q1u2","friends":[{"id":"q1u2f1"},{"id":"q1u2f2"}]}]},{"flaggedUsers":[{"id":"a1u1"},{"id":"a1u2"}]}]},"pending":[{"id":"1","path":["queues"]}],"hasNext":true}
{"incremental":[{"data":{"friends":[{"id":"a1u1f1"},{"id":"a1u1f2"}]},"id":"1","subPath":[1,"flaggedUsers",0]},{"data":{"friends":[{"id":"a1u2f1"},{"id":"a1u2f2"}]},"id":"1","subPath":[1,"flaggedUsers",1]}],"completed":[{"id":"1"}],"hasNext":false}`,
		},
		{
			name:     "object fields of sibling type conditions where the first is deferred",
			query:    `{ queues { flaggedUsers { id } ... on ModerationQueue { flaggedUsers { ... @defer { friends { id } } } } ... on AppealQueue { flaggedUsers { friends { id } } } } }`,
			body:     `{"query":"{queues {__typename flaggedUsers {id} ... on ModerationQueue {flaggedUsers {__internal_typename: __typename}} ... on AppealQueue {flaggedUsers {friends {id}}}}}"}`,
			response: `{"data":{"queues":[{"__typename":"ModerationQueue","flaggedUsers":[{"id":"q1u1","__internal_typename":"User"},{"id":"q1u2","__internal_typename":"User"}]},{"__typename":"AppealQueue","flaggedUsers":[{"id":"a1u1","friends":[{"id":"a1u1f1"},{"id":"a1u1f2"}]},{"id":"a1u2","friends":[{"id":"a1u2f1"},{"id":"a1u2f2"}]}]}]}}`,
			deferred: map[string]string{
				`{"query":"{queues {__typename ... on ModerationQueue {flaggedUsers {friends {id}}}}}"}`: `{"data":{"queues":[{"__typename":"ModerationQueue","flaggedUsers":[{"friends":[{"id":"q1u1f1"},{"id":"q1u1f2"}]},{"friends":[{"id":"q1u2f1"},{"id":"q1u2f2"}]}]},{"__typename":"AppealQueue"}]}}`,
			},
			expectedResponse: `{"data":{"queues":[{"flaggedUsers":[{"id":"q1u1"},{"id":"q1u2"}]},{"flaggedUsers":[{"id":"a1u1","friends":[{"id":"a1u1f1"},{"id":"a1u1f2"}]},{"id":"a1u2","friends":[{"id":"a1u2f1"},{"id":"a1u2f2"}]}]}]},"pending":[{"id":"1","path":["queues"]}],"hasNext":true}
{"incremental":[{"data":{"friends":[{"id":"q1u1f1"},{"id":"q1u1f2"}]},"id":"1","subPath":[0,"flaggedUsers",0]},{"data":{"friends":[{"id":"q1u2f1"},{"id":"q1u2f2"}]},"id":"1","subPath":[0,"flaggedUsers",1]}],"completed":[{"id":"1"}],"hasNext":false}`,
		},
		{
			name:     "scalar fields of sibling type conditions where one is deferred",
			query:    `{ communities { queues { name } ... on Guild { queues { id } } ... on Channel { ... @defer { queues { id } } } } }`,
			body:     `{"query":"{communities {__typename queues {__typename name} ... on Guild {queues {__typename id}} ... on Channel {__internal_typename: __typename}}}"}`,
			response: `{"data":{"communities":[{"__typename":"Guild","queues":[{"__typename":"ModerationQueue","name":"ngq1","id":"gq1"},{"__typename":"AppealQueue","name":"nga1","id":"ga1"}]},{"__typename":"Channel","queues":[{"__typename":"ModerationQueue","name":"ncq1"},{"__typename":"AppealQueue","name":"nca1"}],"__internal_typename":"Channel"}]}}`,
			deferred: map[string]string{
				`{"query":"{communities {__typename ... on Channel {queues {__typename id}}}}"}`: `{"data":{"communities":[{"__typename":"Guild"},{"__typename":"Channel","queues":[{"__typename":"ModerationQueue","id":"cq1"},{"__typename":"AppealQueue","id":"ca1"}]}]}}`,
			},
			expectedResponse: `{"data":{"communities":[{"queues":[{"name":"ngq1","id":"gq1"},{"name":"nga1","id":"ga1"}]},{"queues":[{"name":"ncq1"},{"name":"nca1"}]}]},"pending":[{"id":"1","path":["communities"]}],"hasNext":true}
{"incremental":[{"data":{"id":"cq1"},"id":"1","subPath":[1,"queues",0]},{"data":{"id":"ca1"},"id":"1","subPath":[1,"queues",1]}],"completed":[{"id":"1"}],"hasNext":false}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			responses := map[string]sendResponse{tc.body: {statusCode: 200, body: tc.response}}
			for body, response := range tc.deferred {
				responses[body] = sendResponse{statusCode: 200, body: response}
			}
			moderation := mustGraphqlDataSourceConfiguration(t,
				"moderation",
				mustFactory(t, recordingClient(t, nil, "moderation", "/", responses)),
				&plan.DataSourceMetadata{
					RootNodes: []plan.TypeField{
						{TypeName: "Query", FieldNames: []string{"queues", "communities"}},
					},
					ChildNodes: []plan.TypeField{
						{TypeName: "Community", FieldNames: []string{"id", "queues"}},
						{TypeName: "Guild", FieldNames: []string{"id", "queues"}},
						{TypeName: "Channel", FieldNames: []string{"id", "queues"}},
						{TypeName: "Queue", FieldNames: []string{"id", "name", "flaggedUsers", "flagged", "named"}},
						{TypeName: "Reviewable", FieldNames: []string{"id"}},
						{TypeName: "ModerationQueue", FieldNames: []string{"id", "name", "flaggedUsers", "flagged", "named"}},
						{TypeName: "AppealQueue", FieldNames: []string{"id", "name", "flaggedUsers", "flagged", "named"}},
						{TypeName: "Named", FieldNames: []string{"id", "name"}},
						{TypeName: "User", FieldNames: []string{"id", "name", "friends", "grid"}},
						{TypeName: "Bot", FieldNames: []string{"id", "name"}},
					},
				},
				mustConfiguration(t, graphql_datasource.ConfigurationInput{
					Fetch:               &graphql_datasource.FetchConfiguration{URL: "http://moderation/", Method: "POST"},
					SchemaConfiguration: mustSchemaConfig(t, nil, mergeFieldsSchema),
				}),
			)

			schema, err := graphql.NewSchemaFromString(mergeFieldsSchema)
			require.NoError(t, err)
			engineConf := NewConfiguration(schema)
			engineConf.SetDataSources([]plan.DataSource{moderation})

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			engine, err := NewExecutionEngine(ctx, abstractlogger.Noop{}, engineConf, resolve.ResolverOptions{MaxConcurrency: 1024})
			require.NoError(t, err)

			resultWriter := graphql.NewEngineResultWriter()
			var frames []string
			resultWriter.SetFlushCallback(func(data []byte) {
				frames = append(frames, string(data))
			})
			require.NoError(t, engine.Execute(ctx, &graphql.Request{Query: tc.query}, &resultWriter))
			if len(frames) == 0 {
				frames = append(frames, resultWriter.String())
			}
			assert.Equal(t, tc.expectedResponse, strings.Join(frames, "\n"))
		})
	}
}
