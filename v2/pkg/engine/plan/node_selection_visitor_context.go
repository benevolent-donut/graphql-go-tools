package plan

import (
	"bytes"
	"fmt"
	"slices"
	"strconv"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/ast"
	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

const contextAliasPrefix = resolve.ContextualArgumentAliasPrefix

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
	alias         []byte
	abstract      bool
	isTypename    bool // the __typename of an abstract level, which has no alias
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
	if _, added := c.contextKeyBoundaries[fieldKey]; !added && hasContextCandidate(c.fieldContextRequirements[fieldKey]) {
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

func hasContextCandidate(requirements []contextualArgumentRequirement) bool {
	return slices.ContainsFunc(requirements, func(requirement contextualArgumentRequirement) bool {
		return len(requirement.levels) > 0
	})
}

// Runs in the pre-pass of the consumer's selection set, so the objects of the consumer's parent never form a level.
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

		// a static level matches every object, so the loader never reads a farther level
		if !abstract {
			break
		}
	}

	return candidates, true
}

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

	// only the types that every data source selecting the owner field defines can occur at the level
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

	if fieldRefs := c.findContextField(level.targetSetRef, abstract, setter.TypeName, key, keyFieldRef); len(fieldRefs) > 0 {
		c.addContextDependencies(consumer, fieldRefs)
		return append([]string{c.operation.FieldAliasString(fieldRefs[0])}, nestedPath...), true
	}

	pending := c.pendingContextInjections[level.targetSetRef]
	for i := range pending {
		if !pending[i].isTypename && pending[i].setterType == setter.TypeName && pending[i].selection == setter.SelectionSet && pending[i].abstract == abstract {
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

func (c *nodeSelectionVisitor) findContextField(selectionSetRef int, abstract bool, setterType string, key *ast.Document, keyFieldRef int) (fieldRefs []int) {
	if fieldRefs = c.findContextFieldInSelectionSet(selectionSetRef, key, keyFieldRef); fieldRefs != nil || !abstract {
		return fieldRefs
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
		if fieldRefs = c.findContextFieldInSelectionSet(fragmentSetRef, key, keyFieldRef); fieldRefs != nil {
			return fieldRefs
		}
	}

	return nil
}

func (c *nodeSelectionVisitor) findContextFieldInSelectionSet(selectionSetRef int, key *ast.Document, keyFieldRef int) []int {
	for _, selectionRef := range c.operation.SelectionSets[selectionSetRef].SelectionRefs {
		selection := c.operation.Selections[selectionRef]
		if selection.Kind != ast.SelectionKindField || !c.operation.FieldAliasIsDefined(selection.Ref) {
			continue
		}
		if !bytes.HasPrefix(c.operation.FieldAliasBytes(selection.Ref), []byte(contextAliasPrefix)) {
			continue
		}
		// a client field can use the same alias, for example in a deferred fragment, so only planner fields are reused
		if !slices.Contains(c.skipFieldsRefs, selection.Ref) {
			continue
		}
		if fieldRefs := c.matchContextField(selection.Ref, key, keyFieldRef); fieldRefs != nil {
			return fieldRefs
		}
	}
	return nil
}

// Fields the planner adds next to the selected fields, such as key fields for a jump to another subgraph, are ignored.
func (c *nodeSelectionVisitor) matchContextField(fieldRef int, key *ast.Document, keyFieldRef int) []int {
	if !bytes.Equal(c.operation.FieldNameBytes(fieldRef), key.FieldNameBytes(keyFieldRef)) || !c.contextArgumentsEqual(fieldRef, key, keyFieldRef) {
		return nil
	}

	if !key.FieldHasSelections(keyFieldRef) {
		if c.operation.FieldHasSelections(fieldRef) {
			return nil
		}
		return []int{fieldRef}
	}
	if !c.operation.FieldHasSelections(fieldRef) {
		return nil
	}

	keyChildRef, ok := contextSelectionField(key, key.Fields[keyFieldRef].SelectionSet)
	if !ok {
		return nil
	}
	nestedFieldRefs := c.matchNestedContextFields(c.operation.Fields[fieldRef].SelectionSet, key, keyChildRef)
	if nestedFieldRefs == nil {
		return nil
	}
	return append([]int{fieldRef}, nestedFieldRefs...)
}

// An abstract rewrite puts copies of a nested field into inline fragments, which match too;
// aliased fields never match, because the capture path reads field names.
func (c *nodeSelectionVisitor) matchNestedContextFields(selectionSetRef int, key *ast.Document, keyFieldRef int) (fieldRefs []int) {
	for _, selectionRef := range c.operation.SelectionSets[selectionSetRef].SelectionRefs {
		selection := c.operation.Selections[selectionRef]
		switch selection.Kind {
		case ast.SelectionKindField:
			if !c.operation.FieldAliasIsDefined(selection.Ref) {
				fieldRefs = append(fieldRefs, c.matchContextField(selection.Ref, key, keyFieldRef)...)
			}
		case ast.SelectionKindInlineFragment:
			if fragmentSetRef, hasSelections := c.operation.InlineFragmentSelectionSet(selection.Ref); hasSelections {
				fieldRefs = append(fieldRefs, c.matchNestedContextFields(fragmentSetRef, key, keyFieldRef)...)
			}
		}
	}
	return fieldRefs
}

func (c *nodeSelectionVisitor) contextArgumentsEqual(fieldRef int, key *ast.Document, keyFieldRef int) bool {
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
	return true
}

func contextValuesEqual(left *ast.Document, leftValue ast.Value, right *ast.Document, rightValue ast.Value) bool {
	leftBuf, rightBuf := &bytes.Buffer{}, &bytes.Buffer{}
	if left.PrintValue(leftValue, leftBuf) != nil || right.PrintValue(rightValue, rightBuf) != nil {
		return false
	}
	return bytes.Equal(leftBuf.Bytes(), rightBuf.Bytes())
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

// A name used in any fragment at the same response level would merge with the alias in the response.
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

// The loader matches the setters of an abstract level by __typename.
func (c *nodeSelectionVisitor) queueContextLevelTypename(level contextPathLevel) {
	for _, pending := range c.pendingContextInjections[level.targetSetRef] {
		if pending.isTypename {
			return
		}
	}

	c.queueContextInjection(level.targetSetRef, pendingContextInjection{
		typeName:      level.typeName,
		abstract:      true,
		isTypename:    true,
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
			allowTypename:            injection.isTypename,
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

func (c *nodeSelectionVisitor) addContextKeyBoundary(fieldCtx fieldRequirementsContext, fieldKey fieldIndexKey) {
	dsHash := fieldCtx.dsConfig.Hash()
	// a parent selected only on other data sources gets the ordinary key jump
	if !slices.Contains(c.getSelectedParentsDSHashes(fieldCtx.fieldRef), dsHash) {
		return
	}

	key, ok := c.contextBoundaryKey(fieldCtx.dsConfig, fieldCtx.typeName)
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

func (c *nodeSelectionVisitor) contextBoundaryKey(ds DataSource, typeName string) (FederationFieldConfiguration, bool) {
	for _, key := range ds.RequiredFieldsByKey(typeName) {
		if err := key.parseSelectionSet(); err != nil {
			continue
		}
		keyDocument := key.parsedSelectionSet

		if c.keySelectsOnlyLocalFields(ds, keyDocument, keyDocument.FragmentDefinitions[0].SelectionSet, typeName) {
			return key, true
		}
	}

	return FederationFieldConfiguration{}, false
}

func (c *nodeSelectionVisitor) keySelectsOnlyLocalFields(ds DataSource, key *ast.Document, selectionSetRef int, typeName string) bool {
	for _, fieldSelectionRef := range key.SelectionSetFieldSelections(selectionSetRef) {
		fieldRef := key.Selections[fieldSelectionRef].Ref
		fieldName := key.FieldNameString(fieldRef)
		if ds.HasExternalRootNode(typeName, fieldName) || ds.HasExternalChildNode(typeName, fieldName) {
			return false
		}
		if !key.FieldHasSelections(fieldRef) {
			continue
		}

		enclosingNode, ok := c.definition.NodeByNameStr(typeName)
		if !ok {
			return false
		}
		fieldTypeNode, ok := c.definition.FieldTypeNode(key.FieldNameBytes(fieldRef), enclosingNode)
		if !ok {
			return false
		}
		if !c.keySelectsOnlyLocalFields(ds, key, key.Fields[fieldRef].SelectionSet, c.definition.NodeNameString(fieldTypeNode)) {
			return false
		}
	}

	return true
}

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
