//! RETURN over the rows a match bound (Go's `lib/cypher/project.go`).
use std::collections::HashMap;

use crate::ast::{Expr, Item, Return};
use crate::eval::{EvalError, MapScope, Value, aggregate, eval_now, key, order};

/// Computes RETURN over the rows a match bound (one scope per row): the columns, then the values of each row, grouped
/// by the items that aggregate nothing when an item aggregates, made distinct, ordered, skipped and limited. `vars`
/// are the variables of the patterns in order, the columns of `RETURN *`.
pub fn project(ret: &Return, vars: &[String], rows: &[MapScope]) -> Result<(Vec<String>, Vec<Vec<Value>>), EvalError> {
    let mut items: Vec<Item> = Vec::new();
    if ret.star {
        items.extend(vars.iter().map(|v| Item {
            expr: Expr::Var(v.clone()),
            name: v.clone(),
        }));
    }
    items.extend(ret.items.iter().cloned());
    if items.is_empty() {
        return Err(EvalError("RETURN * with no variable to return".into()));
    }
    let cols: Vec<String> = items.iter().map(|it| it.name.clone()).collect();
    let mut aggregating = false;
    for it in &items {
        if let Expr::Call(c) = &it.expr
            && it.expr.is_aggregate()
        {
            aggregating = true;
            if c.args.iter().any(Expr::has_aggregate) {
                return Err(EvalError(format!("an aggregate inside an aggregate: {}", it.name)));
            }
        } else if it.expr.has_aggregate() {
            return Err(EvalError(format!(
                "an aggregate inside an expression is not supported: {}",
                it.name
            )));
        }
    }
    // the values of each row and the scope it came from
    let mut outs: Vec<(Vec<Value>, MapScope)> = Vec::new();
    if !aggregating {
        for row in rows {
            let mut scope = row.clone();
            let mut vals = Vec::with_capacity(items.len());
            for it in &items {
                vals.push(eval_now(&it.expr, &mut scope)?);
            }
            outs.push((vals, row.clone()));
        }
    } else {
        struct Group {
            values: Vec<Value>,
            scope: MapScope,
            args: Vec<Vec<Value>>,
        }
        let mut order_of: Vec<String> = Vec::new();
        let mut groups: HashMap<String, Group> = HashMap::new();
        for row in rows {
            let mut scope = row.clone();
            let mut vals = vec![Value::Null; items.len()];
            let mut k = String::new();
            for (i, it) in items.iter().enumerate() {
                if it.expr.is_aggregate() {
                    continue;
                }
                let v = eval_now(&it.expr, &mut scope)?;
                k.push_str(&key(&v));
                k.push('\0');
                vals[i] = v;
            }
            if !groups.contains_key(&k) {
                order_of.push(k.clone());
                groups.insert(
                    k.clone(),
                    Group {
                        values: vals,
                        scope: row.clone(),
                        args: vec![Vec::new(); items.len()],
                    },
                );
            }
            for (i, it) in items.iter().enumerate() {
                let Expr::Call(c) = &it.expr else { continue };
                if !it.expr.is_aggregate() {
                    continue;
                }
                let mut v = Value::Null;
                if !c.star {
                    if c.args.len() != 1 {
                        return Err(EvalError(format!("{}() takes 1 argument", c.name)));
                    }
                    v = eval_now(&c.args[0], &mut scope)?;
                }
                if let Some(g) = groups.get_mut(&k) {
                    g.args[i].push(v);
                }
            }
        }
        // `RETURN count(*)` over nothing is one row
        if rows.is_empty() && items.iter().all(|it| it.expr.is_aggregate()) {
            order_of.push(String::new());
            groups.insert(
                String::new(),
                Group {
                    values: vec![Value::Null; items.len()],
                    scope: MapScope::new(),
                    args: vec![Vec::new(); items.len()],
                },
            );
        }
        for k in &order_of {
            let Some(mut g) = groups.remove(k) else { continue };
            for (i, it) in items.iter().enumerate() {
                if let Expr::Call(c) = &it.expr
                    && it.expr.is_aggregate()
                {
                    g.values[i] = aggregate(c, &g.args[i])?;
                }
            }
            outs.push((g.values, g.scope));
        }
    }
    if ret.distinct {
        let mut seen = std::collections::HashSet::new();
        outs.retain(|(vals, _)| seen.insert(key(&Value::List(vals.clone()))));
    }
    if !ret.order.is_empty() {
        let mut keys: Vec<Vec<Value>> = Vec::with_capacity(outs.len());
        for (vals, row) in &outs {
            let mut scope = row.clone();
            for (i, it) in items.iter().enumerate() {
                scope.insert(it.name.clone(), vals[i].clone());
            }
            let mut ks = Vec::with_capacity(ret.order.len());
            for s in &ret.order {
                ks.push(eval_now(&s.expr, &mut scope)?);
            }
            keys.push(ks);
        }
        let mut idx: Vec<usize> = (0..outs.len()).collect();
        idx.sort_by(|&a, &b| {
            for (i, s) in ret.order.iter().enumerate() {
                let mut c = order(&keys[a][i], &keys[b][i]);
                if s.desc {
                    c = c.reverse();
                }
                if c.is_ne() {
                    return c;
                }
            }
            std::cmp::Ordering::Equal
        });
        let mut taken: Vec<Option<(Vec<Value>, MapScope)>> = outs.into_iter().map(Some).collect();
        outs = idx.into_iter().filter_map(|i| taken[i].take()).collect();
    }
    let (skip, limit) = bounds(ret)?;
    let values: Vec<Vec<Value>> = outs
        .into_iter()
        .skip(skip)
        .take(limit.unwrap_or(usize::MAX))
        .map(|(v, _)| v)
        .collect();
    Ok((cols, values))
}

/// The SKIP and LIMIT of RETURN (limit None: none).
pub fn bounds(ret: &Return) -> Result<(usize, Option<usize>), EvalError> {
    let count = |e: &Expr, what: &str| -> Result<usize, EvalError> {
        let v = eval_now(e, &mut MapScope::new())?;
        match v {
            Value::Num(f) if f >= 0.0 && f == (f as i64) as f64 => Ok(f as usize),
            _ => Err(EvalError(format!("{what} must be a non-negative integer"))),
        }
    };
    let skip = match &ret.skip {
        Some(e) => count(e, "SKIP")?,
        None => 0,
    };
    let limit = match &ret.limit {
        Some(e) => Some(count(e, "LIMIT")?),
        None => None,
    };
    Ok((skip, limit))
}

/// The rows a RETURN keeps when its rows follow the matches one by one (no aggregate, DISTINCT or ORDER BY): the
/// match may stop once it bound them (`Some(None)`: no limit); None when the RETURN does not stream.
pub fn streams(ret: &Return) -> Option<Option<usize>> {
    if ret.distinct || !ret.order.is_empty() || ret.items.iter().any(|it| it.expr.has_aggregate()) {
        return None;
    }
    match bounds(ret) {
        Ok((skip, Some(limit))) => Some(Some(skip + limit)),
        _ => Some(None),
    }
}
