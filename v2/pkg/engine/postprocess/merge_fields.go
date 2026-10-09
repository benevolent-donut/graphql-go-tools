package postprocess

import (
	"bytes"
	"slices"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

type mergeFields struct {
	disable          bool
	deferDescriptors map[int]resolve.DeferDescriptor
}

func (m *mergeFields) Process(node resolve.Node) {
	if m.disable {
		return
	}
	m.traverseNode(node)
}

func (m *mergeFields) ProcessDefer(node resolve.Node, deferDescriptors map[int]resolve.DeferDescriptor) {
	if m.disable {
		return
	}
	(&mergeFields{deferDescriptors: deferDescriptors}).traverseNode(node)
}

func (m *mergeFields) ProcessSubscription(node resolve.Node) {
	if m.disable {
		return
	}
	m.traverseNode(node)
}

func (m *mergeFields) traverseNode(node resolve.Node) {
	switch n := node.(type) {
	case *resolve.Object:
		if len(n.Fields) == 1 {
			m.traverseNode(n.Fields[0].Value)
			return
		}
		for i := 0; i < len(n.Fields); i++ {
			// 1. duplicate fields with multiple onTypeNames so they can be merged
			if len(n.Fields[i].OnTypeNames) >= 1 {
				additionalTypeNames := make([][]byte, len(n.Fields[i].OnTypeNames)-1)
				copy(additionalTypeNames, n.Fields[i].OnTypeNames[1:])
				n.Fields[i].OnTypeNames = [][]byte{n.Fields[i].OnTypeNames[0]}
				for j := range additionalTypeNames {
					additionalField := &resolve.Field{
						Name:              n.Fields[i].Name,
						Value:             n.Fields[i].Value.Copy(),
						Position:          n.Fields[i].Position,
						Defer:             n.Fields[i].Defer,
						OnTypeNames:       [][]byte{additionalTypeNames[j]},
						ParentOnTypeNames: slices.Clone(n.Fields[i].ParentOnTypeNames),
						Info:              n.Fields[i].Info,
					}
					m.copyParentOnTypeNames(n.Fields[i].Value, additionalField.Value)
					n.Fields = append(n.Fields[:i+1], append([]*resolve.Field{additionalField}, n.Fields[i+1:]...)...)
				}
			}
			// 2.
			// Propagate onTypeNames to all children
			// In a later stage, we merge all nested scalar fields with the same name
			// However, scalar fields can originate from different parent types, which is why we need to propagate them here
			m.propagateParentTypeNames(n.Fields[i])
		}
		// 3. merge fields without onTypeNames "over" fields with onTypeNames
		// This is possible because if a field exists without onTypeNames, it will always be resolved
		// There are 2 variants of this:
		// 3.1. if the source and target fields are scalars, the target (with onTypeNames) will be removed
		// This means that scalars with no onTypeNames will overwrite scalars with onTypeNames
		// 3.2. if the source and target fields are objects, all fields from the source will be merged into the target
		// This means that objects with no onTypeNames will be merged into objects with onTypeNames
		for i := 0; i < len(n.Fields); i++ {
			if n.Fields[i].OnTypeNames != nil {
				continue
			}
			for j := 0; j < len(n.Fields); j++ {
				if i == j {
					continue
				}
				if n.Fields[j].OnTypeNames == nil {
					continue
				}
				if bytes.Equal(n.Fields[i].Name, n.Fields[j].Name) {
					// a field without onTypeNames is not resolved below parents its parent type conditions exclude,
					// and the fields below a deferred object are rendered only in its defer scope
					if m.parentOnTypeNamesExclude(n.Fields[i], n.Fields[j]) || (!m.nodeIsScalar(n.Fields[i].Value) && !m.deferIncludes(n.Fields[i], n.Fields[j])) {
						continue
					}
					m.mergeValues(n.Fields[i], n.Fields[j])
					n.Fields = append(n.Fields[:j], n.Fields[j+1:]...)
					if i > j {
						i--
					}
					j--
				}
			}
		}
		// 4. merge sibling object fields
		for i := 0; i < len(n.Fields); i++ {
			for j := i + 1; j < len(n.Fields); j++ {
				if m.fieldsCanMerge(n.Fields[i], n.Fields[j]) {
					m.mergeValues(n.Fields[i], n.Fields[j])
					widened := m.widenParentOnTypeNames(n.Fields[i], n.Fields[j])
					n.Fields = append(n.Fields[:j], n.Fields[j+1:]...)
					j--
					if widened {
						// fields kept apart before can be resolved below the same parents as the merged field now
						j = i
					}
				}
			}
		}
		// 5. merge sibling scalar fields
		// Once all objects have been merged, we need to merge (deduplicate) all scalar fields that are left
		for i := 0; i < len(n.Fields); i++ {
			// skip objects
			if m.nodeIsScalar(n.Fields[i].Value) {
				for j := 0; j < len(n.Fields); j++ {
					if i == j {
						continue
					}
					if bytes.Equal(n.Fields[i].Name, n.Fields[j].Name) {
						// we don't merge scalars with different onTypeNames to preserve the order of the fields
						if !m.canMergeScalars(n.Fields[i], n.Fields[j]) {
							continue
						}
						widened := m.mergeScalars(n.Fields[i], n.Fields[j])
						n.Fields = append(n.Fields[:j], n.Fields[j+1:]...)
						if i > j {
							i--
						}
						j--
						if widened {
							j = -1
						}
					}
				}
			}
		}
		for i := 0; i < len(n.Fields); i++ {
			m.traverseNode(n.Fields[i].Value)
		}
	case *resolve.Array:
		m.traverseNode(n.Item)
	}
}

func (m *mergeFields) canMergeScalars(left, right *resolve.Field) bool {
	if !m.sameDefer(left, right) {
		return false
	}
	// step 3 keeps fields with and without onTypeNames apart only if their parent type conditions exclude each other
	if (left.OnTypeNames == nil) != (right.OnTypeNames == nil) {
		return false
	}
	if left.OnTypeNames != nil && right.OnTypeNames != nil {
		if !m.sameOnTypeNames(left.OnTypeNames, right.OnTypeNames) {
			return false
		}
	}
	// fields that are never resolved below the same parents stay apart, because a merged field keeps the conditions of one
	return !m.parentOnTypeNamesExclude(left, right)
}

// mergeScalars merges the type conditions of right into left and reports whether left is resolved below more parents
func (m *mergeFields) mergeScalars(left, right *resolve.Field) bool {
	// when left has no type conditions, it will overwrite right
	if left.OnTypeNames == nil && left.ParentOnTypeNames == nil {
		return false
	}
	// when right has no type conditions, it will overwrite left
	if right.OnTypeNames == nil && right.ParentOnTypeNames == nil {
		left.OnTypeNames = nil
		left.ParentOnTypeNames = nil
		return true
	}
	left.OnTypeNames = m.deduplicateOnTypeNames(append(left.OnTypeNames, right.OnTypeNames...))
	if m.parentOnTypeNamesInclude(left, right) {
		return false
	}
	// the value of left is rendered below the parents of right, which only fits a field of the same definition
	if !m.sameFieldDefinition(left, right) {
		return false
	}
	if !m.parentOnTypeNamesInclude(right, left) {
		// no single set of conditions holds below the parents of both, so left keeps its own
		return false
	}
	left.ParentOnTypeNames = right.ParentOnTypeNames
	return true
}

func (m *mergeFields) fieldsCanMerge(left *resolve.Field, right *resolve.Field) bool {
	if !bytes.Equal(left.Name, right.Name) {
		return false
	}
	if !m.deferIncludes(left, right) {
		return false
	}
	if left.Value.NodeKind() != right.Value.NodeKind() {
		return false
	}
	if !m.sameOnTypeNames(left.OnTypeNames, right.OnTypeNames) {
		return false
	}
	// scalars with different parent type conditions can't be merged at this point
	// we're handling this case later when we merge scalar fields
	if m.nodeIsScalar(left.Value) && !m.sameParentOnTypeNames(left, right) {
		return false
	}
	// fields that are never resolved below the same parents stay apart, because a merged field keeps the conditions of one
	if m.parentOnTypeNamesExclude(left, right) {
		return false
	}
	return true
}

// widenParentOnTypeNames sets the parent type conditions of right on the merged field left
// if right is resolved below more parents, and reports whether it did
func (m *mergeFields) widenParentOnTypeNames(left, right *resolve.Field) bool {
	if m.parentOnTypeNamesInclude(left, right) || !m.parentOnTypeNamesInclude(right, left) {
		return false
	}
	// left is rendered below the parents of right with its own value and defer scope,
	// and the fields of nested list items have no parent type conditions
	if !m.sameFieldDefinition(left, right) || !m.sameDefer(left, right) || m.isNestedList(left.Value) {
		return false
	}
	left.ParentOnTypeNames = right.ParentOnTypeNames
	return true
}

// sameDefer reports whether two fields belong to one defer scope, because a merged field is rendered only in the
// defer scope of the left field.
func (m *mergeFields) sameDefer(left, right *resolve.Field) bool {
	if left.Defer == nil || right.Defer == nil {
		return left.Defer == nil && right.Defer == nil
	}
	return left.Defer.DeferID == right.Defer.DeferID
}

// deferIncludes reports whether the fields of right can be merged into left,
// because the fields below a deferred field are rendered only in its defer scope and the scopes nested in it
func (m *mergeFields) deferIncludes(left, right *resolve.Field) bool {
	if left.Defer == nil {
		return true
	}
	if right.Defer == nil {
		return false
	}
	for id := right.Defer.DeferID; id != 0; id = m.deferDescriptors[id].ParentID {
		if id == left.Defer.DeferID {
			return true
		}
	}
	return false
}

func (m *mergeFields) sameFieldDefinition(left, right *resolve.Field) bool {
	if left.Info == nil || right.Info == nil {
		return left.Info == right.Info
	}
	return left.Info.Name == right.Info.Name
}

func (m *mergeFields) isNestedList(node resolve.Node) bool {
	array, ok := node.(*resolve.Array)
	return ok && array.Item.NodeKind() == resolve.NodeKindArray
}

func (m *mergeFields) deduplicateOnTypeNames(onTypeNames [][]byte) [][]byte {
	uniqueTypeNames := make(map[string]struct{}, len(onTypeNames))
	for _, typeName := range onTypeNames {
		uniqueTypeNames[string(typeName)] = struct{}{}
	}
	if len(uniqueTypeNames) == len(onTypeNames) {
		return onTypeNames
	}
	result := make([][]byte, 0, len(uniqueTypeNames))
	for typeName := range uniqueTypeNames {
		result = append(result, []byte(typeName))
	}
	return result
}

func (m *mergeFields) sameOnTypeNames(left, right [][]byte) bool {
	if len(left) != len(right) {
		return false
	}
WithNext:
	for i := range left {
		for j := range right {
			if bytes.Equal(left[i], right[j]) {
				continue WithNext
			}
		}
		return false
	}
	return true
}

func (m *mergeFields) sameParentOnTypeNames(left, right *resolve.Field) bool {
	if len(left.ParentOnTypeNames) != len(right.ParentOnTypeNames) {
		return false
	}
	for i := range left.ParentOnTypeNames {
		for j := range right.ParentOnTypeNames {
			if left.ParentOnTypeNames[i].Depth != right.ParentOnTypeNames[j].Depth {
				continue
			}
			if !m.sameOnTypeNames(left.ParentOnTypeNames[i].Names, right.ParentOnTypeNames[j].Names) {
				continue
			}
			break
		}
		return false
	}
	return true
}

// parentOnTypeNamesInclude reports whether outer is resolved below all parents that inner is resolved below
func (m *mergeFields) parentOnTypeNamesInclude(outer, inner *resolve.Field) bool {
WithNext:
	for i := range outer.ParentOnTypeNames {
		for j := range inner.ParentOnTypeNames {
			if outer.ParentOnTypeNames[i].Depth == inner.ParentOnTypeNames[j].Depth {
				if !m.containsOnTypeNames(outer.ParentOnTypeNames[i].Names, inner.ParentOnTypeNames[j].Names) {
					return false
				}
				continue WithNext
			}
		}
		return false
	}
	return true
}

func (m *mergeFields) containsOnTypeNames(names, subset [][]byte) bool {
	for i := range subset {
		if !slices.ContainsFunc(names, func(name []byte) bool { return bytes.Equal(name, subset[i]) }) {
			return false
		}
	}
	return true
}

// parentOnTypeNamesExclude reports whether left and right are never resolved below the same parents
func (m *mergeFields) parentOnTypeNamesExclude(left, right *resolve.Field) bool {
	for i := range left.ParentOnTypeNames {
		for j := range right.ParentOnTypeNames {
			if left.ParentOnTypeNames[i].Depth == right.ParentOnTypeNames[j].Depth && !m.shareOnTypeName(left.ParentOnTypeNames[i].Names, right.ParentOnTypeNames[j].Names) {
				return true
			}
		}
	}
	return false
}

func (m *mergeFields) shareOnTypeName(left, right [][]byte) bool {
	for i := range left {
		for j := range right {
			if bytes.Equal(left[i], right[j]) {
				return true
			}
		}
	}
	return false
}

func (m *mergeFields) mergeValues(left, right *resolve.Field) {
	switch l := left.Value.(type) {
	case *resolve.Object:
		r := right.Value.(*resolve.Object)
		l.Fields = append(l.Fields, r.Fields...)
	case *resolve.Array:
		r := right.Value.(*resolve.Array)
		if l.Item.NodeKind() == resolve.NodeKindObject {
			lo := l.Item.(*resolve.Object)
			ro := r.Item.(*resolve.Object)
			lo.Fields = append(lo.Fields, ro.Fields...)
		}
	}
}

// copyParentOnTypeNames copies the parent type conditions of the fields below from to the fields below to,
// because Copy leaves them out
func (m *mergeFields) copyParentOnTypeNames(from, to resolve.Node) {
	switch f := from.(type) {
	case *resolve.Object:
		t := to.(*resolve.Object)
		for i := range f.Fields {
			t.Fields[i].ParentOnTypeNames = slices.Clone(f.Fields[i].ParentOnTypeNames)
			m.copyParentOnTypeNames(f.Fields[i].Value, t.Fields[i].Value)
		}
	case *resolve.Array:
		m.copyParentOnTypeNames(f.Item, to.(*resolve.Array).Item)
	}
}

func (m *mergeFields) nodeIsScalar(node resolve.Node) bool {
	switch node.(type) {
	case *resolve.Object, *resolve.Array:
		return false
	}
	return true
}

func (m *mergeFields) propagateParentTypeNames(field *resolve.Field) {
	if field.OnTypeNames == nil {
		return
	}
	m.setParentTypeNames(field, field.OnTypeNames, 1)
}

// setParentTypeNames recursively sets the parent type names for all children of a field
// increasing the depth by 1 for each level
func (m *mergeFields) setParentTypeNames(field *resolve.Field, typeNames [][]byte, depth int) {
	switch field.Value.NodeKind() {
	case resolve.NodeKindObject:
		object := field.Value.(*resolve.Object)
		for i := range object.Fields {
			object.Fields[i].ParentOnTypeNames = append(object.Fields[i].ParentOnTypeNames, resolve.ParentOnTypeNames{
				Depth: depth,
				Names: typeNames,
			})
			m.setParentTypeNames(object.Fields[i], typeNames, depth+1)
		}
	case resolve.NodeKindArray:
		array := field.Value.(*resolve.Array)
		if array.Item.NodeKind() == resolve.NodeKindObject {
			object := array.Item.(*resolve.Object)
			for i := range object.Fields {
				object.Fields[i].ParentOnTypeNames = append(object.Fields[i].ParentOnTypeNames, resolve.ParentOnTypeNames{
					Depth: depth,
					Names: typeNames,
				})
				m.setParentTypeNames(object.Fields[i], typeNames, depth+1)
			}
		}
	}
}
