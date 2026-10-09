package postprocess

import (
	"slices"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/engine/resolve"
)

// deduplicateSingleFetches is a post-processing step that removes duplicate single fetches
// from the initial fetch tree. It merges their fetch paths and updates dependencies accordingly.
// NOTE: initial tree structure should be flat and contain a single root item with all fetches as children.
type deduplicateSingleFetches struct {
	disable bool
}

func (d *deduplicateSingleFetches) ProcessFetchTree(root *resolve.FetchTreeNode) {
	if d.disable {
		return
	}
	for i := range root.ChildNodes {
		for j := i + 1; j < len(root.ChildNodes); j++ {
			if root.ChildNodes[i].Item.EqualSingleFetch(root.ChildNodes[j].Item) {
				root.ChildNodes[i].Item.FetchPath = d.mergeFetchPath(root.ChildNodes[i].Item.FetchPath, root.ChildNodes[j].Item.FetchPath)

				newId := root.ChildNodes[i].FetchID()
				oldId := root.ChildNodes[j].FetchID()

				// The duplicate can have other parents, for example below sibling fragments whose fields
				// come from different fetches, so the kept fetch has to wait for them as well.
				d.mergeDependencies(root.ChildNodes[i].Item.Fetch, root.ChildNodes[j].Item.Fetch)

				root.ChildNodes = append(root.ChildNodes[:j], root.ChildNodes[j+1:]...)
				j--

				// when we merge duplicated fetches, we need to update the dependencies of the other fetches
				// because they might depend on the fetch that we are removing
				replaceDependsOnFetchID(root, oldId, newId)
			}
		}
	}
}

// mergeDependencies adds the fetch dependencies and coordinate dependencies of a removed duplicate to the kept fetch.
func (d *deduplicateSingleFetches) mergeDependencies(kept, removed resolve.Fetch) {
	keptDependencies := kept.Dependencies()
	for _, id := range removed.Dependencies().DependsOnFetchIDs {
		if id != keptDependencies.FetchID && !slices.Contains(keptDependencies.DependsOnFetchIDs, id) {
			keptDependencies.DependsOnFetchIDs = append(keptDependencies.DependsOnFetchIDs, id)
		}
	}

	keptInfo, removedInfo := kept.FetchInfo(), removed.FetchInfo()
	if keptInfo == nil || removedInfo == nil {
		return
	}
	for _, dependency := range removedInfo.CoordinateDependencies {
		i := slices.IndexFunc(keptInfo.CoordinateDependencies, func(existing resolve.FetchDependency) bool {
			return existing.Coordinate == dependency.Coordinate
		})
		if i == -1 {
			keptInfo.CoordinateDependencies = append(keptInfo.CoordinateDependencies, dependency)
			continue
		}
		for _, origin := range dependency.DependsOn {
			if !slices.Contains(keptInfo.CoordinateDependencies[i].DependsOn, origin) {
				keptInfo.CoordinateDependencies[i].DependsOn = append(keptInfo.CoordinateDependencies[i].DependsOn, origin)
			}
		}
	}
}

// replaceDependsOnFetchID replaces all occurrences of oldId with newId in the
// dependencies of the fetch tree. It walks the organized tree (Sequence and
// Parallel nesting), not just the flat root children: createMultiFetch now runs
// after organizeFetchTree, so dependents that must be rewired may live inside a
// nested Sequence/Parallel node. On a flat tree (the dedupe caller) this reduces
// to iterating the root's Single children exactly as before.
func replaceDependsOnFetchID(root *resolve.FetchTreeNode, oldId, newId int) {
	for i := range root.ChildNodes {
		node := root.ChildNodes[i]
		switch node.Kind {
		case resolve.FetchTreeNodeKindSequence, resolve.FetchTreeNodeKindParallel:
			replaceDependsOnFetchID(node, oldId, newId)
			continue
		}

		replaced := false
		deps := node.Item.Fetch.Dependencies().DependsOnFetchIDs
		for j := range deps {
			if deps[j] == oldId {
				deps[j] = newId
				replaced = true
			}
		}

		if !replaced {
			continue
		}

		info := node.Item.Fetch.FetchInfo()
		if info == nil {
			continue
		}
		for j := range info.CoordinateDependencies {
			for k := range info.CoordinateDependencies[j].DependsOn {
				if info.CoordinateDependencies[j].DependsOn[k].FetchID == oldId {
					info.CoordinateDependencies[j].DependsOn[k].FetchID = newId
				}
			}
		}
	}
}

// mergeFetchPath merges the fetch paths of two single fetches.
// The goal of this method is to merge typename conditions.
// When fetches originate from different parent fragments -
// they will have different typenames, but the same path in response.
func (d *deduplicateSingleFetches) mergeFetchPath(left, right []resolve.FetchItemPathElement) []resolve.FetchItemPathElement {
	for i := range left {
		left[i].TypeNames = d.mergeTypeNames(left[i].TypeNames, right[i].TypeNames)
	}

	return left
}

func (d *deduplicateSingleFetches) mergeTypeNames(left []string, right []string) []string {
	if len(left) == 0 || len(right) == 0 {
		return nil // if either side is empty, fetch is unscoped
	}

	out := append(left, right...)

	slices.Sort(out)
	return slices.Compact(out) // removes consecutive duplicates from the sorted slice
}
