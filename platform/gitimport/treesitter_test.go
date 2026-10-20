//go:build cgo

package gitimport

import (
	"slices"
	"strings"
	"testing"
)

func TestTreeSitterSymbols(t *testing.T) {
	ts, err := NewTreeSitter()
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()
	for _, c := range []struct {
		file, src string
		want      []string // kind:qualified name
	}{
		{"x.c", `#include <linux/module.h>
#include "local.h"
#define MAX 10
#define SQ(x) ((x)*(x))
struct point { int x, y; };
union u { int a; float b; };
enum color { RED, GREEN };
typedef struct point point_t;
static int counter = 0;
static const char *names[] = {"a"};
int add(int a, int b);
static struct point *make(void) { int local = 1; return 0; }
int main(void) { return add(1, 2); }
`, []string{"import:linux/module.h", "import:local.h", "macro:MAX", "macro:SQ", "struct:point", "union:u", "enum:color", "type:point_t",
			"variable:counter", "variable:names", "prototype:add", "function:make", "function:main"}},
		{"x.go", `package x

import (
	"fmt"
	io "io"
)

const Max = 3

var count int

type Point struct{ X int }

type Shape interface{ Area() float64 }

type ID string

func New() *Point { var local int; _ = local; return nil }

func (p *Point) Move(dx int) { fmt.Println(dx) }
`, []string{"import:fmt", "import:io", "constant:Max", "variable:count", "struct:Point", "interface:Shape", "type:ID", "function:New", "method:Move"}},
		{"x.py", `import os
from collections import OrderedDict
LIMIT = 4

class Shape:
    def area(self):
        def inner():
            pass
        return 0

def main():
    x = 1
`, []string{"import:os", "import:collections", "variable:LIMIT", "class:Shape", "method:Shape.area", "function:main"}},
		{"X.java", `package p;
import java.util.List;
public class Shape {
    private int sides;
    public Shape() {}
    public int area() { int local = 0; return local; }
    interface Visitor { void visit(); }
    enum Kind { A, B }
}
`, []string{"import:java.util.List", "class:Shape", "field:Shape.sides", "method:Shape.Shape", "method:Shape.area", "interface:Shape.Visitor", "method:Shape.Visitor.visit", "enum:Shape.Kind"}},
		{"x.js", `import fs from "fs";
const LIMIT = 3;
function main() { const local = 1; }
class Shape { area() { return 0; } }
`, []string{"import:fs", "variable:LIMIT", "function:main", "class:Shape", "method:Shape.area"}},
		{"x.ts", `import { a } from "./a";
export const LIMIT = 3;
interface Shape { area(): number; }
type ID = string;
enum Color { Red }
class Square implements Shape { area(): number { return 1; } }
function main(): void {}
`, []string{"import:./a", "variable:LIMIT", "interface:Shape", "method:Shape.area", "type:ID", "enum:Color", "class:Square", "method:Square.area", "function:main"}},
		{"x.rs", `use std::io;
const MAX: u32 = 3;
static mut COUNT: u32 = 0;
struct Point { x: i32 }
enum Color { Red }
trait Shape { fn area(&self) -> f64; }
impl Point { fn new() -> Self { Point { x: 0 } } }
mod util { pub fn help() {} }
macro_rules! sq { ($x:expr) => { $x * $x }; }
fn main() { let local = 1; }
`, []string{"import:std::io", "constant:MAX", "variable:COUNT", "struct:Point", "enum:Color", "interface:Shape", "prototype:Shape.area",
			"method:Point.new", "module:util", "method:util.help", "macro:sq", "function:main"}},
		{"x.cpp", `#include <vector>
namespace geo {
class Shape {
public:
    virtual double area() const;
};
double Shape::area() const { return 0; }
}
int main() { return 0; }
`, []string{"import:vector", "namespace:geo", "class:geo.Shape", "prototype:geo.Shape.area", "method:geo.Shape::area", "function:main"}},
	} {
		lang := ts.Language(c.file)
		syms, err := ts.Symbols(lang, []byte(c.src))
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		var got []string
		for _, s := range syms {
			q := s.Name
			if s.Container != "" {
				q = s.Container + "." + q
			}
			got = append(got, s.Kind+":"+q)
			if s.Digest == "" || s.Signature == "" {
				t.Errorf("%s: %s has no digest or signature", c.file, q)
			}
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s:\nwant %s\ngot  %s", c.file, strings.Join(c.want, " "), strings.Join(got, " "))
		}
	}
}
