//! The strings govaluate reads as times (`tryParseTime`): Go's `time.ParseInLocation` over its fourteen layouts, in
//! the local zone of the service (UTC), giving the instant and the text `%v` prints of it.
use std::fmt;

use chrono::{DateTime, Datelike, FixedOffset, NaiveDate, Timelike};

/// A time a string literal reads as.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Time {
    unix: i64,
    nanos: u32,
    offset: i32,
    zone: String,
}

impl Time {
    pub fn unix(&self) -> i64 {
        self.unix
    }
}

impl fmt::Display for Time {
    /// `Time.String`: `2006-01-02 15:04:05.999999999 -0700 MST`.
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let tz = FixedOffset::east_opt(self.offset).unwrap_or(FixedOffset::east_opt(0).expect("offset 0"));
        let Some(t) = DateTime::from_timestamp(self.unix, self.nanos) else {
            return write!(f, "{}", self.unix);
        };
        let t = t.with_timezone(&tz);
        write!(
            f,
            "{:04}-{:02}-{:02} {:02}:{:02}:{:02}",
            t.year(),
            t.month(),
            t.day(),
            t.hour(),
            t.minute(),
            t.second()
        )?;
        if self.nanos > 0 {
            let frac = format!("{:09}", self.nanos);
            write!(f, ".{}", frac.trim_end_matches('0'))?;
        }
        let (sign, off) = if self.offset < 0 {
            ('-', -self.offset)
        } else {
            ('+', self.offset)
        };
        let zone = if self.zone.is_empty() {
            format!("{sign}{:02}{:02}", off / 3600, off / 60 % 60)
        } else {
            self.zone.clone()
        };
        write!(f, " {sign}{:02}{:02} {zone}", off / 3600, off / 60 % 60)
    }
}

/// An element of a layout (`nextStdChunk`).
#[derive(Clone, Copy)]
enum E {
    Lit(&'static str),
    WeekDay,
    Month,
    ZeroMonth,
    UnderDay,
    ZeroDay,
    Hour,
    Hour12,
    ZeroMinute,
    ZeroSecond,
    Year,
    Pm,
    Tz,
    NumTz,
    NumColonTz,
    IsoTz,
    IsoColonTz,
    Frac9,
}

use E::*;

const LAYOUTS: [&[E]; 14] = [
    // ANSIC
    &[
        WeekDay,
        Lit(" "),
        Month,
        Lit(" "),
        UnderDay,
        Lit(" "),
        Hour,
        Lit(":"),
        ZeroMinute,
        Lit(":"),
        ZeroSecond,
        Lit(" "),
        Year,
    ],
    // UnixDate
    &[
        WeekDay,
        Lit(" "),
        Month,
        Lit(" "),
        UnderDay,
        Lit(" "),
        Hour,
        Lit(":"),
        ZeroMinute,
        Lit(":"),
        ZeroSecond,
        Lit(" "),
        Tz,
        Lit(" "),
        Year,
    ],
    // RubyDate
    &[
        WeekDay,
        Lit(" "),
        Month,
        Lit(" "),
        ZeroDay,
        Lit(" "),
        Hour,
        Lit(":"),
        ZeroMinute,
        Lit(":"),
        ZeroSecond,
        Lit(" "),
        NumTz,
        Lit(" "),
        Year,
    ],
    // Kitchen
    &[Hour12, Lit(":"), ZeroMinute, Pm],
    // RFC3339
    &[
        Year,
        Lit("-"),
        ZeroMonth,
        Lit("-"),
        ZeroDay,
        Lit("T"),
        Hour,
        Lit(":"),
        ZeroMinute,
        Lit(":"),
        ZeroSecond,
        IsoColonTz,
    ],
    // RFC3339Nano
    &[
        Year,
        Lit("-"),
        ZeroMonth,
        Lit("-"),
        ZeroDay,
        Lit("T"),
        Hour,
        Lit(":"),
        ZeroMinute,
        Lit(":"),
        ZeroSecond,
        Frac9,
        IsoColonTz,
    ],
    &[Year, Lit("-"), ZeroMonth, Lit("-"), ZeroDay],
    &[
        Year,
        Lit("-"),
        ZeroMonth,
        Lit("-"),
        ZeroDay,
        Lit(" "),
        Hour,
        Lit(":"),
        ZeroMinute,
    ],
    &[
        Year,
        Lit("-"),
        ZeroMonth,
        Lit("-"),
        ZeroDay,
        Lit(" "),
        Hour,
        Lit(":"),
        ZeroMinute,
        Lit(":"),
        ZeroSecond,
    ],
    &[
        Year,
        Lit("-"),
        ZeroMonth,
        Lit("-"),
        ZeroDay,
        Lit(" "),
        Hour,
        Lit(":"),
        ZeroMinute,
        Lit(":"),
        ZeroSecond,
        NumColonTz,
    ],
    &[Year, Lit("-"), ZeroMonth, Lit("-"), ZeroDay, Lit("T"), Hour, IsoTz],
    &[
        Year,
        Lit("-"),
        ZeroMonth,
        Lit("-"),
        ZeroDay,
        Lit("T"),
        Hour,
        Lit(":"),
        ZeroMinute,
        IsoTz,
    ],
    &[
        Year,
        Lit("-"),
        ZeroMonth,
        Lit("-"),
        ZeroDay,
        Lit("T"),
        Hour,
        Lit(":"),
        ZeroMinute,
        Lit(":"),
        ZeroSecond,
        IsoTz,
    ],
    &[
        Year,
        Lit("-"),
        ZeroMonth,
        Lit("-"),
        ZeroDay,
        Lit("T"),
        Hour,
        Lit(":"),
        ZeroMinute,
        Lit(":"),
        ZeroSecond,
        Frac9,
        IsoTz,
    ],
];

const DAYS: [&str; 7] = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
const MONTHS: [&str; 12] = [
    "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec",
];

/// The time a string reads as in the first layout that takes it.
pub fn try_parse(s: &str) -> Option<Time> {
    LAYOUTS.iter().find_map(|l| parse(l, s.as_bytes()))
}

fn digit(v: &[u8], i: usize) -> bool {
    v.get(i).is_some_and(u8::is_ascii_digit)
}

/// `getnum`: one or two digits, two when fixed.
fn getnum(v: &[u8], fixed: bool) -> Option<(i64, &[u8])> {
    if !digit(v, 0) {
        return None;
    }
    if !digit(v, 1) {
        if fixed {
            return None;
        }
        return Some(((v[0] - b'0') as i64, &v[1..]));
    }
    Some((((v[0] - b'0') * 10 + (v[1] - b'0')) as i64, &v[2..]))
}

fn cutspace(v: &[u8]) -> &[u8] {
    let n = v.iter().take_while(|&&c| c == b' ').count();
    &v[n..]
}

/// `skip`: a literal of the layout, a space of it any number of spaces.
fn skip<'a>(mut v: &'a [u8], mut prefix: &[u8]) -> Option<&'a [u8]> {
    while let Some(&p) = prefix.first() {
        if p == b' ' {
            if v.first().is_some_and(|&c| c != b' ') {
                return None;
            }
            prefix = cutspace(prefix);
            v = cutspace(v);
            continue;
        }
        if v.first() != Some(&p) {
            return None;
        }
        prefix = &prefix[1..];
        v = &v[1..];
    }
    Some(v)
}

fn lookup<'a>(names: &[&str], v: &'a [u8]) -> Option<(usize, &'a [u8])> {
    names.iter().enumerate().find_map(|(i, n)| {
        let n = n.as_bytes();
        (v.len() >= n.len() && v[..n.len()].eq_ignore_ascii_case(n)).then(|| (i, &v[n.len()..]))
    })
}

/// `leadingInt`
fn leading_int(v: &[u8]) -> Option<(i64, &[u8])> {
    let n = v.iter().take_while(|c| c.is_ascii_digit()).count();
    let x = std::str::from_utf8(&v[..n]).ok()?.parse().ok()?;
    Some((x, &v[n..]))
}

fn signed_offset(v: &[u8]) -> usize {
    if !matches!(v.first(), Some(b'+' | b'-')) {
        return 0;
    }
    match leading_int(&v[1..]) {
        Some((x, rem)) if rem.len() != v.len() - 1 && x <= 23 => v.len() - rem.len(),
        _ => 0,
    }
}

/// `parseTimeZone`: the length of a zone abbreviation.
fn time_zone(v: &[u8]) -> Option<usize> {
    if v.len() < 3 {
        return None;
    }
    if v.len() >= 4 && (&v[..4] == b"ChST" || &v[..4] == b"MeST") {
        return Some(4);
    }
    if &v[..3] == b"GMT" {
        let rest = &v[3..];
        return Some(if rest.is_empty() { 3 } else { 3 + signed_offset(rest) });
    }
    if v[0] == b'+' || v[0] == b'-' {
        let n = signed_offset(v);
        return (n > 0).then_some(n);
    }
    let upper = v.iter().take(6).take_while(|c| c.is_ascii_uppercase()).count();
    match upper {
        5 if v[4] == b'T' => Some(5),
        4 if v[3] == b'T' || &v[..4] == b"WITA" => Some(4),
        3 => Some(3),
        _ => None,
    }
}

fn days_in(month: i64, year: i64) -> i64 {
    match month {
        2 if year % 4 == 0 && (year % 100 != 0 || year % 400 == 0) => 29,
        2 => 28,
        4 | 6 | 9 | 11 => 30,
        _ => 31,
    }
}

fn nanos(v: &[u8], n: usize) -> Option<u32> {
    let digits = &v[1..n.min(10)];
    let mut x: u32 = 0;
    for &d in digits {
        x = x * 10 + (d - b'0') as u32;
    }
    for _ in digits.len()..9 {
        x *= 10;
    }
    Some(x)
}

fn parse(layout: &[E], mut v: &[u8]) -> Option<Time> {
    let (mut year, mut month, mut day) = (0i64, -1i64, -1i64);
    let (mut hour, mut min, mut sec, mut nsec) = (0i64, 0i64, 0i64, 0u32);
    let (mut pm, mut am) = (false, false);
    let mut utc = false;
    let mut offset: Option<i64> = None;
    let mut zone = String::new();
    for (k, e) in layout.iter().enumerate() {
        match *e {
            Lit(p) => v = skip(v, p.as_bytes())?,
            Year => {
                if v.len() < 4 || !digit(v, 0) {
                    return None;
                }
                let (y, rest) = leading_int(&v[..4])?;
                if !rest.is_empty() {
                    return None;
                }
                year = y;
                v = &v[4..];
            }
            Month => {
                let (i, rest) = lookup(&MONTHS, v)?;
                month = i as i64 + 1;
                v = rest;
            }
            ZeroMonth => {
                let (m, rest) = getnum(v, true)?;
                if !(1..=12).contains(&m) {
                    return None;
                }
                month = m;
                v = rest;
            }
            WeekDay => v = lookup(&DAYS, v)?.1,
            UnderDay | ZeroDay => {
                if matches!(e, UnderDay) && v.first() == Some(&b' ') {
                    v = &v[1..];
                }
                let (d, rest) = getnum(v, matches!(e, ZeroDay))?;
                day = d;
                v = rest;
            }
            Hour => {
                let (h, rest) = getnum(v, false)?;
                if !(0..24).contains(&h) {
                    return None;
                }
                hour = h;
                v = rest;
            }
            Hour12 => {
                let (h, rest) = getnum(v, false)?;
                if !(0..=12).contains(&h) {
                    return None;
                }
                hour = h;
                v = rest;
            }
            ZeroMinute => {
                let (m, rest) = getnum(v, true)?;
                if !(0..60).contains(&m) {
                    return None;
                }
                min = m;
                v = rest;
            }
            ZeroSecond => {
                let (s, rest) = getnum(v, true)?;
                if !(0..60).contains(&s) {
                    return None;
                }
                sec = s;
                v = rest;
                // a fractional second the layout does not name is read all the same
                if v.len() >= 2
                    && matches!(v[0], b'.' | b',')
                    && digit(v, 1)
                    && !matches!(layout.get(k + 1), Some(Frac9))
                {
                    let n = 2 + v[2..].iter().take_while(|c| c.is_ascii_digit()).count();
                    nsec = nanos(v, n)?;
                    v = &v[n..];
                }
            }
            Frac9 => {
                if v.len() < 2 || !matches!(v[0], b'.' | b',') || !digit(v, 1) {
                    continue;
                }
                let n = 1 + v[1..].iter().take_while(|c| c.is_ascii_digit()).count();
                nsec = nanos(v, n)?;
                v = &v[n..];
            }
            Pm => {
                if v.len() < 2 {
                    return None;
                }
                match &v[..2] {
                    b"PM" => pm = true,
                    b"AM" => am = true,
                    _ => return None,
                }
                v = &v[2..];
            }
            Tz => {
                if v.len() >= 3 && &v[..3] == b"UTC" {
                    utc = true;
                    v = &v[3..];
                    continue;
                }
                let n = time_zone(v)?;
                zone = String::from_utf8_lossy(&v[..n]).into_owned();
                v = &v[n..];
            }
            NumTz | NumColonTz | IsoTz | IsoColonTz => {
                if matches!(e, IsoTz | IsoColonTz) && v.first() == Some(&b'Z') {
                    v = &v[1..];
                    utc = true;
                    continue;
                }
                let (sign, h, m) = if matches!(e, NumTz | IsoTz) {
                    if v.len() < 5 {
                        return None;
                    }
                    let parts = (v[0], &v[1..3], &v[3..5]);
                    v = &v[5..];
                    parts
                } else {
                    if v.len() < 6 || v[3] != b':' {
                        return None;
                    }
                    let parts = (v[0], &v[1..3], &v[4..6]);
                    v = &v[6..];
                    parts
                };
                let (h, _) = getnum(h, true)?;
                let (m, _) = getnum(m, true)?;
                if h > 24 || m > 60 {
                    return None;
                }
                let off = (h * 60 + m) * 60;
                offset = Some(match sign {
                    b'+' => off,
                    b'-' => -off,
                    _ => return None,
                });
            }
        }
    }
    if !v.is_empty() {
        return None;
    }
    if pm && hour < 12 {
        hour += 12;
    } else if am && hour == 12 {
        hour = 0;
    }
    if month < 0 {
        month = 1;
    }
    if day < 0 {
        day = 1;
    }
    if day < 1 || day > days_in(month, year) {
        return None;
    }
    let date = NaiveDate::from_ymd_opt(year as i32, month as u32, day as u32)?;
    let wall = date
        .and_hms_opt(hour as u32, min as u32, sec as u32)?
        .and_utc()
        .timestamp();
    Some(match (utc, offset) {
        (true, _) => Time {
            unix: wall,
            nanos: nsec,
            offset: 0,
            zone: "UTC".into(),
        },
        (false, Some(off)) => Time {
            unix: wall - off,
            nanos: nsec,
            offset: off as i32,
            zone: if off == 0 { "UTC".into() } else { String::new() },
        },
        // an abbreviation the local zone does not know: a fabricated zone of that name and no offset
        (false, None) if !zone.is_empty() && zone != "UTC" => {
            let off = if zone.len() > 3 && zone.starts_with("GMT") {
                zone[3..].parse::<i64>().unwrap_or(0) * 3600
            } else {
                0
            };
            Time {
                unix: wall,
                nanos: nsec,
                offset: off as i32,
                zone,
            }
        }
        _ => Time {
            unix: wall,
            nanos: nsec,
            offset: 0,
            zone: "UTC".into(),
        },
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn reads_the_layouts_of_govaluate() {
        let unix = |s: &str| try_parse(s).map(|t| t.unix());
        assert_eq!(unix("2006-01-02"), Some(1136160000));
        assert_eq!(unix("2006-01-02T15:04:05Z"), Some(1136214245));
        assert_eq!(unix("2006-01-02T15:04:05+01:00"), Some(1136210645));
        assert_eq!(unix("2006-01-02 15:04"), Some(1136214240));
        assert_eq!(unix("Mon Jan  2 15:04:05 2006"), Some(1136214245));
        assert_eq!(unix("3:04PM"), Some(-62167219200 + 15 * 3600 + 4 * 60));
        assert_eq!(unix("2006-02-30"), None);
        assert_eq!(unix("admin"), None);
        assert_eq!(unix("2006"), None);
    }
}
