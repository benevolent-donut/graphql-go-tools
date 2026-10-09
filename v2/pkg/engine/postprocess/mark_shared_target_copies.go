package postprocess

import (
	"slices"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

// markSharedTargetCopies sets DeepCopyForSharedTargets on the batch entity fetches whose deduplicated merges could make
// positions below different contextual argument values share response nodes.
type markSharedTargetCopies struct{}

// contextualFetchPath is the fetch path of a contextual fetch and the depth of its outermost level below the root.
type contextualFetchPath struct {
	path     []resolve.FetchItemPathElement
	minDepth int
}

// Process visits the initial fetch tree and every defer group's tree together, because a deferred contextual fetch
// reads positions that fetches of the initial response merged into.
func (m *markSharedTargetCopies) Process(roots ...*resolve.FetchTreeNode) {
	var contextual []contextualFetchPath
	for _, root := range roots {
		visitFetchItems(root, func(item *resolve.FetchItem) {
			if minDepth, ok := contextualArgumentsMinDepth(item.Fetch); ok {
				contextual = append(contextual, contextualFetchPath{path: item.FetchPath, minDepth: minDepth})
			}
		})
	}
	if len(contextual) == 0 {
		return
	}
	for _, root := range roots {
		visitFetchItems(root, func(item *resolve.FetchItem) {
			switch fetch := item.Fetch.(type) {
			case *resolve.BatchEntityFetch:
				if needsSharedTargetCopies(item.FetchPath, contextual) {
					fetch.DeepCopyForSharedTargets = true
				}
			case *resolve.MultiEntityFetch:
				for i := range fetch.Input.Entries {
					entry := &fetch.Input.Entries[i]
					if needsSharedTargetCopies(entry.Item.FetchPath, contextual) {
						entry.DeepCopyForSharedTargets = true
					}
				}
			}
		})
	}
}

// contextualArgumentsMinDepth returns the depth of the outermost level of an entity fetch's captures.
// A level at depth 0 is the root data, which every item shares, so it never tells two positions apart.
func contextualArgumentsMinDepth(fetch resolve.Fetch) (int, bool) {
	var captures []resolve.ContextualArgumentCapture
	switch f := fetch.(type) {
	case *resolve.EntityFetch:
		captures = f.ContextualArguments
	case *resolve.BatchEntityFetch:
		captures = f.ContextualArguments
	}
	minDepth, ok := 0, false
	for i := range captures {
		for _, level := range captures[i].Levels {
			if level.Depth > 0 && (!ok || level.Depth < minDepth) {
				minDepth, ok = level.Depth, true
			}
		}
	}
	return minDepth, ok
}

// needsSharedTargetCopies reports whether a fetch at path merges into ancestors of a contextual fetch's items that lie
// at or below one of its levels.
func needsSharedTargetCopies(path []resolve.FetchItemPathElement, contextual []contextualFetchPath) bool {
	for _, c := range contextual {
		if len(path) >= c.minDepth && len(path) < len(c.path) && isFetchPathPrefix(path, c.path) {
			return true
		}
	}
	return false
}

func isFetchPathPrefix(prefix, path []resolve.FetchItemPathElement) bool {
	for i := range prefix {
		if !slices.Equal(prefix[i].Path, path[i].Path) {
			return false
		}
	}
	return true
}

func visitFetchItems(node *resolve.FetchTreeNode, visit func(item *resolve.FetchItem)) {
	if node == nil {
		return
	}
	switch node.Kind {
	case resolve.FetchTreeNodeKindSingle:
		if node.Item != nil && node.Item.Fetch != nil {
			visit(node.Item)
		}
	case resolve.FetchTreeNodeKindSequence, resolve.FetchTreeNodeKindParallel:
		for _, child := range node.ChildNodes {
			visitFetchItems(child, visit)
		}
	}
}
