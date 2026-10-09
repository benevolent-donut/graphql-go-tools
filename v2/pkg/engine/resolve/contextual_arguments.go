package resolve

import (
	"slices"
)

// ContextualArgumentAliasPrefix starts the response key under which the planner selects the value of a context.
const ContextualArgumentAliasPrefix = "__context_"

// ContextualArgumentCapture tells the loader where to read one contextual argument value for each item of an entity fetch.
type ContextualArgumentCapture struct {
	VariableName string
	// Levels lists the ancestor levels that can set the context, nearest first.
	Levels []ContextualArgumentLevel
}

type ContextualArgumentLevel struct {
	// Depth is the number of FetchPath elements applied to reach the level; 0 is the root data.
	Depth int
	// MatchByTypename selects a setter by the object's __typename; otherwise Setters[0] matches every object.
	MatchByTypename bool
	Setters         []ContextualArgumentSetter
}

type ContextualArgumentSetter struct {
	TypeName string
	// Path holds response keys from the setter object to the value; the first key is the planner alias, and an empty Path means null.
	Path []string
}

func (c *ContextualArgumentCapture) Equals(other *ContextualArgumentCapture) bool {
	if c == nil || other == nil {
		return c == other
	}
	return c.VariableName == other.VariableName && slices.EqualFunc(c.Levels, other.Levels, equalContextualArgumentLevels)
}

func equalContextualArgumentLevels(a, b ContextualArgumentLevel) bool {
	return a.Depth == b.Depth && a.MatchByTypename == b.MatchByTypename && slices.EqualFunc(a.Setters, b.Setters, equalContextualArgumentSetters)
}

func equalContextualArgumentSetters(a, b ContextualArgumentSetter) bool {
	return a.TypeName == b.TypeName && slices.Equal(a.Path, b.Path)
}
