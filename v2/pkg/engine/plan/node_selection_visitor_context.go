package plan

import (
	"bytes"
	"fmt"
	"slices"
	"strconv"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

const contextAliasPrefix = "__context_"

// contextualArgumentRequirement is the planning decision for one contextual argument of a consumer field.
type contextualArgumentRequirement struct {
	argumentName string
	// levels lists the ancestor levels that can set the context, nearest first; nil when no level can.
	levels []resolve.ContextualArgumentLevel
}

// contextPathLevel describes the objects in which one path field of a consumer is selected.
type contextPathLevel struct {
	depth         int
	typeName      string // static type of the objects
	targetSetRef  int    // selection set directly containing the path field
	levelSetRef   int    // selection set of the owner field, or the operation selection set at depth 0
	ownerFieldRef int    // field whose selection set holds the objects, ast.InvalidRef at depth 0
	ownerTypeName string // enclosing type of the owner field
}

type pendingContextInjection struct {
	typeName      string
	fieldSet      string
	setterType    string
	selection     string
	alias         []byte // nil for the __typename of an abstract level
	abstract      bool
	ownerFieldRef int
	deferInfo     *DeferInfo
	requestedBy   []fieldIndexKey
}

func (c *nodeSelectionVisitor) handleFieldContextArguments(fieldCtx fieldRequirementsContext) {
	arguments := fieldCtx.dsConfig.ContextualArgumentsForField(fieldCtx.typeName, fieldCtx.fieldName)
	if len(arguments) == 0 {
		return
	}

	fieldKey := fieldIndexKey{fieldCtx.fieldRef, fieldCtx.dsConfig.Hash()}
	if _, visited := c.visitedFieldsContextChecks[fieldKey]; !visited {
		if c.visitedFieldsContextChecks == nil {
			c.visitedFieldsContextChecks = make(map[fieldIndexKey]struct{})
		}
		c.visitedFieldsContextChecks[fieldKey] = struct{}{}

		if !c.planFieldContextArguments(fieldKey, arguments) {
			return
		}
	}

	// the parent can be selected on more data sources in later walks, so the boundary is checked until it is added
	if _, added := c.contextKeyBoundaries[fieldKey]; !added && c.hasContextCandidate(fieldKey) {
		c.addContextKeyBoundary(fieldCtx, fieldKey)
	}
}

func (c *nodeSelectionVisitor) planFieldContextArguments(fieldKey fieldIndexKey, arguments []ContextualArgumentConfiguration) bool {
	levels, ok := c.contextPathLevels()
	if !ok {
		return false
	}

	requirements := make([]contextualArgumentRequirement, 0, len(arguments))
	for i := range arguments {
		candidateLevels, ok := c.contextCandidateLevels(fieldKey, levels, arguments[i])
		if !ok {
			return false
		}
		requirements = append(requirements, contextualArgumentRequirement{
			argumentName: arguments[i].ArgumentName,
			levels:       candidateLevels,
		})
	}

	if c.fieldContextRequirements == nil {
		c.fieldContextRequirements = make(map[fieldIndexKey][]contextualArgumentRequirement)
	}
	c.fieldContextRequirements[fieldKey] = requirements
	return true
}

func (c *nodeSelectionVisitor) hasContextCandidate(fieldKey fieldIndexKey) bool {
	return slices.ContainsFunc(c.fieldContextRequirements[fieldKey], func(requirement contextualArgumentRequirement) bool {
		return len(requirement.levels) > 0
	})
}

// contextPathLevels derives the levels from the walker ancestors; in the selection set pre-pass
// the last ancestor owns the selection set of the consumer.
func (c *nodeSelectionVisitor) contextPathLevels() (levels []contextPathLevel, ok bool) {
	ancestors := c.walker.Ancestors
	if len(ancestors) == 0 || ancestors[0].Kind != ast.NodeKindOperationDefinition {
		return nil, false
	}

	typeName, ok := c.rootOperationTypeName(c.operation.OperationDefinitions[ancestors[0].Ref].OperationType)
	if !ok {
		return nil, false
	}

	var (
		targetSetRef  = ast.InvalidRef
		levelSetRef   = ast.InvalidRef
		ownerFieldRef = ast.InvalidRef
		ownerTypeName string
	)

	for _, ancestor := range ancestors[1:] {
		switch ancestor.Kind {
		case ast.NodeKindSelectionSet:
			targetSetRef = ancestor.Ref
			if levelSetRef == ast.InvalidRef {
				levelSetRef = ancestor.Ref
			}
		case ast.NodeKindInlineFragment:
			if c.operation.InlineFragmentHasTypeCondition(ancestor.Ref) {
				typeName = c.operation.InlineFragmentTypeConditionNameString(ancestor.Ref)
			}
		case ast.NodeKindField:
			levels = append(levels, contextPathLevel{
				depth:         len(levels),
				typeName:      typeName,
				targetSetRef:  targetSetRef,
				levelSetRef:   levelSetRef,
				ownerFieldRef: ownerFieldRef,
				ownerTypeName: ownerTypeName,
			})

			enclosingNode, exists := c.definition.NodeByNameStr(typeName)
			if !exists {
				return nil, false
			}
			fieldTypeNode, exists := c.definition.FieldTypeNode(c.operation.FieldNameBytes(ancestor.Ref), enclosingNode)
			if !exists {
				return nil, false
			}

			ownerFieldRef, ownerTypeName = ancestor.Ref, typeName
			typeName = c.definition.NodeNameString(fieldTypeNode)
			levelSetRef = ast.InvalidRef
		}
	}

	return levels, true
}

func (c *nodeSelectionVisitor) rootOperationTypeName(operationType ast.OperationType) (string, bool) {
	var typeName ast.ByteSlice
	switch operationType {
	case ast.OperationTypeQuery:
		typeName = c.definition.Index.QueryTypeName
	case ast.OperationTypeMutation:
		typeName = c.definition.Index.MutationTypeName
	case ast.OperationTypeSubscription:
		typeName = c.definition.Index.SubscriptionTypeName
	}
	return string(typeName), len(typeName) > 0
}

// contextCandidateLevels collects the levels that can set the context, nearest first, and queues their injections.
// Collection stops at the first static level, because it matches every object.
func (c *nodeSelectionVisitor) contextCandidateLevels(consumer fieldIndexKey, levels []contextPathLevel, argument ContextualArgumentConfiguration) (candidates []resolve.ContextualArgumentLevel, ok bool) {
	for _, level := range slices.Backward(levels) {
		possibleTypes, abstract := c.contextLevelPossibleTypes(level)
		setterCount := 0
		for _, setter := range argument.Setters {
			if slices.Contains(possibleTypes, setter.TypeName) {
				setterCount++
			}
		}
		if setterCount == 0 {
			continue
		}

		if abstract {
			c.queueContextLevelTypename(level)
		}

		setters := make([]resolve.ContextualArgumentSetter, 0, setterCount)
		for _, setter := range argument.Setters {
			if !slices.Contains(possibleTypes, setter.TypeName) {
				continue
			}

			var path []string
			if setter.SelectionSet != "" {
				if path, ok = c.contextSelectionPath(consumer, level, abstract, setter); !ok {
					return nil, false
				}
			}

			setters = append(setters, resolve.ContextualArgumentSetter{TypeName: setter.TypeName, Path: path})
		}

		candidates = append(candidates, resolve.ContextualArgumentLevel{
			Depth:           level.depth,
			MatchByTypename: abstract,
			Setters:         setters,
		})

		if !abstract {
			break
		}
	}

	return candidates, true
}

// contextLevelPossibleTypes returns the object types that can occur at the level, taken from the upstream schema
// of every data source the owner field is selected on.
func (c *nodeSelectionVisitor) contextLevelPossibleTypes(level contextPathLevel) (typeNames []string, abstract bool) {
	node, ok := c.definition.NodeByNameStr(level.typeName)
	if !ok {
		return nil, false
	}
	abstract = node.Kind == ast.NodeKindInterfaceTypeDefinition || node.Kind == ast.NodeKindUnionTypeDefinition

	if level.ownerFieldRef == ast.InvalidRef {
		return []string{level.typeName}, false
	}

	levelTypeNames := c.definitionObjectTypeNames(node)

	for i, dsHash := range c.selectedDataSourceHashes(level.ownerFieldRef) {
		dsIdx := slices.IndexFunc(c.dataSources, func(d DataSource) bool {
			return d.Hash() == dsHash
		})
		if dsIdx == -1 {
			return nil, abstract
		}

		dsTypeNames := slices.DeleteFunc(c.ownerFieldTypeNames(c.dataSources[dsIdx], level), func(typeName string) bool {
			return !slices.Contains(levelTypeNames, typeName)
		})

		if i == 0 {
			typeNames = dsTypeNames
			continue
		}
		typeNames = slices.DeleteFunc(typeNames, func(typeName string) bool {
			return !slices.Contains(dsTypeNames, typeName)
		})
	}

	return typeNames, abstract
}

// ownerFieldTypeNames returns the object types the owner field can return from the data source,
// with the lookups the abstract selection rewriter uses.
func (c *nodeSelectionVisitor) ownerFieldTypeNames(ds DataSource, level contextPathLevel) []string {
	enclosingNode, ok := c.definition.NodeByNameStr(level.ownerTypeName)
	if !ok {
		return nil
	}
	fieldTypeNode, ok := c.definition.FieldTypeNode(c.operation.FieldNameBytes(level.ownerFieldRef), enclosingNode)
	if !ok {
		return nil
	}

	if fieldTypeNode.Kind != ast.NodeKindInterfaceTypeDefinition && fieldTypeNode.Kind != ast.NodeKindUnionTypeDefinition {
		return c.definitionObjectTypeNames(fieldTypeNode)
	}

	rewriter, err := newFieldSelectionRewriter(c.operation, c.definition, ds)
	if err != nil {
		return c.definitionObjectTypeNames(fieldTypeNode)
	}

	var typeNames []string
	if fieldTypeNode.Kind == ast.NodeKindInterfaceTypeDefinition {
		_, typeNames, _, err = rewriter.getAllowedInterfaceMemberTypeNames(level.ownerFieldRef, fieldTypeNode.Ref, []byte(level.ownerTypeName))
	} else {
		typeNames, err = rewriter.getAllowedUnionMemberTypeNames(level.ownerFieldRef, fieldTypeNode.Ref, []byte(level.ownerTypeName))
	}
	if err != nil {
		return nil
	}

	return slices.Clone(typeNames)
}

func (c *nodeSelectionVisitor) definitionObjectTypeNames(node ast.Node) []string {
	var typeNames []string
	switch node.Kind {
	case ast.NodeKindObjectTypeDefinition:
		return []string{c.definition.ObjectTypeDefinitionNameString(node.Ref)}
	case ast.NodeKindInterfaceTypeDefinition:
		typeNames, _ = c.definition.InterfaceTypeDefinitionImplementedByObjectWithNames(node.Ref)
	case ast.NodeKindUnionTypeDefinition:
		typeNames, _ = c.definition.UnionTypeDefinitionMemberTypeNames(node.Ref)
	}
	return slices.Clone(typeNames)
}

func (c *nodeSelectionVisitor) selectedDataSourceHashes(fieldRef int) (out []DSHash) {
	treeNode, ok := c.nodeSuggestions.responseTree.Find(TreeNodeID(fieldRef))
	if !ok {
		return nil
	}

	for _, itemID := range treeNode.GetData() {
		item := c.nodeSuggestions.items[itemID]
		if item.Selected && !item.IsOrphan && !slices.Contains(out, item.DataSourceHash) {
			out = append(out, item.DataSourceHash)
		}
	}

	return out
}

// contextSelectionPath returns the response keys from a setter object to the context value.
// The selection is reused when the target already holds it under a context alias, otherwise its injection is queued.
func (c *nodeSelectionVisitor) contextSelectionPath(consumer fieldIndexKey, level contextPathLevel, abstract bool, setter ContextSetterConfiguration) (path []string, ok bool) {
	key, report := RequiredFieldsFragment(setter.TypeName, setter.SelectionSet, false)
	if report.HasErrors() {
		c.walker.StopWithInternalErr(report)
		return nil, false
	}

	keyFieldRef, nestedPath, ok := contextSelectionChain(key, key.FragmentDefinitions[0].SelectionSet)
	if !ok {
		c.walker.StopWithInternalErr(fmt.Errorf("the context selection %q of the setter type %s does not resolve to a single field", setter.SelectionSet, setter.TypeName))
		return nil, false
	}

	if fieldRef, found := c.findContextField(level.targetSetRef, abstract, setter.TypeName, key, keyFieldRef); found {
		c.addContextDependencies(consumer, c.contextFieldChainRefs(fieldRef, nestedPath))
		return append([]string{c.operation.FieldAliasString(fieldRef)}, nestedPath...), true
	}

	pending := c.pendingContextInjections[level.targetSetRef]
	for i := range pending {
		if pending[i].alias != nil && pending[i].setterType == setter.TypeName && pending[i].selection == setter.SelectionSet && pending[i].abstract == abstract {
			pending[i].requestedBy = append(pending[i].requestedBy, consumer)
			return append([]string{string(pending[i].alias)}, nestedPath...), true
		}
	}

	alias := c.nextContextAlias(level.levelSetRef)
	fieldSet := setter.SelectionSet
	typeName := setter.TypeName
	if abstract {
		fieldSet = "... on " + setter.TypeName + " {" + setter.SelectionSet + "}"
		typeName = level.typeName
	}

	c.queueContextInjection(level.targetSetRef, pendingContextInjection{
		typeName:      typeName,
		fieldSet:      fieldSet,
		setterType:    setter.TypeName,
		selection:     setter.SelectionSet,
		alias:         alias,
		abstract:      abstract,
		ownerFieldRef: level.ownerFieldRef,
		deferInfo:     c.ownerDeferInfo(level.ownerFieldRef),
		requestedBy:   []fieldIndexKey{consumer},
	})

	return append([]string{string(alias)}, nestedPath...), true
}

func contextSelectionChain(key *ast.Document, selectionSetRef int) (fieldRef int, nestedPath []string, ok bool) {
	fieldRef, ok = contextSelectionField(key, selectionSetRef)
	if !ok {
		return ast.InvalidRef, nil, false
	}

	for current := fieldRef; key.FieldHasSelections(current); {
		next, ok := contextSelectionField(key, key.Fields[current].SelectionSet)
		if !ok {
			return ast.InvalidRef, nil, false
		}
		nestedPath = append(nestedPath, key.FieldNameString(next))
		current = next
	}

	return fieldRef, nestedPath, true
}

// contextSelectionField returns the single field of a context selection set, looking through inline fragments.
func contextSelectionField(document *ast.Document, selectionSetRef int) (fieldRef int, ok bool) {
	fieldRef = ast.InvalidRef
	for _, selectionRef := range document.SelectionSets[selectionSetRef].SelectionRefs {
		selection := document.Selections[selectionRef]
		var candidate int
		switch selection.Kind {
		case ast.SelectionKindField:
			candidate = selection.Ref
		case ast.SelectionKindInlineFragment:
			fragmentSetRef, hasSelections := document.InlineFragmentSelectionSet(selection.Ref)
			if !hasSelections {
				continue
			}
			if candidate, ok = contextSelectionField(document, fragmentSetRef); !ok {
				return ast.InvalidRef, false
			}
		default:
			return ast.InvalidRef, false
		}
		if fieldRef != ast.InvalidRef {
			return ast.InvalidRef, false
		}
		fieldRef = candidate
	}
	return fieldRef, fieldRef != ast.InvalidRef
}

// findContextField searches the target selection set and, for an abstract level, its inline fragments
// on the setter type for a context aliased field equal to the selection.
func (c *nodeSelectionVisitor) findContextField(selectionSetRef int, abstract bool, setterType string, key *ast.Document, keyFieldRef int) (fieldRef int, ok bool) {
	if fieldRef, ok = c.findContextFieldInSelectionSet(selectionSetRef, key, keyFieldRef); ok || !abstract {
		return fieldRef, ok
	}

	for _, selectionRef := range c.operation.SelectionSets[selectionSetRef].SelectionRefs {
		selection := c.operation.Selections[selectionRef]
		if selection.Kind != ast.SelectionKindInlineFragment || c.operation.InlineFragmentTypeConditionNameString(selection.Ref) != setterType {
			continue
		}
		fragmentSetRef, hasSelections := c.operation.InlineFragmentSelectionSet(selection.Ref)
		if !hasSelections {
			continue
		}
		if fieldRef, ok = c.findContextFieldInSelectionSet(fragmentSetRef, key, keyFieldRef); ok {
			return fieldRef, true
		}
	}

	return ast.InvalidRef, false
}

func (c *nodeSelectionVisitor) findContextFieldInSelectionSet(selectionSetRef int, key *ast.Document, keyFieldRef int) (fieldRef int, ok bool) {
	for _, selectionRef := range c.operation.SelectionSets[selectionSetRef].SelectionRefs {
		selection := c.operation.Selections[selectionRef]
		if selection.Kind != ast.SelectionKindField || !c.operation.FieldAliasIsDefined(selection.Ref) {
			continue
		}
		if !bytes.HasPrefix(c.operation.FieldAliasBytes(selection.Ref), []byte(contextAliasPrefix)) {
			continue
		}
		if c.contextFieldMatches(selection.Ref, key, keyFieldRef) {
			return selection.Ref, true
		}
	}
	return ast.InvalidRef, false
}

// contextFieldMatches compares an operation field with a field of a context selection by name, arguments
// and sub-selection, ignoring aliases, directives and __typename selections.
func (c *nodeSelectionVisitor) contextFieldMatches(fieldRef int, key *ast.Document, keyFieldRef int) bool {
	if !bytes.Equal(c.operation.FieldNameBytes(fieldRef), key.FieldNameBytes(keyFieldRef)) {
		return false
	}

	arguments, keyArguments := c.operation.FieldArguments(fieldRef), key.FieldArguments(keyFieldRef)
	if len(arguments) != len(keyArguments) {
		return false
	}
	for _, keyArgument := range keyArguments {
		argument, exists := c.operation.FieldArgument(fieldRef, key.ArgumentNameBytes(keyArgument))
		if !exists || !contextValuesEqual(c.operation, c.operation.ArgumentValue(argument), key, key.ArgumentValue(keyArgument)) {
			return false
		}
	}

	if !key.FieldHasSelections(keyFieldRef) {
		return !c.operation.FieldHasSelections(fieldRef)
	}
	if !c.operation.FieldHasSelections(fieldRef) {
		return false
	}

	keyChildRef, ok := contextSelectionField(key, key.Fields[keyFieldRef].SelectionSet)
	if !ok {
		return false
	}
	childRef, ok := c.contextSelectionFieldIgnoringTypename(c.operation.Fields[fieldRef].SelectionSet)
	return ok && c.contextFieldMatches(childRef, key, keyChildRef)
}

func (c *nodeSelectionVisitor) contextSelectionFieldIgnoringTypename(selectionSetRef int) (fieldRef int, ok bool) {
	fieldRef = ast.InvalidRef
	for _, selectionRef := range c.operation.SelectionSets[selectionSetRef].SelectionRefs {
		selection := c.operation.Selections[selectionRef]
		var candidate int
		switch selection.Kind {
		case ast.SelectionKindField:
			if bytes.Equal(c.operation.FieldNameBytes(selection.Ref), typeNameFieldBytes) && !c.operation.FieldAliasIsDefined(selection.Ref) {
				continue
			}
			candidate = selection.Ref
		case ast.SelectionKindInlineFragment:
			fragmentSetRef, hasSelections := c.operation.InlineFragmentSelectionSet(selection.Ref)
			if !hasSelections {
				continue
			}
			if candidate, ok = c.contextSelectionFieldIgnoringTypename(fragmentSetRef); !ok {
				return ast.InvalidRef, false
			}
		default:
			return ast.InvalidRef, false
		}
		if fieldRef != ast.InvalidRef {
			return ast.InvalidRef, false
		}
		fieldRef = candidate
	}
	return fieldRef, fieldRef != ast.InvalidRef
}

func contextValuesEqual(left *ast.Document, leftValue ast.Value, right *ast.Document, rightValue ast.Value) bool {
	leftBuf, rightBuf := &bytes.Buffer{}, &bytes.Buffer{}
	if left.PrintValue(leftValue, leftBuf) != nil || right.PrintValue(rightValue, rightBuf) != nil {
		return false
	}
	return bytes.Equal(leftBuf.Bytes(), rightBuf.Bytes())
}

func (c *nodeSelectionVisitor) contextFieldChainRefs(fieldRef int, nestedPath []string) []int {
	refs := []int{fieldRef}
	current := fieldRef
	for _, name := range nestedPath {
		if !c.operation.FieldHasSelections(current) {
			break
		}
		next, ok := c.contextSelectionFieldIgnoringTypename(c.operation.Fields[current].SelectionSet)
		if !ok || c.operation.FieldNameString(next) != name {
			break
		}
		refs = append(refs, next)
		current = next
	}
	return refs
}

func (c *nodeSelectionVisitor) nextContextAlias(levelSetRef int) []byte {
	base := strconv.AppendInt([]byte(contextAliasPrefix), int64(c.contextAliasCounter), 10)
	c.contextAliasCounter++

	alias := base
	for i := 1; c.selectionSetHasResponseName(levelSetRef, alias); i++ {
		alias = strconv.AppendInt(append(slices.Clone(base), '_'), int64(i), 10)
	}
	return alias
}

// selectionSetHasResponseName checks the selection set and the inline fragments at the same response level.
func (c *nodeSelectionVisitor) selectionSetHasResponseName(selectionSetRef int, responseName []byte) bool {
	if exists, _ := c.operation.SelectionSetHasFieldSelectionWithNameOrAliasBytes(selectionSetRef, responseName); exists {
		return true
	}

	for _, selectionRef := range c.operation.SelectionSets[selectionSetRef].SelectionRefs {
		selection := c.operation.Selections[selectionRef]
		if selection.Kind != ast.SelectionKindInlineFragment {
			continue
		}
		fragmentSetRef, hasSelections := c.operation.InlineFragmentSelectionSet(selection.Ref)
		if hasSelections && c.selectionSetHasResponseName(fragmentSetRef, responseName) {
			return true
		}
	}

	return false
}

// queueContextLevelTypename makes sure an abstract level selects __typename in the owner's defer scope,
// which the loader uses to match setters.
func (c *nodeSelectionVisitor) queueContextLevelTypename(level contextPathLevel) {
	for _, pending := range c.pendingContextInjections[level.targetSetRef] {
		if pending.alias == nil {
			return
		}
	}

	c.queueContextInjection(level.targetSetRef, pendingContextInjection{
		typeName:      level.typeName,
		abstract:      true,
		ownerFieldRef: level.ownerFieldRef,
		deferInfo:     c.ownerDeferInfo(level.ownerFieldRef),
	})
}

func (c *nodeSelectionVisitor) queueContextInjection(selectionSetRef int, injection pendingContextInjection) {
	if c.pendingContextInjections == nil {
		c.pendingContextInjections = make(map[int][]pendingContextInjection)
	}
	c.pendingContextInjections[selectionSetRef] = append(c.pendingContextInjections[selectionSetRef], injection)
}

func (c *nodeSelectionVisitor) ownerDeferInfo(ownerFieldRef int) *DeferInfo {
	if ownerFieldRef == ast.InvalidRef {
		return nil
	}
	id, label, parentID, ok := c.operation.FieldDeferInfo(ownerFieldRef)
	if !ok {
		return nil
	}
	return &DeferInfo{ID: id, Label: label, ParentID: parentID}
}

// processPendingContextInjections runs when the selection set is left, because changing it while a descendant
// is walked restarts its iteration.
func (c *nodeSelectionVisitor) processPendingContextInjections(selectionSetRef int) {
	injections, ok := c.pendingContextInjections[selectionSetRef]
	if !ok {
		return
	}
	delete(c.pendingContextInjections, selectionSetRef)

	for _, injection := range injections {
		result, report := addRequiredFields(&addRequiredFieldsConfiguration{
			operation:                c.operation,
			definition:               c.definition,
			operationSelectionSetRef: selectionSetRef,
			allowTypename:            injection.alias == nil,
			typeName:                 injection.typeName,
			fieldSet:                 injection.fieldSet,
			deferInfo:                injection.deferInfo,
			isContextInjection:       true,
			contextAlias:             injection.alias,
		})
		if report.HasErrors() {
			c.walker.StopWithInternalErr(fmt.Errorf("failed to add the context selection %s on %s: %w", injection.fieldSet, injection.typeName, report))
			return
		}

		if len(result.skipFieldRefs) == 0 {
			continue
		}

		c.addNewSkipFieldRefs(result.skipFieldRefs...)
		for _, consumer := range injection.requestedBy {
			c.addContextDependencies(consumer, result.requiredFieldRefs)
		}

		if injection.ownerFieldRef != ast.InvalidRef && c.fieldSelectedOnFlatteningDataSource(injection.ownerFieldRef) {
			c.resetVisitedAbstractChecksForModifiedFields([]int{injection.ownerFieldRef})
		}

		c.hasNewFields = true
	}
}

func (c *nodeSelectionVisitor) fieldSelectedOnFlatteningDataSource(fieldRef int) bool {
	for _, dsHash := range c.selectedDataSourceHashes(fieldRef) {
		dsIdx := slices.IndexFunc(c.dataSources, func(d DataSource) bool {
			return d.Hash() == dsHash
		})
		if dsIdx != -1 && c.dataSources[dsIdx].PlanningBehavior().AlwaysFlattenFragments {
			return true
		}
	}
	return false
}

func (c *nodeSelectionVisitor) addContextDependencies(consumer fieldIndexKey, fieldRefs []int) {
	for _, fieldRef := range fieldRefs {
		if !slices.Contains(c.fieldDependsOn[consumer], fieldRef) {
			c.fieldDependsOn[consumer] = append(c.fieldDependsOn[consumer], fieldRef)
		}
		if !slices.Contains(c.fieldRefDependsOn[consumer.fieldRef], fieldRef) {
			c.fieldRefDependsOn[consumer.fieldRef] = append(c.fieldRefDependsOn[consumer.fieldRef], fieldRef)
		}
		c.fieldDependencyKind[fieldDependencyKey{field: consumer.fieldRef, dependsOn: fieldRef}] = fieldDependencyKindContext
	}
}

// addContextKeyBoundary forces an entity fetch for a consumer whose parent is selected on its own data source.
// The ordinary key jump covers a parent selected only on other data sources.
func (c *nodeSelectionVisitor) addContextKeyBoundary(fieldCtx fieldRequirementsContext, fieldKey fieldIndexKey) {
	dsHash := fieldCtx.dsConfig.Hash()
	if !slices.Contains(c.getSelectedParentsDSHashes(fieldCtx.fieldRef), dsHash) {
		return
	}

	key, ok := contextBoundaryKey(fieldCtx.dsConfig, fieldCtx.typeName)
	if !ok {
		return
	}

	if c.contextKeyBoundaries == nil {
		c.contextKeyBoundaries = make(map[fieldIndexKey]struct{})
	}
	c.contextKeyBoundaries[fieldKey] = struct{}{}

	c.addPendingKeyRequirements(fieldCtx, SourceConnection{
		Type: SourceConnectionTypeDirect,
		Jumps: []KeyJump{
			{
				From:         dsHash,
				To:           dsHash,
				SelectionSet: key.SelectionSet,
				TypeName:     fieldCtx.typeName,
			},
		},
	}, false)
}

// contextBoundaryKey returns the first resolvable key whose fields the data source resolves itself.
func contextBoundaryKey(ds DataSource, typeName string) (FederationFieldConfiguration, bool) {
	for _, key := range ds.RequiredFieldsByKey(typeName) {
		if err := key.parseSelectionSet(); err != nil {
			continue
		}
		keyDocument := key.parsedSelectionSet

		hasExternalField := false
		for _, fieldSelectionRef := range keyDocument.SelectionSetFieldSelections(keyDocument.FragmentDefinitions[0].SelectionSet) {
			fieldName := keyDocument.FieldNameString(keyDocument.Selections[fieldSelectionRef].Ref)
			if ds.HasExternalRootNode(typeName, fieldName) || ds.HasExternalChildNode(typeName, fieldName) {
				hasExternalField = true
				break
			}
		}

		if !hasExternalField {
			return key, true
		}
	}

	return FederationFieldConfiguration{}, false
}

// pruneContextRequirements drops the context requirements of consumers no longer selected on their data source.
func (c *nodeSelectionVisitor) pruneContextRequirements(isSelected func(fieldRef int, dsHash DSHash) bool) {
	for fieldKey := range c.fieldContextRequirements {
		if isSelected(fieldKey.fieldRef, fieldKey.dsHash) {
			continue
		}
		delete(c.fieldContextRequirements, fieldKey)
		delete(c.visitedFieldsContextChecks, fieldKey)
		delete(c.contextKeyBoundaries, fieldKey)
	}
}
