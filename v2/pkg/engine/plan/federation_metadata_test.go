package plan

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFederationMetaData_ContextualArgumentsForField(t *testing.T) {
	profile := ContextualArgumentConfiguration{
		TypeName:     "User",
		FieldName:    "profile",
		ArgumentName: "includeSuspended",
		ContextName:  "userScope",
		Setters: []ContextSetterConfiguration{
			{TypeName: "ModerationQueue", SelectionSet: "includeSuspended"},
			{TypeName: "Post"},
		},
	}
	digestLevel := ContextualArgumentConfiguration{
		TypeName:     "User",
		FieldName:    "digest",
		ArgumentName: "level",
		ContextName:  "policyScope",
		Setters:      []ContextSetterConfiguration{{TypeName: "ModerationQueue", SelectionSet: "policyLevel"}},
	}
	digestIncludeSuspended := ContextualArgumentConfiguration{
		TypeName:     "User",
		FieldName:    "digest",
		ArgumentName: "includeSuspended",
		ContextName:  "userScope",
		Setters:      []ContextSetterConfiguration{{TypeName: "ModerationQueue", SelectionSet: "includeSuspended"}},
	}
	summary := ContextualArgumentConfiguration{
		TypeName:     "ModerationQueue",
		FieldName:    "summary",
		ArgumentName: "includeSuspended",
		ContextName:  "userScope",
		Setters:      []ContextSetterConfiguration{{TypeName: "Organization", SelectionSet: "includeSuspended"}},
	}

	t.Run("data source without contextual arguments has no index", func(t *testing.T) {
		metadata := &DataSourceMetadata{
			FederationMetaData: FederationMetaData{
				Keys: FederationFieldConfigurations{{TypeName: "User", SelectionSet: "id"}},
			},
		}
		require.NoError(t, metadata.Init())

		assert.Nil(t, metadata.contextualArgumentsByField)
		assert.Nil(t, metadata.ContextualArgumentsForField("User", "profile"))
	})

	t.Run("arguments are indexed by type and field in configuration order", func(t *testing.T) {
		metadata := &DataSourceMetadata{
			FederationMetaData: FederationMetaData{
				ContextualArguments: ContextualArgumentConfigurations{digestLevel, profile, digestIncludeSuspended, summary},
			},
		}
		require.NoError(t, metadata.Init())

		assert.Equal(t, []ContextualArgumentConfiguration{profile}, metadata.ContextualArgumentsForField("User", "profile"))
		assert.Equal(t, []ContextualArgumentConfiguration{digestLevel, digestIncludeSuspended}, metadata.ContextualArgumentsForField("User", "digest"))
		assert.Equal(t, []ContextualArgumentConfiguration{summary}, metadata.ContextualArgumentsForField("ModerationQueue", "summary"))
		assert.Nil(t, metadata.ContextualArgumentsForField("User", "id"))
		assert.Nil(t, metadata.ContextualArgumentsForField("ModerationQueue", "profile"))
	})

	t.Run("init without contextual arguments clears the index", func(t *testing.T) {
		metadata := &DataSourceMetadata{
			FederationMetaData: FederationMetaData{
				ContextualArguments: ContextualArgumentConfigurations{profile},
			},
		}
		require.NoError(t, metadata.Init())
		require.NotNil(t, metadata.ContextualArgumentsForField("User", "profile"))

		metadata.ContextualArguments = nil
		require.NoError(t, metadata.Init())
		assert.Nil(t, metadata.contextualArgumentsByField)
		assert.Nil(t, metadata.ContextualArgumentsForField("User", "profile"))
	})

	t.Run("data source configuration exposes the index", func(t *testing.T) {
		ds, err := NewDataSourceConfiguration[any]("users", nil, &DataSourceMetadata{
			FederationMetaData: FederationMetaData{
				ContextualArguments: ContextualArgumentConfigurations{profile},
			},
		}, nil)
		require.NoError(t, err)

		assert.Equal(t, []ContextualArgumentConfiguration{profile}, ds.ContextualArgumentsForField("User", "profile"))

		federation := ds.FederationConfiguration()
		assert.Equal(t, []ContextualArgumentConfiguration{profile}, federation.ContextualArgumentsForField("User", "profile"))
	})
}
