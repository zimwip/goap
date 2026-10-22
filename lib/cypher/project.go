package cypher

import (
	"slices"
	"strings"
)

// Project computes RETURN over the rows a match bound (one scope per row): the columns, then the values of each row,
// grouped by the items that aggregate nothing when an item aggregates, made distinct, ordered, skipped and limited.
// vars are the variables of the patterns in order, the columns of `RETURN *`.
func Project(ret Return, vars []string, rows []MapScope) ([]string, [][]any, error) {
	items := ret.Items
	if ret.Star {
		star := make([]Item, 0, len(vars)+len(items))
		for _, v := range vars {
			star = append(star, Item{Expr: Var{Name: v}, Name: v})
		}
		items = append(star, items...)
	}
	if len(items) == 0 {
		return nil, nil, errorf("RETURN * with no variable to return")
	}
	cols := make([]string, len(items))
	aggregating := false
	for i, it := range items {
		cols[i] = it.Name
		if IsAggregate(it.Expr) {
			aggregating = true
			for _, a := range it.Expr.(Call).Args {
				if HasAggregate(a) {
					return nil, nil, errorf("an aggregate inside an aggregate: %s", it.Name)
				}
			}
		} else if HasAggregate(it.Expr) {
			return nil, nil, errorf("an aggregate inside an expression is not supported: %s", it.Name)
		}
	}
	type out struct {
		values []any
		scope  MapScope
	}
	var outs []out
	if !aggregating {
		for _, row := range rows {
			vals := make([]any, len(items))
			for i, it := range items {
				v, err := Eval(it.Expr, row)
				if err != nil {
					return nil, nil, err
				}
				vals[i] = v
			}
			outs = append(outs, out{vals, row})
		}
	} else {
		type group struct {
			values []any
			scope  MapScope
			args   [][]any
		}
		var order []string
		groups := map[string]*group{}
		for _, row := range rows {
			vals := make([]any, len(items))
			var key strings.Builder
			for i, it := range items {
				if IsAggregate(it.Expr) {
					continue
				}
				v, err := Eval(it.Expr, row)
				if err != nil {
					return nil, nil, err
				}
				vals[i] = v
				key.WriteString(Key(v) + "\x00")
			}
			g := groups[key.String()]
			if g == nil {
				g = &group{values: vals, scope: row, args: make([][]any, len(items))}
				groups[key.String()] = g
				order = append(order, key.String())
			}
			for i, it := range items {
				c, ok := it.Expr.(Call)
				if !ok || !IsAggregate(c) {
					continue
				}
				var v any
				if !c.Star {
					if len(c.Args) != 1 {
						return nil, nil, errorf("%s() takes 1 argument", c.Name)
					}
					var err error
					if v, err = Eval(c.Args[0], row); err != nil {
						return nil, nil, err
					}
				}
				g.args[i] = append(g.args[i], v)
			}
		}
		if len(rows) == 0 && allAggregates(items) { // `RETURN count(*)` over nothing is one row
			groups[""] = &group{values: make([]any, len(items)), scope: MapScope{}, args: make([][]any, len(items))}
			order = append(order, "")
		}
		for _, k := range order {
			g := groups[k]
			for i, it := range items {
				if c, ok := it.Expr.(Call); ok && IsAggregate(c) {
					v, err := Aggregate(c, g.args[i])
					if err != nil {
						return nil, nil, err
					}
					g.values[i] = v
				}
			}
			outs = append(outs, out{g.values, g.scope})
		}
	}
	if ret.Distinct {
		seen := map[string]bool{}
		kept := outs[:0]
		for _, o := range outs {
			k := Key(o.values)
			if !seen[k] {
				seen[k] = true
				kept = append(kept, o)
			}
		}
		outs = kept
	}
	if len(ret.Order) > 0 {
		keys := make([][]any, len(outs))
		for r, o := range outs {
			scope := MapScope{}
			for k, v := range o.scope {
				scope[k] = v
			}
			for i, it := range items {
				scope[it.Name] = o.values[i]
			}
			keys[r] = make([]any, len(ret.Order))
			for i, s := range ret.Order {
				v, err := Eval(s.Expr, scope)
				if err != nil {
					return nil, nil, err
				}
				keys[r][i] = v
			}
		}
		idx := make([]int, len(outs))
		for i := range idx {
			idx[i] = i
		}
		slices.SortStableFunc(idx, func(a, b int) int {
			for i, s := range ret.Order {
				c := Order(keys[a][i], keys[b][i])
				if s.Desc {
					c = -c
				}
				if c != 0 {
					return c
				}
			}
			return 0
		})
		sorted := make([]out, len(outs))
		for i, j := range idx {
			sorted[i] = outs[j]
		}
		outs = sorted
	}
	skip, limit, err := Bounds(ret)
	if err != nil {
		return nil, nil, err
	}
	skip = min(skip, len(outs))
	outs = outs[skip:]
	if limit >= 0 && limit < len(outs) {
		outs = outs[:limit]
	}
	values := make([][]any, len(outs))
	for i, o := range outs {
		values[i] = o.values
	}
	return cols, values, nil
}

func allAggregates(items []Item) bool {
	for _, it := range items {
		if !IsAggregate(it.Expr) {
			return false
		}
	}
	return true
}

// Bounds are the SKIP and LIMIT of RETURN (limit -1: none).
func Bounds(ret Return) (skip, limit int, err error) {
	count := func(e Expr, what string) (int, error) {
		v, err := Eval(e, MapScope{})
		if err != nil {
			return 0, err
		}
		f, ok := v.(float64)
		if !ok || f < 0 || f != float64(int(f)) {
			return 0, errorf("%s must be a non-negative integer", what)
		}
		return int(f), nil
	}
	limit = -1
	if ret.Skip != nil {
		if skip, err = count(ret.Skip, "SKIP"); err != nil {
			return 0, 0, err
		}
	}
	if ret.Limit != nil {
		if limit, err = count(ret.Limit, "LIMIT"); err != nil {
			return 0, 0, err
		}
	}
	return skip, limit, nil
}

// Streams reports a RETURN whose rows follow the matches one by one (no aggregate, DISTINCT or ORDER BY): the match can
// stop once it bound the rows the SKIP and LIMIT keep (rows -1: no limit).
func Streams(ret Return) (rows int, ok bool) {
	if ret.Distinct || len(ret.Order) > 0 {
		return 0, false
	}
	for _, it := range ret.Items {
		if HasAggregate(it.Expr) {
			return 0, false
		}
	}
	skip, limit, err := Bounds(ret)
	if err != nil || limit < 0 {
		return -1, true
	}
	return skip + limit, true
}
