package resolve

import (
	"bytes"
	"context"
	"fmt"
	"slices"

	"golang.org/x/sync/errgroup"

	"github.com/wundergraph/astjson"
	"github.com/wundergraph/go-arena"

	"github.com/wundergraph/graphql-go-tools/v2/pkg/internal/unsafebytes"
)

const (
	defaultMaxContextualArgumentGroups = 100
	// maxConcurrentContextualArgumentGroupLoads limits the group requests of one fetch that are in flight at once.
	maxConcurrentContextualArgumentGroupLoads = 16
)

const (
	contextualArgumentGroupLimitExceeded = "the entities need %d requests for distinct contextual argument values, which exceeds the limit of %d"
	contextualArgumentValueMissing       = "%d entities were not fetched because the context value of their '%s' ancestor is missing"
)

// fetchContextualArguments returns the contextual argument captures of an entity fetch.
func fetchContextualArguments(fetch Fetch) []ContextualArgumentCapture {
	switch f := fetch.(type) {
	case *BatchEntityFetch:
		return f.ContextualArguments
	case *EntityFetch:
		return f.ContextualArguments
	default:
		return nil
	}
}

func (l *Loader) contextualArgumentGroupLimit() int {
	if l.maxContextualArgumentGroups > 0 {
		return l.maxContextualArgumentGroups
	}
	return defaultMaxContextualArgumentGroups
}

// prepareContextualArgumentFetch prepares an entity fetch whose items take contextual argument values from their ancestors:
// one request per distinct tuple of values. It runs under the data lock.
func (l *Loader) prepareContextualArgumentFetch(fetchItem *FetchItem, captures []ContextualArgumentCapture) (*preparedFetch, error) {
	items, runs, missing := l.selectItemsWithContextualArguments(fetchItem.FetchPath, captures)
	res := &result{}
	prepared := &preparedFetch{item: fetchItem, items: items, res: res}
	switch fetch := fetchItem.Fetch.(type) {
	case *EntityFetch:
		res.init(fetch.PostProcessing, fetch.Info)
	case *BatchEntityFetch:
		res.init(fetch.PostProcessing, fetch.Info)
		prepared.batchFetch = true
	}
	res.tools = batchEntityToolPool.Get(len(items))

	groups, excluded := groupContextualArgumentRuns(res.tools, runs, missing, len(captures))
	if excluded.items != 0 {
		err := l.renderContextualArgumentError(fetchItem, res, fmt.Sprintf(contextualArgumentValueMissing, excluded.items, excluded.typeName))
		if err != nil {
			return prepared, err
		}
	}
	if limit := l.contextualArgumentGroupLimit(); len(groups) > limit {
		l.recordErroredFetchIDLocked(fetchItem)
		res.fetchSkipped = true
		prepared.skipLoad = true
		l.traceSkippedContextualArgumentFetch(fetchItem)
		return prepared, l.renderContextualArgumentError(fetchItem, res, fmt.Sprintf(contextualArgumentGroupLimitExceeded, len(groups), limit))
	}
	if len(groups) == 0 {
		res.fetchSkipped = true
		prepared.skipLoad = true
		l.traceSkippedContextualArgumentFetch(fetchItem)
		return prepared, nil
	}

	names := make([]string, len(captures))
	for i := range captures {
		names[i] = captures[i].VariableName
	}
	switch fetch := fetchItem.Fetch.(type) {
	case *EntityFetch:
		values := ContextualArgumentValues{Names: names, Values: groups[0].values}
		return prepared, l.prepareEntityFetch(fetchItem, fetch, items, &values, res, prepared)
	case *BatchEntityFetch:
		if l.ctx.TracingOptions.Enable {
			fetch.Traces = nil
		}
		if len(groups) == 1 {
			values := ContextualArgumentValues{Names: names, Values: groups[0].values}
			return prepared, l.prepareBatchEntityFetchGroup(fetchItem, fetch, items, groups[0].spans, &values, res, prepared)
		}
		return prepared, l.prepareContextualArgumentGroups(fetchItem, fetch, items, groups, names, res, prepared)
	}
	return prepared, nil
}

// preparedGroups holds the requests of a fetch that sends one per group of contextual argument values.
type preparedGroups struct {
	fetches []*preparedFetch
}

// prepareContextualArgumentGroups renders one request per group. Authorization and rate limiting run once for the
// fetch, on the first request, because a rejection applies to every group.
func (l *Loader) prepareContextualArgumentGroups(fetchItem *FetchItem, fetch *BatchEntityFetch, items []*astjson.Value, groups []contextualArgumentGroup, names []string, res *result, prepared *preparedFetch) error {
	tracing := l.ctx.TracingOptions.Enable
	var traces []*DataSourceLoadTrace
	requests := make([]*preparedFetch, 0, len(groups))
	validated := false
	for i := range groups {
		groupRes := &result{}
		groupRes.init(fetch.PostProcessing, fetch.Info)
		group := &preparedFetch{item: fetchItem, items: items, res: groupRes, batchFetch: true}
		if tracing {
			group.trace = l.contextualArgumentGroupTrace(items, groups[i].spans)
			traces = append(traces, group.trace)
		}

		res.tools.clearDedupState()
		values := ContextualArgumentValues{Names: names, Values: groups[i].values}
		input, err := l.renderBatchEntityInput(fetch, items, groups[i].spans, &values, res.tools, groupRes, group, group.trace)
		if err != nil {
			return err
		}
		if group.skipLoad {
			continue
		}
		if tracing && groupRes.fetchSkipped {
			l.setTracingInput(fetchItem, input, group.trace)
			continue
		}
		if !validated {
			validated = true
			allowed, err := l.validatePreFetch(input, fetch.Info, res)
			if err != nil {
				return err
			}
			if !allowed {
				prepared.skipLoad = true
				break
			}
		}
		group.source = fetch.DataSource
		group.input = input
		requests = append(requests, group)
	}
	if tracing {
		fetch.Trace = traces[0]
		fetch.Traces = traces
	}
	if prepared.skipLoad {
		return nil
	}
	if len(requests) == 0 {
		res.fetchSkipped = true
		prepared.skipLoad = true
		return nil
	}
	prepared.groups = &preparedGroups{fetches: requests}
	return nil
}

func (l *Loader) contextualArgumentGroupTrace(items []*astjson.Value, spans []batchItemSpan) *DataSourceLoadTrace {
	trace := &DataSourceLoadTrace{}
	if l.ctx.TracingOptions.ExcludeRawInputData {
		return trace
	}
	var groupItems []*astjson.Value
	for _, span := range spans {
		groupItems = append(groupItems, items[span.start:span.end]...)
	}
	if data := l.itemsData(groupItems); data != nil {
		trace.RawInputData, _ = l.compactJSON(data.MarshalTo(nil))
	}
	return trace
}

// traceSkippedContextualArgumentFetch records the trace of a contextual fetch that sends no request.
func (l *Loader) traceSkippedContextualArgumentFetch(fetchItem *FetchItem) {
	if !l.ctx.TracingOptions.Enable {
		return
	}
	trace := &DataSourceLoadTrace{LoadSkipped: true, Path: fetchItem.ResponsePath}
	switch fetch := fetchItem.Fetch.(type) {
	case *EntityFetch:
		fetch.Trace = trace
	case *BatchEntityFetch:
		fetch.Trace = trace
		fetch.Traces = nil
	}
}

// renderContextualArgumentError renders an error at the fetch's path that belongs to no single group request.
func (l *Loader) renderContextualArgumentError(fetchItem *FetchItem, res *result, reason string) error {
	return l.renderErrorsFailedToFetch(fetchItem, &result{ds: res.ds}, reason)
}

// loadContextualArgumentGroups sends the group requests of a fetch concurrently, outside the data lock.
func (l *Loader) loadContextualArgumentGroups(ctx context.Context, prepared *preparedFetch) error {
	groups := prepared.groups.fetches
	if len(groups) == 1 {
		return l.loadPhase(ctx, groups[0])
	}
	var g errgroup.Group
	g.SetLimit(maxConcurrentContextualArgumentGroupLoads)
	for _, group := range groups {
		g.Go(func() error {
			return l.loadPhase(ctx, group)
		})
	}
	return g.Wait()
}

// mergeContextualArgumentGroups merges the response of each group into its items, in group order, under the data lock.
func (l *Loader) mergeContextualArgumentGroups(prepared *preparedFetch) error {
	var firstErr error
	for _, group := range prepared.groups.fetches {
		if err := l.responseCacheCollect(group); err != nil {
			l.reportResponseCacheError(ResponseCacheOperationCollect, group.res.ds.Name, fmt.Errorf("response cache collect error: %w", err))
		}
		l.responseCacheMergeSurrogateKeys(group.res)
		// The flush after the merge writes the entries of every group in one call.
		prepared.responseCacheItems = append(prepared.responseCacheItems, group.responseCacheItems...)
		group.responseCacheItems = nil

		if err := l.mergeResult(prepared.item, group.res, prepared.items); err != nil && firstErr == nil {
			firstErr = err
		}
		l.callOnFinished(group.res)
	}
	return firstErr
}

// contextualArgumentRun is a run of consecutive items whose nearest setters set the same values.
type contextualArgumentRun struct {
	// end is the index after the last item of the run.
	end int
	// values holds the value of the nearest setter per capture: nil without a setter, which renders null, or a missing value marker.
	values []*astjson.Value
}

// contextualArgumentWalk is the state of selecting the items of one fetch together with their contextual argument values.
type contextualArgumentWalk struct {
	a        arena.Arena
	captures []ContextualArgumentCapture
	// missing holds one marker per setter type whose value is missing on some object; runs hold the marker in place of the value.
	missing []*astjson.Value
}

// selectItemsWithContextualArguments selects the items of path like selectItemsForPath and carries the values of their
// setters down the walk: children inherit the values of their parent and a deeper level overwrites them, so the nearest setter wins.
func (l *Loader) selectItemsWithContextualArguments(path []FetchItemPathElement, captures []ContextualArgumentCapture) ([]*astjson.Value, []contextualArgumentRun, []*astjson.Value) {
	walk := contextualArgumentWalk{a: l.jsonArena, captures: captures}
	items := arena.AllocateSlice[*astjson.Value](l.jsonArena, 1, 1)
	items[0] = l.dataBuffer.Get()
	runs := arena.AllocateSlice[contextualArgumentRun](l.jsonArena, 1, 1)
	runs[0] = contextualArgumentRun{end: 1, values: arena.AllocateSlice[*astjson.Value](l.jsonArena, len(captures), len(captures))}
	for depth := range path {
		if len(items) == 0 {
			break
		}
		items, runs = walk.step(items, runs, depth, path[depth])
	}
	items, runs = l.taintedObjs.filterOutTaintedRuns(items, runs)
	return items, runs, walk.missing
}

// step applies the levels at depth to the items and selects element on them.
func (w *contextualArgumentWalk) step(items []*astjson.Value, runs []contextualArgumentRun, depth int, element FetchItemPathElement) ([]*astjson.Value, []contextualArgumentRun) {
	capture := w.hasLevelAt(depth)
	if len(items) == 1 {
		values := runs[0].values
		if capture {
			values = w.capture(items[0], values, depth)
		}
		selected := selectItems(w.a, items, element)
		if len(selected) == 0 {
			return nil, nil
		}
		next := arena.AllocateSlice[contextualArgumentRun](w.a, 1, 1)
		next[0] = contextualArgumentRun{end: len(selected), values: values}
		return selected, next
	}

	// Without a level at depth every run carries over, so there are at most as many runs as before.
	runCapacity := len(runs)
	if capture {
		runCapacity = len(items)
	}
	selected := arena.AllocateSlice[*astjson.Value](w.a, 0, len(items))
	nextRuns := arena.AllocateSlice[contextualArgumentRun](w.a, 0, runCapacity)
	run := 0
	for i, item := range items {
		for i >= runs[run].end {
			run++
		}
		count := len(selected)
		selected = appendSelectedItems(w.a, selected, item, element)
		if len(selected) == count {
			continue
		}
		values := runs[run].values
		if capture {
			values = w.capture(item, values, depth)
		}
		if last := len(nextRuns) - 1; last >= 0 && slices.Equal(nextRuns[last].values, values) {
			nextRuns[last].end = len(selected)
			continue
		}
		nextRuns = arena.SliceAppend(w.a, nextRuns, contextualArgumentRun{end: len(selected), values: values})
	}
	return selected, nextRuns
}

// appendSelectedItems appends what element selects on item, the way selectItems does for each of several items.
func appendSelectedItems(a arena.Arena, selected []*astjson.Value, item *astjson.Value, element FetchItemPathElement) []*astjson.Value {
	if len(element.Path) == 0 {
		return arena.SliceAppend(a, selected, item)
	}
	if !isItemAllowedByTypename(item, element.TypeNames) {
		return selected
	}
	field := item.Get(element.Path...)
	if field == nil {
		return selected
	}
	if field.Type() != astjson.TypeArray {
		return arena.SliceAppend(a, selected, field)
	}
	arr := field.GetArray()
	if isArrayOfArrays(arr) {
		return appendFlattened(a, selected, arr)
	}
	return arena.SliceAppend(a, selected, arr...)
}

func (w *contextualArgumentWalk) hasLevelAt(depth int) bool {
	for i := range w.captures {
		for j := range w.captures[i].Levels {
			if w.captures[i].Levels[j].Depth == depth {
				return true
			}
		}
	}
	return false
}

// capture returns the values below item: values with the value of every capture whose level at depth matches item.
// values is shared with other items, so it is copied before the first change.
func (w *contextualArgumentWalk) capture(item *astjson.Value, values []*astjson.Value, depth int) []*astjson.Value {
	if item == nil || item.Type() != astjson.TypeObject {
		return values
	}
	captured, copied := values, false
	for i := range w.captures {
		levels := w.captures[i].Levels
		for j := range levels {
			if levels[j].Depth != depth {
				continue
			}
			setter := matchContextualArgumentSetter(&levels[j], item)
			if setter == nil {
				continue
			}
			value := w.setterValue(item, setter)
			if captured[i] == value {
				continue
			}
			if !copied {
				captured = arena.AllocateSlice[*astjson.Value](w.a, len(values), len(values))
				copy(captured, values)
				copied = true
			}
			captured[i] = value
		}
	}
	return captured
}

// matchContextualArgumentSetter returns the setter of level that item matches, or nil when item does not set the context.
func matchContextualArgumentSetter(level *ContextualArgumentLevel, item *astjson.Value) *ContextualArgumentSetter {
	if !level.MatchByTypename {
		if len(level.Setters) == 0 {
			return nil
		}
		return &level.Setters[0]
	}
	typeName := unsafebytes.BytesToString(item.GetStringBytes("__typename"))
	for i := range level.Setters {
		if level.Setters[i].TypeName == typeName {
			return &level.Setters[i]
		}
	}
	return nil
}

// setterValue returns the value a setter object sets, or the missing value marker of its type when a key of the path is absent.
func (w *contextualArgumentWalk) setterValue(item *astjson.Value, setter *ContextualArgumentSetter) *astjson.Value {
	if len(setter.Path) == 0 {
		return astjson.NullValue
	}
	if value, ok := projectContextualArgumentValue(w.a, item, setter.Path); ok {
		return value
	}
	return w.missingValue(setter.TypeName)
}

func (w *contextualArgumentWalk) missingValue(typeName string) *astjson.Value {
	for _, marker := range w.missing {
		if unsafebytes.BytesToString(marker.GetStringBytes()) == typeName {
			return marker
		}
	}
	marker := astjson.StringValue(w.a, typeName)
	w.missing = append(w.missing, marker)
	return marker
}

// projectContextualArgumentValue follows path from a setter object to the value and maps over lists element by element.
// It reports false when an object misses a key of the path.
func projectContextualArgumentValue(a arena.Arena, value *astjson.Value, path []string) (*astjson.Value, bool) {
	for i, key := range path {
		switch value.Type() {
		case astjson.TypeObject:
			if value = value.Get(key); value == nil {
				return nil, false
			}
		case astjson.TypeArray:
			list := astjson.ArrayValue(a)
			for j, element := range value.GetArray() {
				projected, ok := projectContextualArgumentValue(a, element, path[i:])
				if !ok {
					return nil, false
				}
				list.SetArrayItem(a, j, projected)
			}
			return list, true
		case astjson.TypeNull:
			return value, true
		default:
			return nil, false
		}
	}
	return value, true
}

// filterOutTaintedRuns is filterOutTainted for items that carry contextual argument runs.
func (t taintedObjects) filterOutTaintedRuns(items []*astjson.Value, runs []contextualArgumentRun) ([]*astjson.Value, []contextualArgumentRun) {
	if len(items) == 0 || len(t) == 0 {
		return items, runs
	}
	filtered := make([]*astjson.Value, 0, len(items))
	filteredRuns := make([]contextualArgumentRun, 0, len(runs))
	start := 0
	for _, run := range runs {
		count := len(filtered)
		for _, item := range items[start:run.end] {
			if !t.isTainted(item, 0) {
				filtered = append(filtered, item)
			}
		}
		start = run.end
		if len(filtered) > count {
			filteredRuns = append(filteredRuns, contextualArgumentRun{end: len(filtered), values: run.values})
		}
	}
	return filtered, filteredRuns
}

// contextualArgumentGroup holds the items of a fetch that share one tuple of contextual argument values.
type contextualArgumentGroup struct {
	// tuple renders the values as a JSON array; groups are told apart by these bytes.
	tuple []byte
	// values holds the rendered value of each capture, slices of tuple.
	values [][]byte
	spans  []batchItemSpan
}

// contextualArgumentExclusion counts the items whose nearest setter misses its value.
type contextualArgumentExclusion struct {
	items    int
	typeName string
}

// groupContextualArgumentRuns groups the runs by their rendered values on the arena of tools: the hash narrows, the bytes decide.
// Runs holding a missing value marker join no group.
func groupContextualArgumentRuns(tools *batchEntityTools, runs []contextualArgumentRun, missing []*astjson.Value, captures int) ([]contextualArgumentGroup, contextualArgumentExclusion) {
	var excluded contextualArgumentExclusion
	groups := arena.AllocateSlice[contextualArgumentGroup](tools.a, 0, 1)
	bounds := arena.AllocateSlice[int](tools.a, 2*captures, 2*captures)
	scratch := arena.AllocateSlice[byte](tools.a, 0, 64)
	start, previous := 0, -1
	for r := range runs {
		span := batchItemSpan{start: start, end: runs[r].end}
		start = runs[r].end
		values := runs[r].values
		if marker := missingContextualArgumentValue(values, missing); marker != nil {
			if excluded.items == 0 {
				excluded.typeName = string(marker.GetStringBytes())
			}
			excluded.items += span.end - span.start
			previous = -1
			continue
		}

		group := previous
		if group == -1 || !slices.Equal(runs[r-1].values, values) {
			scratch = appendContextualArgumentTuple(scratch[:0], values, bounds)
			tools.keyGen.Reset()
			_, _ = tools.keyGen.Write(scratch)
			hash := tools.keyGen.Sum64()
			if ref, ok := tools.batchHashToIndex[hash]; ok && bytes.Equal(groups[ref.index].tuple, scratch) {
				group = ref.index
			} else {
				tuple := arena.AllocateSlice[byte](tools.a, len(scratch), len(scratch))
				copy(tuple, scratch)
				groupValues := arena.AllocateSlice[[]byte](tools.a, captures, captures)
				for i := range groupValues {
					groupValues[i] = tuple[bounds[2*i]:bounds[2*i+1]]
				}
				group = len(groups)
				groups = arena.SliceAppend(tools.a, groups, contextualArgumentGroup{tuple: tuple, values: groupValues})
				tools.batchHashToIndex[hash] = batchItemRef{index: group}
			}
		}

		spans := groups[group].spans
		if last := len(spans) - 1; last >= 0 && spans[last].end == span.start {
			spans[last].end = span.end
		} else {
			spans = arena.SliceAppend(tools.a, spans, span)
		}
		groups[group].spans = spans
		previous = group
	}
	tools.clearDedupState()
	return groups, excluded
}

// missingContextualArgumentValue returns the missing value marker among values, or nil.
func missingContextualArgumentValue(values, missing []*astjson.Value) *astjson.Value {
	if len(missing) == 0 {
		return nil
	}
	for _, value := range values {
		if slices.Contains(missing, value) {
			return value
		}
	}
	return nil
}

// appendContextualArgumentTuple renders values as a JSON array and records where each value starts and ends in bounds.
func appendContextualArgumentTuple(dst []byte, values []*astjson.Value, bounds []int) []byte {
	dst = append(dst, '[')
	for i, value := range values {
		if i > 0 {
			dst = append(dst, ',')
		}
		bounds[2*i] = len(dst)
		if value == nil {
			dst = append(dst, null...)
		} else {
			dst = value.MarshalTo(dst)
		}
		bounds[2*i+1] = len(dst)
	}
	return append(dst, ']')
}
