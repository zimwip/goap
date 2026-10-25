//! Casbin's enforcement of the model of `contracts/authz` (casbin v2.135.0): each policy line, in order, through the
//! matcher `(p.obj_type == "*" || r.obj.Type == p.obj_type) && (p.act == "*" || r.act == p.act) &&
//! eval(p.sub_rule)`, its rule read by `eval` once its `r.` and `p.` are escaped; the effect `some allow && !some
//! deny` stops at the first deny that matches. The functions are the model's (`hasRole`, `mayRun`…) and Casbin's own
//! (`keyMatch`…`keyMatch5`, `keyGet`…`keyGet3`, `regexMatch`, `ipMatch`, `globMatch`).
use std::net::IpAddr;
use std::sync::LazyLock;

use regex::Regex;

use crate::abac::{Policy, Resource, Subject};
use crate::goregexp::GoRegex;
use crate::govaluate::{Expression, Fault, Host, Value};

/// The matcher of the model, its `r.` and `p.` escaped as Casbin escapes them when it loads the model.
static MATCHER: LazyLock<String> = LazyLock::new(|| {
    escape_assertion(
        r#"(p.obj_type == "*" || r.obj.Type == p.obj_type) && (p.act == "*" || r.act == p.act) && eval(p.sub_rule)"#,
    )
});

static ASSERTION: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"([()\t\n\x0C\r |&,=!><+\-*/]|^)((r|p)[0-9]*)\.").expect("the assertion pattern"));

/// `util.EscapeAssertion`: `r.sub` read `r_sub`, `p.act` `p_act`.
fn escape_assertion(s: &str) -> String {
    ASSERTION
        .replace_all(s, |c: &regex::Captures| format!("{}{}_", &c[1], &c[2]))
        .into_owned()
}

const FUNCTIONS: [&str; 20] = [
    "eval",
    "hasRole",
    "hasAnyRole",
    "hasRoleIn",
    "onProject",
    "mayRun",
    "isProjectAdmin",
    "isProjectOwner",
    "isAnonymous",
    "keyMatch",
    "keyGet",
    "keyMatch2",
    "keyGet2",
    "keyMatch3",
    "keyGet3",
    "keyMatch4",
    "keyMatch5",
    "regexMatch",
    "ipMatch",
    "globMatch",
];

/// The parameters of one evaluation (`enforceParameters`): the request and the policy line.
struct Params<'a> {
    sub: &'a Subject,
    res: &'a Resource,
    act: &'a str,
    policy: &'a Policy,
}

impl Host for Params<'_> {
    fn is_function(&self, name: &str) -> bool {
        FUNCTIONS.contains(&name)
    }

    fn param(&self, name: &str) -> Result<Value, Fault> {
        let p = self.policy;
        Ok(match name {
            "" => Value::Nil,
            "p_sub_rule" => Value::Str(p.rule.clone()),
            "p_obj_type" => Value::Str(p.resource.clone()),
            "p_act" => Value::Str(p.action.clone()),
            "p_eft" => Value::Str(p.effect.clone()),
            "r_sub" => Value::Principal(Box::new(self.sub.clone())),
            "r_obj" => Value::Resource(Box::new(self.res.clone())),
            "r_act" => Value::Str(self.act.to_string()),
            _ => return Err(Fault::Error(format!("No parameter '{name}' found."))),
        })
    }

    fn call(&self, name: &str, args: Vec<Value>) -> Result<Value, Fault> {
        let principal = |i: usize| match args.get(i) {
            Some(Value::Principal(p)) => (**p).clone(),
            _ => Subject::default(),
        };
        let resource = |i: usize| match args.get(i) {
            Some(Value::Resource(r)) => (**r).clone(),
            _ => Resource::default(),
        };
        let text = |i: usize| match args.get(i) {
            Some(Value::Str(s)) => s.clone(),
            _ => String::new(),
        };
        let arity = |n: usize, usage: &str| {
            if args.len() == n {
                Ok(())
            } else {
                Err(Fault::Error(usage.to_string()))
            }
        };
        let first = || {
            if args.is_empty() {
                Err(Fault::Panic(
                    "runtime error: index out of range [0] with length 0".into(),
                ))
            } else {
                Ok(principal(0))
            }
        };
        Ok(Value::Bool(match name {
            "eval" => return self.eval(args),
            "hasRole" => {
                arity(2, "hasRole(sub, role)")?;
                principal(0).has_role(&text(1))
            }
            "hasAnyRole" => {
                if args.len() < 2 {
                    return Err(Fault::Error("hasAnyRole(sub, role...)".into()));
                }
                let p = principal(0);
                (1..args.len()).any(|i| p.has_role(&text(i)))
            }
            "hasRoleIn" => {
                arity(3, "hasRoleIn(sub, role, obj)")?;
                principal(0).has_role_in(&text(1), &resource(2))
            }
            "onProject" => {
                arity(1, "onProject(sub)")?;
                principal(0).on_project()
            }
            "mayRun" => {
                arity(2, "mayRun(sub, obj)")?;
                principal(0).may_run(&resource(1))
            }
            "isProjectAdmin" => {
                let p = first()?;
                p.project_admin || p.project_owner
            }
            "isProjectOwner" => first()?.project_owner,
            "isAnonymous" => first()?.anonymous(),
            _ => return builtin(name, &args),
        }))
    }
}

impl Params<'_> {
    /// `eval(subrule)`: the rule read with every function, `eval` itself among them.
    fn eval(&self, args: Vec<Value>) -> Result<Value, Fault> {
        if args.len() != 1 {
            return Err(Fault::Error(format!(
                "function eval(subrule string) expected 1 arguments, but got {}",
                args.len()
            )));
        }
        let Value::Str(rule) = &args[0] else {
            return Err(Fault::Error("argument of eval(subrule string) must be a string".into()));
        };
        let expr = escape_assertion(rule);
        let e = Expression::new(&expr, self).map_err(|f| match f {
            Fault::Error(e) => Fault::Error(format!("error while parsing eval parameter: {expr}, {e}")),
            p => p,
        })?;
        e.eval(self)
    }
}

/// `enforce` over the policy lines of the model, the error of the first one that fails.
pub fn enforce(rules: &[Policy], sub: &Subject, res: &Resource, act: &str) -> Result<bool, String> {
    let words = |f: Fault| match f {
        Fault::Error(e) => e,
        Fault::Panic(p) => format!("panic: {p}"),
    };
    let none = Policy::default();
    let host = Params {
        sub,
        res,
        act,
        policy: rules.first().unwrap_or(&none),
    };
    let matcher = Expression::new(&MATCHER, &host).map_err(words)?;
    if rules.is_empty() {
        return Err("please make sure rule exists in policy when using eval() in matcher".into());
    }
    let mut allowed = false;
    for p in rules {
        let host = Params { policy: p, ..host };
        let matched = match matcher.eval(&host).map_err(words)? {
            Value::Bool(b) => b,
            Value::Num(f) => f != 0.0,
            _ => return Err("matcher result should be bool, int or float".into()),
        };
        if matched && p.effect == "deny" {
            return Ok(false);
        }
        allowed |= matched && p.effect == "allow";
    }
    Ok(allowed)
}

// ---------------------------------------------------------------------------------------------------------------
// Casbin's own functions (`util/builtin_operators.go`)

fn builtin(name: &str, args: &[Value]) -> Result<Value, Fault> {
    let n = match name {
        "keyGet2" | "keyGet3" => 3,
        _ => 2,
    };
    let wrong = |e: String| Fault::Error(format!("{name}: {e}"));
    if args.len() != n {
        return Err(wrong(format!("expected {n} arguments, but got {}", args.len())));
    }
    let mut s = Vec::with_capacity(n);
    for a in args {
        let Value::Str(v) = a else {
            return Err(wrong("argument must be a string".into()));
        };
        s.push(v.as_str());
    }
    Ok(match name {
        "keyMatch" => Value::Bool(key_match(s[0], s[1])),
        "keyGet" => Value::Str(key_get(s[0], s[1])),
        "keyMatch2" => Value::Bool(regex_match(s[0], &format!("^{}$", key_pattern(s[1], &KEY2, "[^/]+")))?),
        "keyGet2" => Value::Str(key_get_var(s[0], s[1], s[2], &KEY2, "([^/]+)", |k| &k[1..])?),
        "keyMatch3" => Value::Bool(regex_match(s[0], &format!("^{}$", key_pattern(s[1], &KEY3, "[^/]+")))?),
        "keyGet3" => Value::Str(key_get_var(s[0], s[1], s[2], &KEY_GET3, "([^/]+?)", |k| {
            &k[1..k.len() - 1]
        })?),
        "keyMatch4" => Value::Bool(key_match4(s[0], s[1])?),
        "keyMatch5" => {
            let key1 = s[0].split_once('?').map_or(s[0], |(k, _)| k);
            Value::Bool(regex_match(key1, &format!("^{}$", key_pattern(s[1], &KEY3, "[^/]+")))?)
        }
        "regexMatch" => Value::Bool(regex_match(s[0], s[1])?),
        "ipMatch" => Value::Bool(ip_match(s[0], s[1])?),
        "globMatch" => Value::Bool(glob_match(s[1].as_bytes(), s[0].as_bytes())?),
        _ => return Err(Fault::Error(format!("No function {name}"))),
    })
}

static KEY2: LazyLock<Regex> = LazyLock::new(|| Regex::new(r":[^/]+").expect("keyMatch2"));
static KEY3: LazyLock<Regex> = LazyLock::new(|| Regex::new(r"\{[^/]+\}").expect("keyMatch3"));
static KEY4: LazyLock<Regex> = LazyLock::new(|| Regex::new(r"\{([^/]+)\}").expect("keyMatch4"));
static KEY_GET3: LazyLock<Regex> = LazyLock::new(|| Regex::new(r"\{[^/]+?\}").expect("keyGet3"));

fn key_match(key1: &str, key2: &str) -> bool {
    let (a, b) = (key1.as_bytes(), key2.as_bytes());
    match key2.find('*') {
        None => key1 == key2,
        Some(i) if a.len() > i => a[..i] == b[..i],
        Some(i) => a == &b[..i],
    }
}

fn key_get(key1: &str, key2: &str) -> String {
    let (a, b) = (key1.as_bytes(), key2.as_bytes());
    match key2.find('*') {
        Some(i) if a.len() > i && a[..i] == b[..i] => String::from_utf8_lossy(&a[i..]).into_owned(),
        _ => String::new(),
    }
}

/// The pattern of a key: `/*` any path, each variable the pattern given.
fn key_pattern(key2: &str, var: &Regex, with: &str) -> String {
    let key2 = key2.replace("/*", "/.*");
    var.replace_all(&key2, regex::NoExpand(with)).into_owned()
}

/// A pattern `regexp.MustCompile` compiles, its refusal Go's panic.
fn must_compile(p: &str) -> Result<GoRegex, Fault> {
    GoRegex::compile(p).map_err(|e| Fault::Panic(format!("regexp: Compile({}): {e}", go_quote(p))))
}

/// `strconv.Quote` of a pattern: printable characters as they are.
fn go_quote(s: &str) -> String {
    let mut out = String::from("\"");
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\t' => out.push_str("\\t"),
            '\r' => out.push_str("\\r"),
            c if c.is_control() => out.push_str(&format!("\\x{:02x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

fn key_get_var(
    key1: &str,
    key2: &str,
    var: &str,
    re: &Regex,
    with: &str,
    name: fn(&str) -> &str,
) -> Result<String, Fault> {
    let key2 = key2.replace("/*", "/.*");
    let keys: Vec<&str> = re.find_iter(&key2).map(|m| m.as_str()).collect();
    let pattern = format!("^{}$", re.replace_all(&key2, regex::NoExpand(with)));
    let Some(values) = must_compile(&pattern)?.submatches(key1) else {
        return Ok(String::new());
    };
    Ok(keys
        .iter()
        .position(|k| name(k) == var)
        .and_then(|i| values.get(i + 1).cloned())
        .unwrap_or_default())
}

fn key_match4(key1: &str, key2: &str) -> Result<bool, Fault> {
    let key2 = key2.replace("/*", "/.*");
    let tokens: Vec<String> = KEY4.captures_iter(&key2).map(|c| c[1].to_string()).collect();
    let pattern = format!("^{}$", KEY4.replace_all(&key2, regex::NoExpand("([^/]+)")));
    let Some(matches) = must_compile(&pattern)?.submatches(key1) else {
        return Ok(false);
    };
    let matches = &matches[1..];
    if tokens.len() != matches.len() {
        return Err(Fault::Panic(
            "KeyMatch4: number of tokens is not equal to number of values".into(),
        ));
    }
    let mut values: Vec<(&str, &str)> = Vec::new();
    for (t, m) in tokens.iter().zip(matches) {
        match values.iter().find(|(k, _)| k == t) {
            Some((_, v)) if v != m => return Ok(false),
            Some(_) => {}
            None => values.push((t, m)),
        }
    }
    Ok(true)
}

fn regex_match(key1: &str, key2: &str) -> Result<bool, Fault> {
    let re = GoRegex::compile(key2).map_err(Fault::Panic)?;
    Ok(re.is_match(key1))
}

/// `net.ParseIP`: an IPv4 address of four decimal bytes, or an IPv6 address.
fn parse_ip(s: &str) -> Option<IpAddr> {
    if s.contains(':') {
        return s.parse::<std::net::Ipv6Addr>().ok().map(IpAddr::V6);
    }
    let parts: Vec<&str> = s.split('.').collect();
    if parts.len() != 4 {
        return None;
    }
    let mut b = [0u8; 4];
    for (i, p) in parts.iter().enumerate() {
        if p.is_empty() || p.len() > 3 || !p.bytes().all(|c| c.is_ascii_digit()) || (p.len() > 1 && p.starts_with('0'))
        {
            return None;
        }
        b[i] = p.parse().ok()?;
    }
    Some(IpAddr::from(b))
}

/// An address as the sixteen bytes Go holds, an IPv4 one mapped.
fn bytes16(ip: IpAddr) -> [u8; 16] {
    match ip {
        IpAddr::V4(v) => v.to_ipv6_mapped().octets(),
        IpAddr::V6(v) => v.octets(),
    }
}

fn ip_match(ip1: &str, ip2: &str) -> Result<bool, Fault> {
    let a = parse_ip(ip1)
        .ok_or_else(|| Fault::Panic("invalid argument: ip1 in IPMatch() function is not an IP address.".into()))?;
    if let Some((net, bits)) = ip2.split_once('/')
        && let Some(n) = parse_ip(net)
        && bits.bytes().all(|c| c.is_ascii_digit())
        && !bits.is_empty()
        && let Ok(bits) = bits.parse::<u32>()
    {
        let width = if n.is_ipv4() { 32 } else { 128 };
        if bits <= width {
            // `IPNet.Contains`: an IPv4 network (or one written as a mapped IPv6 address) holds the IPv4 addresses and
            // their mapped forms, an IPv6 network the IPv6 addresses that are not
            let four = |ip: IpAddr| match ip {
                IpAddr::V4(v) => Some(v.octets()),
                IpAddr::V6(v) => v.to_ipv4_mapped().map(|v| v.octets()),
            };
            let (x, y, bits): (Vec<u8>, Vec<u8>, u32) = match (four(n), four(a)) {
                (Some(x), Some(y)) => (
                    x.to_vec(),
                    y.to_vec(),
                    if n.is_ipv4() { bits } else { bits.saturating_sub(96) },
                ),
                (None, None) => (bytes16(n).to_vec(), bytes16(a).to_vec(), bits),
                _ => return Ok(false),
            };
            let full = (bits / 8) as usize;
            if x[..full] != y[..full] {
                return Ok(false);
            }
            let rest = bits % 8;
            if rest == 0 {
                return Ok(true);
            }
            let mask = 0xFFu8 << (8 - rest);
            return Ok(x[full] & mask == y[full] & mask);
        }
    }
    let b = parse_ip(ip2).ok_or_else(|| {
        Fault::Panic("invalid argument: ip2 in IPMatch() function is neither an IP address nor a CIDR.".into())
    })?;
    Ok(bytes16(a) == bytes16(b))
}

// ---------------------------------------------------------------------------------------------------------------
// `doublestar.Match` (v4.6.1), the separator `/`

fn bad_pattern() -> Fault {
    Fault::Error("syntax error in pattern".into())
}

/// `utf8.DecodeRuneInString`: the rune at the start and its length, an invalid byte the replacement rune of length 1.
fn rune(s: &[u8]) -> (char, usize) {
    if s.is_empty() {
        return ('\u{FFFD}', 0);
    }
    let n = match s[0] {
        0x00..=0x7F => 1,
        0xC0..=0xDF => 2,
        0xE0..=0xEF => 3,
        0xF0..=0xF7 => 4,
        _ => return ('\u{FFFD}', 1),
    };
    match s
        .get(..n)
        .and_then(|b| std::str::from_utf8(b).ok())
        .and_then(|t| t.chars().next())
    {
        Some(c) => (c, n),
        None => ('\u{FFFD}', 1),
    }
}

fn glob_match(pattern: &[u8], name: &[u8]) -> Result<bool, Fault> {
    glob(pattern, name, -1, -1, -1, -1, 0, 0)
}

#[allow(clippy::too_many_arguments)]
fn glob(
    pattern: &[u8],
    name: &[u8],
    mut ds_pat: isize,
    mut ds_name: isize,
    mut star_pat: isize,
    mut star_name: isize,
    mut pi: usize,
    mut ni: usize,
) -> Result<bool, Fault> {
    const SEP: char = '/';
    let (pl, nl) = (pattern.len(), name.len());
    let mut start_of_segment = true;
    'matching: while ni < nl {
        'step: {
            if pi >= pl {
                break 'step;
            }
            match pattern[pi] {
                b'*' => {
                    pi += 1;
                    if pi < pl && pattern[pi] == b'*' {
                        pi += 1;
                        if start_of_segment {
                            if pi >= pl {
                                return Ok(true);
                            }
                            let (r, n) = rune(&pattern[pi..]);
                            if r == SEP {
                                pi += n;
                                ds_pat = pi as isize;
                                ds_name = ni as isize;
                                star_pat = -1;
                                star_name = -1;
                                continue 'matching;
                            }
                        }
                    }
                    start_of_segment = false;
                    star_pat = pi as isize;
                    star_name = ni as isize;
                    continue 'matching;
                }
                b'?' => {
                    start_of_segment = false;
                    let (r, n) = rune(&name[ni..]);
                    if r == SEP {
                        break 'step;
                    }
                    pi += 1;
                    ni += n;
                    continue 'matching;
                }
                b'[' => {
                    start_of_segment = false;
                    pi += 1;
                    if pi >= pl {
                        return Err(bad_pattern());
                    }
                    let (nr, nn) = rune(&name[ni..]);
                    let mut matched = false;
                    let negate = pattern[pi] == b'!' || pattern[pi] == b'^';
                    if negate {
                        pi += 1;
                    }
                    if pi >= pl || pattern[pi] == b']' {
                        return Err(bad_pattern());
                    }
                    let mut last = char::MAX;
                    while pi < pl && pattern[pi] != b']' {
                        let (mut pr, mut pn) = rune(&pattern[pi..]);
                        pi += pn;
                        if last < char::MAX && pr == '-' && pi < pl && pattern[pi] != b']' {
                            if pattern[pi] == b'\\' {
                                pi += 1;
                            }
                            (pr, pn) = rune(&pattern[pi..]);
                            pi += pn;
                            if last <= nr && nr <= pr {
                                matched = true;
                                break;
                            }
                            last = char::MAX;
                            continue;
                        }
                        if pr == '\\' {
                            (pr, pn) = rune(&pattern[pi..]);
                            pi += pn;
                        }
                        if pr == nr {
                            matched = true;
                            break;
                        }
                        last = pr;
                    }
                    if matched == negate {
                        if pi >= pl {
                            return Err(bad_pattern());
                        }
                        break 'step;
                    }
                    let Some(close) = index_unescaped(&pattern[pi..], b']') else {
                        return Err(bad_pattern());
                    };
                    pi += close + 1;
                    ni += nn;
                    continue 'matching;
                }
                b'{' => {
                    let before = pi;
                    pi += 1;
                    let Some(close) = index_closing_alt(&pattern[pi..]) else {
                        return Err(bad_pattern());
                    };
                    let close = close + pi;
                    while let Some(comma) = index_next_alt(&pattern[pi..close]) {
                        let comma = comma + pi;
                        let alt = [&pattern[..before], &pattern[pi..comma], &pattern[close + 1..]].concat();
                        let r = glob(&alt, name, ds_pat, ds_name, star_pat, star_name, before, ni)?;
                        if r {
                            return Ok(true);
                        }
                        pi = comma + 1;
                    }
                    let alt = [&pattern[..before], &pattern[pi..close], &pattern[close + 1..]].concat();
                    return glob(&alt, name, ds_pat, ds_name, star_pat, star_name, before, ni);
                }
                c => {
                    if c == b'\\' {
                        pi += 1;
                        if pi >= pl {
                            return Err(bad_pattern());
                        }
                    }
                    let (pr, pn) = rune(&pattern[pi..]);
                    let (nr, nn) = rune(&name[ni..]);
                    if pr != nr {
                        if pi > 0 && pattern[pi - 1] == b'\\' {
                            pi -= 1;
                        }
                        break 'step;
                    }
                    pi += pn;
                    ni += nn;
                    start_of_segment = pr == SEP;
                    continue 'matching;
                }
            }
        }
        if star_pat >= 0 {
            let (r, n) = rune(&name[star_name as usize..]);
            if r != SEP {
                star_name += n as isize;
                pi = star_pat as usize;
                ni = star_name as usize;
                start_of_segment = false;
                continue 'matching;
            }
        }
        if ds_pat >= 0 {
            ni = ds_name as usize;
            while ni < nl {
                let (r, n) = rune(&name[ni..]);
                ni += n;
                if r == SEP {
                    ds_name = ni as isize;
                    pi = ds_pat as usize;
                    start_of_segment = true;
                    continue 'matching;
                }
            }
        }
        if pi < pl && !valid_glob(&pattern[pi..]) {
            return Err(bad_pattern());
        }
        return Ok(false);
    }
    if ni < nl {
        return Ok(false);
    }
    zero_length(&pattern[pi..])
}

fn zero_length(p: &[u8]) -> Result<bool, Fault> {
    if matches!(p, b"" | b"*" | b"**" | b"/**" | b"**/" | b"/**/") {
        return Ok(true);
    }
    if p[0] == b'{' {
        let Some(close) = index_closing_alt(&p[1..]) else {
            return Err(bad_pattern());
        };
        let close = close + 1;
        let mut pi = 1;
        while let Some(comma) = index_next_alt(&p[pi..close]) {
            let comma = comma + pi;
            if zero_length(&[&p[pi..comma], &p[close + 1..]].concat())? {
                return Ok(true);
            }
            pi = comma + 1;
        }
        return zero_length(&[&p[pi..close], &p[close + 1..]].concat());
    }
    if !valid_glob(p) {
        return Err(bad_pattern());
    }
    Ok(false)
}

fn index_unescaped(s: &[u8], c: u8) -> Option<usize> {
    let mut i = 0;
    while i < s.len() {
        if s[i] == b'\\' {
            i += 1;
        } else if s[i] == c {
            return Some(i);
        }
        i += 1;
    }
    None
}

fn index_closing_alt(s: &[u8]) -> Option<usize> {
    let mut alts = 1;
    let mut i = 0;
    while i < s.len() {
        match s[i] {
            b'\\' => i += 1,
            b'{' => alts += 1,
            b'}' => {
                alts -= 1;
                if alts == 0 {
                    return Some(i);
                }
            }
            _ => {}
        }
        i += 1;
    }
    None
}

fn index_next_alt(s: &[u8]) -> Option<usize> {
    let mut alts = 1;
    let mut i = 0;
    while i < s.len() {
        match s[i] {
            b'\\' => i += 1,
            b'{' => alts += 1,
            b'}' => alts -= 1,
            b',' if alts == 1 => return Some(i),
            _ => {}
        }
        i += 1;
    }
    None
}

/// `doValidatePattern`
fn valid_glob(s: &[u8]) -> bool {
    let mut depth = 0;
    let mut i = 0;
    let l = s.len();
    'validate: while i < l {
        match s[i] {
            b'\\' => {
                i += 1;
                if i >= l {
                    return false;
                }
            }
            b'[' => {
                i += 1;
                if i >= l {
                    return false;
                }
                if s[i] == b'^' || s[i] == b'!' {
                    i += 1;
                }
                if i >= l || s[i] == b']' {
                    return false;
                }
                while i < l {
                    if s[i] == b'\\' {
                        i += 1;
                    } else if s[i] == b']' {
                        i += 1;
                        continue 'validate;
                    }
                    i += 1;
                }
                return false;
            }
            b'{' => depth += 1,
            b'}' => {
                if depth == 0 {
                    return false;
                }
                depth -= 1;
            }
            _ => {}
        }
        i += 1;
    }
    depth == 0
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn escapes_assertions() {
        assert_eq!(escape_assertion("r.sub.Subject == p.act"), "r_sub.Subject == p_act");
        assert_eq!(escape_assertion("hasRole(r.sub, \"x.y\")"), "hasRole(r_sub, \"x.y\")");
        assert_eq!(escape_assertion("rr.sub"), "rr.sub");
    }

    #[test]
    fn matches_as_casbin() {
        assert!(key_match("/foo/bar", "/foo/*"));
        assert!(!key_match("/bar", "/foo/*"));
        assert_eq!(key_get("/foo/bar", "/foo/*"), "bar");
        assert_eq!(
            regex_match("/foo/1", &format!("^{}$", key_pattern("/foo/:id", &KEY2, "[^/]+"))),
            Ok(true)
        );
        assert_eq!(
            key_get_var("/foo/12", "/foo/:id", "id", &KEY2, "([^/]+)", |k| &k[1..]),
            Ok("12".into())
        );
        assert_eq!(key_match4("/a/1/b/1", "/a/{x}/b/{x}"), Ok(true));
        assert_eq!(key_match4("/a/1/b/2", "/a/{x}/b/{x}"), Ok(false));
        assert_eq!(ip_match("192.168.2.3", "192.168.2.0/24"), Ok(true));
        assert_eq!(ip_match("192.168.3.3", "192.168.2.0/24"), Ok(false));
        assert_eq!(ip_match("::ffff:192.168.2.3", "192.168.2.0/24"), Ok(true));
        assert!(ip_match("x", "1.2.3.4").is_err());
        assert_eq!(glob_match(b"/a/**/c", b"/a/b/x/c"), Ok(true));
        assert_eq!(glob_match(b"/a/*/c", b"/a/b/x/c"), Ok(false));
        assert_eq!(glob_match(b"*.{go,rs}", b"x.rs"), Ok(true));
        assert_eq!(glob_match(b"[a-c]x", b"bx"), Ok(true));
        assert_eq!(glob_match(b"[", b"a"), Err(bad_pattern()));
    }
}
