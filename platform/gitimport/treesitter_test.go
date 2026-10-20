//go:build cgo

package gitimport

import (
	"reflect"
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
`, []string{"import:fmt", "import:io", "constant:Max", "variable:count", "struct:Point", "field:Point.X", "interface:Shape", "method:Shape.Area", "type:ID", "function:New", "method:Point.Move"}},
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
`, []string{"import:vector", "namespace:geo", "class:geo.Shape", "prototype:geo.Shape.area", "method:geo.Shape.area", "function:main"}},
	} {
		lang := ts.Language(c.file)
		src, err := ts.Parse(lang, []byte(c.src))
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		var got []string
		for _, i := range src.Imports {
			got = append(got, "import:"+i.Path)
		}
		for _, s := range src.Symbols {
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

// TestTreeSitterRefs reads the imports of a file and the names its declarations call and use.
func TestTreeSitterRefs(t *testing.T) {
	ts, err := NewTreeSitter()
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()
	refs := func(s Symbol) []string {
		var out []string
		for _, r := range s.Refs {
			q := r.Name
			if r.Qualifier != "" {
				q = r.Qualifier + "." + q
			}
			out = append(out, r.Kind+":"+q)
		}
		slices.Sort(out)
		return out
	}
	for _, c := range []struct {
		file, src string
		pkg       string
		imports   []Import
		want      map[string][]string // qualified symbol → the references it holds (a subset)
		self      map[string]string
	}{
		{file: "x.go", src: `package shapes

import (
	"fmt"
	str "strings"
)

const Max = 3

type Point struct{ X int }

func New() *Point { return &Point{X: Max} }

func (p *Point) Move() { p.check(); fmt.Println(str.ToUpper("a")); New() }
`, pkg: "shapes", imports: []Import{{Path: "fmt"}, {Path: "strings", Alias: "str"}},
			want: map[string][]string{"New": {"name:Max", "type:Point"}, "Point.Move": {"call:New", "call:fmt.Println", "call:p.check", "call:str.ToUpper", "type:Point"}},
			self: map[string]string{"Point.Move": "p"}},
		{file: "m.py", src: `import os.path
import numpy as np
from .util import helper, other as alias
from pkg import *

class Base: pass

class Shape(Base):
    def area(self):
        self.check()
        return helper(np.zeros(1))
`, imports: []Import{{Path: "os.path", Alias: "os.path"}, {Path: "numpy", Alias: "np"}, {Path: ".util", Names: map[string]string{"helper": "helper", "alias": "other"}}, {Path: "pkg", Alias: "*"}},
			want: map[string][]string{"Shape": {"type:Base"}, "Shape.area": {"call:helper", "call:np.zeros", "call:self.check"}}},
		{file: "A.java", src: `package com.acme;
import java.util.List;
import static java.lang.Math.*;
public class A extends B {
    void run() { helper(); List.of(); new C(); }
}
`, pkg: "com.acme", imports: []Import{{Path: "java.util.List"}, {Path: "java.lang.Math.*"}},
			want: map[string][]string{"A": {"type:B"}, "A.run": {"call:C", "call:List.of", "call:helper"}}},
		{file: "x.ts", src: `import def, { a, b as c } from "./lib";
import * as ns from "pkg";
class S extends Base { run(): T { a(); ns.f(); return this.g(); } }
`, imports: []Import{{Path: "./lib", Names: map[string]string{"def": "default", "a": "a", "c": "b"}}, {Path: "pkg", Alias: "ns"}},
			want: map[string][]string{"S": {"type:Base"}, "S.run": {"call:a", "call:ns.f", "call:this.g", "type:T"}}},
		{file: "x.c", src: `#include <stdio.h>
#include "util.h"
struct point { int x; };
int add(int a, int b) { struct point p; printf("%d", helper(a)); return MAX; }
`, imports: []Import{{Path: "stdio.h", System: true}, {Path: "util.h"}},
			want: map[string][]string{"add": {"call:helper", "call:printf", "name:MAX", "type:point"}}},
		{file: "x.rs", src: `use std::io;
use crate::util::{help, Thing as T};
use super::model::*;
fn main() { help(); io::stdin(); T::new(); println!("x"); }
`, imports: []Import{{Path: "std::io", Names: map[string]string{"io": "io"}}, {Path: "crate::util::help", Names: map[string]string{"help": "help"}},
			{Path: "crate::util::Thing", Names: map[string]string{"T": "Thing"}}, {Path: "super::model", Alias: "*"}},
			want: map[string][]string{"main": {"call:T.new", "call:help", "call:io.stdin", "call:println"}}},
	} {
		src, err := ts.Parse(ts.Language(c.file), []byte(c.src))
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		if src.Package != c.pkg {
			t.Errorf("%s: package %q, want %q", c.file, src.Package, c.pkg)
		}
		if !reflect.DeepEqual(src.Imports, c.imports) {
			t.Errorf("%s: imports\nwant %+v\ngot  %+v", c.file, c.imports, src.Imports)
		}
		by := map[string]Symbol{}
		for _, s := range src.Symbols {
			by[qualified(s.Container, s.Name)] = s
			if s.Code == "" || !within(s.Code, c.src) {
				t.Errorf("%s: %s: code %q", c.file, s.Name, s.Code)
			}
		}
		for q, want := range c.want {
			got := refs(by[q])
			for _, w := range want {
				if !slices.Contains(got, w) {
					t.Errorf("%s: %s: no %s in %v", c.file, q, w, got)
				}
			}
		}
		for q, self := range c.self {
			if by[q].Self != self {
				t.Errorf("%s: %s: self %q, want %q", c.file, q, by[q].Self, self)
			}
		}
	}
}

// TestContainerOwnText keeps a class's digest when only a member changes, and changes it with its own text.
func TestContainerOwnText(t *testing.T) {
	ts, err := NewTreeSitter()
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()
	digests := func(file, src string) map[string]string {
		t.Helper()
		s, err := ts.Parse(languageOf(file), []byte(src))
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, x := range s.Symbols {
			out[x.Kind+":"+qualified(x.Container, x.Name)] = x.Digest
		}
		return out
	}
	for _, c := range []struct {
		file, before, member, own, drop string
		class, method                   string
	}{
		{"a.py", "class A:\n    x = 1\n\n    def f(self):\n        return 1\n\n    def g(self):\n        return 2\n",
			"return 1", "x = 1", "\n    def g(self):\n        return 2\n", "class:A", "method:A.f"},
		{"a.ts", "class A {\n  x = 1;\n  f() { return 1; }\n  g() { return 2; }\n}\n",
			"return 1", "x = 1", "  g() { return 2; }\n", "class:A", "method:A.f"},
		{"a.go", "package a\n\n// T is a type.\ntype T struct {\n\tX int\n\tY string\n}\n",
			"X int", "", "\tY string\n", "struct:T", "field:T.X"},
	} {
		before := digests(c.file, c.before)
		if before[c.class] == "" || before[c.method] == "" {
			t.Fatalf("%s: %v", c.file, before)
		}
		member := digests(c.file, strings.Replace(c.before, c.member, c.member+"0", 1))
		if member[c.class] != before[c.class] || member[c.method] == before[c.method] {
			t.Errorf("%s: a member changed, the class's digest %v, the member's changed %v", c.file,
				member[c.class] != before[c.class], member[c.method] != before[c.method])
		}
		// a member removed changes no container either
		if removed := digests(c.file, strings.Replace(c.before, c.drop, "", 1)); removed[c.class] != before[c.class] {
			t.Errorf("%s: a member removed changed the class's digest", c.file)
		}
		if c.own == "" {
			continue // a Go struct has no text of its own but its fields
		}
		own := digests(c.file, strings.Replace(c.before, c.own, c.own+"0", 1))
		if own[c.class] == before[c.class] {
			t.Errorf("%s: the class's own text changed, its digest did not", c.file)
		}
	}
}

// within tells whether the bytes of code appear in src in their order (a container's code leaves its members out).
func within(code, src string) bool {
	for i := 0; i < len(code); i++ {
		j := strings.IndexByte(src, code[i])
		if j < 0 {
			return false
		}
		src = src[j+1:]
	}
	return true
}
