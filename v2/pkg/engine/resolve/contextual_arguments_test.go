package resolve

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testContextualArgumentCapture() *ContextualArgumentCapture {
	return &ContextualArgumentCapture{
		VariableName: "contextualArgument_0",
		Levels: []ContextualArgumentLevel{
			{
				Depth:           2,
				MatchByTypename: true,
				Setters: []ContextualArgumentSetter{
					{TypeName: "ModerationQueue", Path: []string{"__context_0"}},
					{TypeName: "Team"},
				},
			},
			{
				Depth: 0,
				Setters: []ContextualArgumentSetter{
					{TypeName: "Query", Path: []string{"__context_1", "includeSuspended"}},
				},
			},
		},
	}
}

func TestContextualArgumentCapture_Equals(t *testing.T) {
	t.Run("identical captures are equal", func(t *testing.T) {
		assert.True(t, testContextualArgumentCapture().Equals(testContextualArgumentCapture()))
	})

	t.Run("nil captures", func(t *testing.T) {
		var missing *ContextualArgumentCapture
		assert.True(t, missing.Equals(nil))
		assert.False(t, missing.Equals(testContextualArgumentCapture()))
		assert.False(t, testContextualArgumentCapture().Equals(nil))
	})

	t.Run("nil and empty null setter paths are equal", func(t *testing.T) {
		other := testContextualArgumentCapture()
		other.Levels[0].Setters[1].Path = []string{}
		assert.True(t, testContextualArgumentCapture().Equals(other))
	})

	differences := []struct {
		name   string
		modify func(capture *ContextualArgumentCapture)
	}{
		{name: "variable name", modify: func(capture *ContextualArgumentCapture) { capture.VariableName = "contextualArgument_1" }},
		{name: "level depth", modify: func(capture *ContextualArgumentCapture) { capture.Levels[1].Depth = 1 }},
		{name: "level match mode", modify: func(capture *ContextualArgumentCapture) { capture.Levels[0].MatchByTypename = false }},
		{name: "level order", modify: func(capture *ContextualArgumentCapture) {
			capture.Levels[0], capture.Levels[1] = capture.Levels[1], capture.Levels[0]
		}},
		{name: "level count", modify: func(capture *ContextualArgumentCapture) { capture.Levels = capture.Levels[:1] }},
		{name: "setter type name", modify: func(capture *ContextualArgumentCapture) { capture.Levels[0].Setters[0].TypeName = "AppealQueue" }},
		{name: "setter order", modify: func(capture *ContextualArgumentCapture) {
			setters := capture.Levels[0].Setters
			setters[0], setters[1] = setters[1], setters[0]
		}},
		{name: "setter count", modify: func(capture *ContextualArgumentCapture) {
			capture.Levels[0].Setters = capture.Levels[0].Setters[:1]
		}},
		{name: "setter path", modify: func(capture *ContextualArgumentCapture) {
			capture.Levels[1].Setters[0].Path = []string{"__context_1", "showSuspended"}
		}},
		{name: "null setter", modify: func(capture *ContextualArgumentCapture) { capture.Levels[0].Setters[0].Path = nil }},
	}

	for _, difference := range differences {
		t.Run("captures differing in "+difference.name+" are not equal", func(t *testing.T) {
			other := testContextualArgumentCapture()
			difference.modify(other)
			assert.False(t, testContextualArgumentCapture().Equals(other))
			assert.False(t, other.Equals(testContextualArgumentCapture()))
		})
	}
}

func TestContextualArgumentVariable(t *testing.T) {
	newVariable := func(modify func(capture *ContextualArgumentCapture)) *ContextualArgumentVariable {
		capture := testContextualArgumentCapture()
		if modify != nil {
			modify(capture)
		}
		return &ContextualArgumentVariable{Capture: *capture}
	}

	t.Run("kind is appended after the existing kinds", func(t *testing.T) {
		assert.Equal(t, VariableKind(5), ListVariableKind)
		assert.Equal(t, VariableKind(6), ContextualArgumentVariableKind)
		assert.Equal(t, ContextualArgumentVariableKind, newVariable(nil).GetVariableKind())
	})

	t.Run("template segment names the variable", func(t *testing.T) {
		assert.Equal(t, TemplateSegment{
			SegmentType:        VariableSegmentType,
			VariableKind:       ContextualArgumentVariableKind,
			VariableSourcePath: []string{"contextualArgument_0"},
		}, newVariable(nil).TemplateSegment())
	})

	t.Run("equals compares the whole capture", func(t *testing.T) {
		variable := newVariable(nil)
		assert.True(t, variable.Equals(newVariable(nil)))
		assert.False(t, variable.Equals(newVariable(func(capture *ContextualArgumentCapture) {
			capture.Levels[0].Setters[0].TypeName = "AppealQueue"
		})))
		assert.False(t, variable.Equals(nil))
		assert.False(t, variable.Equals(&ContextVariable{Path: []string{"contextualArgument_0"}}))
	})

	t.Run("identical captures share one variable", func(t *testing.T) {
		variables := NewVariables(&HeaderVariable{Path: []string{"Authorization"}})

		name, exists := variables.AddVariable(newVariable(nil))
		assert.Equal(t, "$$1$$", name)
		assert.False(t, exists)

		name, exists = variables.AddVariable(newVariable(nil))
		assert.Equal(t, "$$1$$", name)
		assert.True(t, exists)

		name, exists = variables.AddVariable(newVariable(func(capture *ContextualArgumentCapture) {
			capture.VariableName = "contextualArgument_1"
			capture.Levels[1].Setters[0].Path = []string{"__context_1", "showSuspended"}
		}))
		assert.Equal(t, "$$2$$", name)
		assert.False(t, exists)
		assert.Len(t, variables, 3)
	})
}

func TestIsContextJSON(t *testing.T) {
	t.Run("fetch reason omits is_context unless set", func(t *testing.T) {
		reason, err := json.Marshal(FetchReason{TypeName: "ModerationQueue", FieldName: "includeSuspended", IsKey: true})
		require.NoError(t, err)
		assert.Equal(t, `{"typename":"ModerationQueue","field":"includeSuspended","is_key":true}`, string(reason))

		reason, err = json.Marshal(FetchReason{TypeName: "ModerationQueue", FieldName: "includeSuspended", IsContext: true})
		require.NoError(t, err)
		assert.Equal(t, `{"typename":"ModerationQueue","field":"includeSuspended","is_context":true}`, string(reason))
	})

	t.Run("fetch dependency origin omits isContext unless set", func(t *testing.T) {
		coordinate := GraphCoordinate{TypeName: "ModerationQueue", FieldName: "includeSuspended"}

		origin, err := json.Marshal(FetchDependencyOrigin{FetchID: 1, Subgraph: "moderation", Coordinate: coordinate, IsKey: true})
		require.NoError(t, err)
		assert.Equal(t, `{"fetchId":1,"subgraph":"moderation","coordinate":{"typeName":"ModerationQueue","fieldName":"includeSuspended"},"isKey":true,"isRequires":false}`, string(origin))

		origin, err = json.Marshal(FetchDependencyOrigin{FetchID: 1, Subgraph: "moderation", Coordinate: coordinate, IsContext: true})
		require.NoError(t, err)
		assert.Equal(t, `{"fetchId":1,"subgraph":"moderation","coordinate":{"typeName":"ModerationQueue","fieldName":"includeSuspended"},"isKey":false,"isRequires":false,"isContext":true}`, string(origin))
	})
}
