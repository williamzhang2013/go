// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gc

import (
	"bytes"
	"fmt"
	"internal/testenv"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// This file contains code generation tests.
//
// Each test is defined in a variable of type asmTest. Tests are
// architecture-specific, and they are grouped in arrays of tests, one
// for each architecture.
//
// Each asmTest consists of a function to compile, an array of
// positive regexps that must match the generated assembly and
// an array of negative regexps that must not match generated assembly.
// For example, the following amd64 test
//
//   {
// 	  fn: `
// 	  func f0(x int) int {
// 		  return x * 64
// 	  }
// 	  `,
// 	  pos: []string{"\tSHLQ\t[$]6,"},
//	  neg: []string{"MULQ"}
//   }
//
// verifies that the code the compiler generates for a multiplication
// by 64 contains a 'SHLQ' instruction and does not contain a MULQ.
//
// Since all the tests for a given architecture are dumped in the same
// file, the function names must be unique. As a workaround for this
// restriction, the test harness supports the use of a '$' placeholder
// for function names. The func f0 above can be also written as
//
//   {
// 	  fn: `
// 	  func $(x int) int {
// 		  return x * 64
// 	  }
// 	  `,
// 	  pos: []string{"\tSHLQ\t[$]6,"},
//	  neg: []string{"MULQ"}
//   }
//
// Each '$'-function will be given a unique name of form f<N>_<arch>,
// where <N> is the test index in the test array, and <arch> is the
// test's architecture.
//
// It is allowed to mix named and unnamed functions in the same test
// array; the named functions will retain their original names.

// TestAssembly checks to make sure the assembly generated for
// functions contains certain expected instructions.
func TestAssembly(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	if runtime.GOOS == "windows" {
		// TODO: remove if we can get "go tool compile -S" to work on windows.
		t.Skipf("skipping test: recursive windows compile not working")
	}
	dir, err := ioutil.TempDir("", "TestAssembly")
	if err != nil {
		t.Fatalf("could not create directory: %v", err)
	}
	defer os.RemoveAll(dir)

	nameRegexp := regexp.MustCompile("func \\w+")
	t.Run("platform", func(t *testing.T) {
		for _, ats := range allAsmTests {
			ats := ats
			t.Run(ats.os+"/"+ats.arch, func(tt *testing.T) {
				tt.Parallel()

				asm := ats.compileToAsm(tt, dir)

				for i, at := range ats.tests {
					var funcName string
					if strings.Contains(at.fn, "func $") {
						funcName = fmt.Sprintf("f%d_%s", i, ats.arch)
					} else {
						funcName = nameRegexp.FindString(at.fn)[len("func "):]
					}
					fa := funcAsm(tt, asm, funcName)
					if fa != "" {
						at.verifyAsm(tt, fa)
					}
				}
			})
		}
	})
}

var nextTextRegexp = regexp.MustCompile(`\n\S`)

// funcAsm returns the assembly listing for the given function name.
func funcAsm(t *testing.T, asm string, funcName string) string {
	if i := strings.Index(asm, fmt.Sprintf("TEXT\t\"\".%s(SB)", funcName)); i >= 0 {
		asm = asm[i:]
	} else {
		t.Errorf("could not find assembly for function %v", funcName)
		return ""
	}

	// Find the next line that doesn't begin with whitespace.
	loc := nextTextRegexp.FindStringIndex(asm)
	if loc != nil {
		asm = asm[:loc[0]]
	}

	return asm
}

type asmTest struct {
	// function to compile
	fn string
	// regular expressions that must match the generated assembly
	pos []string
	// regular expressions that must not match the generated assembly
	neg []string
}

func (at asmTest) verifyAsm(t *testing.T, fa string) {
	for _, r := range at.pos {
		if b, err := regexp.MatchString(r, fa); !b || err != nil {
			t.Errorf("expected:%s\ngo:%s\nasm:%s\n", r, at.fn, fa)
		}
	}
	for _, r := range at.neg {
		if b, err := regexp.MatchString(r, fa); b || err != nil {
			t.Errorf("not expected:%s\ngo:%s\nasm:%s\n", r, at.fn, fa)
		}
	}
}

type asmTests struct {
	arch    string
	os      string
	imports []string
	tests   []*asmTest
}

func (ats *asmTests) generateCode() []byte {
	var buf bytes.Buffer
	fmt.Fprintln(&buf, "package main")
	for _, s := range ats.imports {
		fmt.Fprintf(&buf, "import %q\n", s)
	}

	for i, t := range ats.tests {
		function := strings.Replace(t.fn, "func $", fmt.Sprintf("func f%d_%s", i, ats.arch), 1)
		fmt.Fprintln(&buf, function)
	}

	return buf.Bytes()
}

// compile compiles the package pkg for architecture arch and
// returns the generated assembly.  dir is a scratch directory.
func (ats *asmTests) compileToAsm(t *testing.T, dir string) string {
	// create test directory
	testDir := filepath.Join(dir, fmt.Sprintf("%s_%s", ats.arch, ats.os))
	err := os.Mkdir(testDir, 0700)
	if err != nil {
		t.Fatalf("could not create directory: %v", err)
	}

	// Create source.
	src := filepath.Join(testDir, "test.go")
	err = ioutil.WriteFile(src, ats.generateCode(), 0600)
	if err != nil {
		t.Fatalf("error writing code: %v", err)
	}

	// First, install any dependencies we need.  This builds the required export data
	// for any packages that are imported.
	for _, i := range ats.imports {
		out := filepath.Join(testDir, i+".a")

		if s := ats.runGo(t, "build", "-o", out, "-gcflags=-dolinkobj=false", i); s != "" {
			t.Fatalf("Stdout = %s\nWant empty", s)
		}
	}

	// Now, compile the individual file for which we want to see the generated assembly.
	asm := ats.runGo(t, "tool", "compile", "-I", testDir, "-S", "-o", filepath.Join(testDir, "out.o"), src)
	return asm
}

// runGo runs go command with the given args and returns stdout string.
// go is run with GOARCH and GOOS set as ats.arch and ats.os respectively
func (ats *asmTests) runGo(t *testing.T, args ...string) string {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(testenv.GoToolPath(t), args...)
	cmd.Env = append(os.Environ(), "GOARCH="+ats.arch, "GOOS="+ats.os)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("error running cmd: %v\nstdout:\n%sstderr:\n%s\n", err, stdout.String(), stderr.String())
	}

	if s := stderr.String(); s != "" {
		t.Fatalf("Stderr = %s\nWant empty", s)
	}

	return stdout.String()
}

var allAsmTests = []*asmTests{
	{
		arch:    "amd64",
		os:      "linux",
		imports: []string{"unsafe", "runtime"},
		tests:   linuxAMD64Tests,
	},
	{
		arch:  "386",
		os:    "linux",
		tests: linux386Tests,
	},
	{
		arch:  "s390x",
		os:    "linux",
		tests: linuxS390XTests,
	},
	{
		arch:    "arm",
		os:      "linux",
		imports: []string{"runtime"},
		tests:   linuxARMTests,
	},
	{
		arch:  "arm64",
		os:    "linux",
		tests: linuxARM64Tests,
	},
	{
		arch:  "mips",
		os:    "linux",
		tests: linuxMIPSTests,
	},
	{
		arch:  "mips64",
		os:    "linux",
		tests: linuxMIPS64Tests,
	},
	{
		arch:  "ppc64le",
		os:    "linux",
		tests: linuxPPC64LETests,
	},
	{
		arch:  "amd64",
		os:    "plan9",
		tests: plan9AMD64Tests,
	},
}

var linuxAMD64Tests = []*asmTest{
	{
		fn: `
		func $(x int) int {
			return x * 96
		}
		`,
		pos: []string{"\tSHLQ\t\\$5,", "\tLEAQ\t\\(.*\\)\\(.*\\*2\\),"},
	},
	// Structure zeroing.  See issue #18370.
	{
		fn: `
		type T1 struct {
			a, b, c int
		}
		func $(t *T1) {
			*t = T1{}
		}
		`,
		pos: []string{"\tXORPS\tX., X", "\tMOVUPS\tX., \\(.*\\)", "\tMOVQ\t\\$0, 16\\(.*\\)"},
	},
	// SSA-able composite literal initialization. Issue 18872.
	{
		fn: `
		type T18872 struct {
			a, b, c, d int
		}

		func f18872(p *T18872) {
			*p = T18872{1, 2, 3, 4}
		}
		`,
		pos: []string{"\tMOVQ\t[$]1", "\tMOVQ\t[$]2", "\tMOVQ\t[$]3", "\tMOVQ\t[$]4"},
	},
	// Also test struct containing pointers (this was special because of write barriers).
	{
		fn: `
		type T2 struct {
			a, b, c *int
		}
		func f19(t *T2) {
			*t = T2{}
		}
		`,
		pos: []string{"\tXORPS\tX., X", "\tMOVUPS\tX., \\(.*\\)", "\tMOVQ\t\\$0, 16\\(.*\\)", "\tCALL\truntime\\.gcWriteBarrier\\(SB\\)"},
	},
	{
		fn: `
		func f33(m map[int]int) int {
			return m[5]
		}
		`,
		pos: []string{"\tMOVQ\t[$]5,"},
	},
	// Direct use of constants in fast map access calls. Issue 19015.
	{
		fn: `
		func f34(m map[int]int) bool {
			_, ok := m[5]
			return ok
		}
		`,
		pos: []string{"\tMOVQ\t[$]5,"},
	},
	{
		fn: `
		func f35(m map[string]int) int {
			return m["abc"]
		}
		`,
		pos: []string{"\"abc\""},
	},
	{
		fn: `
		func f36(m map[string]int) bool {
			_, ok := m["abc"]
			return ok
		}
		`,
		pos: []string{"\"abc\""},
	},
	// Bit test ops on amd64, issue 18943.
	{
		fn: `
		func f37(a, b uint64) int {
			if a&(1<<(b&63)) != 0 {
				return 1
			}
			return -1
		}
		`,
		pos: []string{"\tBTQ\t"},
	},
	{
		fn: `
		func f38(a, b uint64) bool {
			return a&(1<<(b&63)) != 0
		}
		`,
		pos: []string{"\tBTQ\t"},
	},
	{
		fn: `
		func f39(a uint64) int {
			if a&(1<<60) != 0 {
				return 1
			}
			return -1
		}
		`,
		pos: []string{"\tBTQ\t\\$60"},
	},
	{
		fn: `
		func f40(a uint64) bool {
			return a&(1<<60) != 0
		}
		`,
		pos: []string{"\tBTQ\t\\$60"},
	},
	// see issue 19595.
	// We want to merge load+op in f58, but not in f59.
	{
		fn: `
		func f58(p, q *int) {
			x := *p
			*q += x
		}`,
		pos: []string{"\tADDQ\t\\("},
	},
	{
		fn: `
		func f59(p, q *int) {
			x := *p
			for i := 0; i < 10; i++ {
				*q += x
			}
		}`,
		pos: []string{"\tADDQ\t[A-Z]"},
	},
	// Floating-point strength reduction
	{
		fn: `
		func f60(f float64) float64 {
			return f * 2.0
		}`,
		pos: []string{"\tADDSD\t"},
	},
	{
		fn: `
		func f62(f float64) float64 {
			return f / 16.0
		}`,
		pos: []string{"\tMULSD\t"},
	},
	{
		fn: `
		func f63(f float64) float64 {
			return f / 0.125
		}`,
		pos: []string{"\tMULSD\t"},
	},
	{
		fn: `
		func f64(f float64) float64 {
			return f / 0.5
		}`,
		pos: []string{"\tADDSD\t"},
	},
	// Check that compare to constant string uses 2/4/8 byte compares
	{
		fn: `
		func f65(a string) bool {
		    return a == "xx"
		}`,
		pos: []string{"\tCMPW\t\\(.*\\), [$]"},
	},
	{
		fn: `
		func f66(a string) bool {
		    return a == "xxxx"
		}`,
		pos: []string{"\tCMPL\t\\(.*\\), [$]"},
	},
	{
		fn: `
		func f67(a string) bool {
		    return a == "xxxxxxxx"
		}`,
		pos: []string{"\tCMPQ\t[A-Z]"},
	},
	// Check that array compare uses 2/4/8 byte compares
	{
		fn: `
		func f68(a,b [2]byte) bool {
		    return a == b
		}`,
		pos: []string{"\tCMPW\t\"\"[.+_a-z0-9]+\\(SP\\), [A-Z]"},
	},
	{
		fn: `
		func f69(a,b [3]uint16) bool {
		    return a == b
		}`,
		pos: []string{
			"\tCMPL\t\"\"[.+_a-z0-9]+\\(SP\\), [A-Z]",
			"\tCMPW\t\"\"[.+_a-z0-9]+\\(SP\\), [A-Z]",
		},
	},
	{
		fn: `
		func $(a,b [3]int16) bool {
		    return a == b
		}`,
		pos: []string{
			"\tCMPL\t\"\"[.+_a-z0-9]+\\(SP\\), [A-Z]",
			"\tCMPW\t\"\"[.+_a-z0-9]+\\(SP\\), [A-Z]",
		},
	},
	{
		fn: `
		func $(a,b [12]int8) bool {
		    return a == b
		}`,
		pos: []string{
			"\tCMPQ\t\"\"[.+_a-z0-9]+\\(SP\\), [A-Z]",
			"\tCMPL\t\"\"[.+_a-z0-9]+\\(SP\\), [A-Z]",
		},
	},
	{
		fn: `
		func f70(a,b [15]byte) bool {
		    return a == b
		}`,
		pos: []string{"\tCMPQ\t\"\"[.+_a-z0-9]+\\(SP\\), [A-Z]"},
	},
	{
		fn: `
		func f71(a,b unsafe.Pointer) bool { // This was a TODO in mapaccess1_faststr
		    return *((*[4]byte)(a)) != *((*[4]byte)(b))
		}`,
		pos: []string{"\tCMPL\t\\(.*\\), [A-Z]"},
	},
	{
		// make sure assembly output has matching offset and base register.
		fn: `
		func f72(a, b int) int {
			runtime.GC() // use some frame
			return b
		}
		`,
		pos: []string{"b\\+24\\(SP\\)"},
	},
	{
		// check load combining
		fn: `
		func f73(a, b byte) (byte,byte) {
		    return f73(f73(a,b))
		}
		`,
		pos: []string{"\tMOVW\t"},
	},
	{
		fn: `
		func f74(a, b uint16) (uint16,uint16) {
		    return f74(f74(a,b))
		}
		`,
		pos: []string{"\tMOVL\t"},
	},
	{
		fn: `
		func f75(a, b uint32) (uint32,uint32) {
		    return f75(f75(a,b))
		}
		`,
		pos: []string{"\tMOVQ\t"},
	},
	// Make sure we don't put pointers in SSE registers across safe points.
	{
		fn: `
		func $(p, q *[2]*int)  {
		    a, b := p[0], p[1]
		    runtime.GC()
		    q[0], q[1] = a, b
		}
		`,
		neg: []string{"MOVUPS"},
	},
	{
		// check that stack store is optimized away
		fn: `
		func $() int {
			var x int
			return *(&x)
		}
		`,
		pos: []string{"TEXT\t.*, [$]0-8"},
	},
	// int <-> fp moves
	{
		fn: `
		func $(x uint32) bool {
			return x > 4
		}
		`,
		pos: []string{"\tSETHI\t.*\\(SP\\)"},
	},
	// Check that len() and cap() div by a constant power of two
	// are compiled into SHRQ.
	{
		fn: `
		func $(a []int) int {
			return len(a) / 1024
		}
		`,
		pos: []string{"\tSHRQ\t\\$10,"},
	},
	{
		fn: `
		func $(s string) int {
			return len(s) / (4097 >> 1)
		}
		`,
		pos: []string{"\tSHRQ\t\\$11,"},
	},
	{
		fn: `
		func $(a []int) int {
			return cap(a) / ((1 << 11) + 2048)
		}
		`,
		pos: []string{"\tSHRQ\t\\$12,"},
	},
	// Check that len() and cap() mod by a constant power of two
	// are compiled into ANDQ.
	{
		fn: `
		func $(a []int) int {
			return len(a) % 1024
		}
		`,
		pos: []string{"\tANDQ\t\\$1023,"},
	},
	{
		fn: `
		func $(s string) int {
			return len(s) % (4097 >> 1)
		}
		`,
		pos: []string{"\tANDQ\t\\$2047,"},
	},
	{
		fn: `
		func $(a []int) int {
			return cap(a) % ((1 << 11) + 2048)
		}
		`,
		pos: []string{"\tANDQ\t\\$4095,"},
	},
	{
		// Test that small memmove was replaced with direct movs
		fn: `
                func $() {
                       x := [...]byte{1, 2, 3, 4, 5, 6, 7}
                       copy(x[1:], x[:])
                }
		`,
		neg: []string{"memmove"},
	},
	{
		// Same as above but with different size
		fn: `
                func $() {
                       x := [...]byte{1, 2, 3, 4}
                       copy(x[1:], x[:])
                }
		`,
		neg: []string{"memmove"},
	},
	{
		// Same as above but with different size
		fn: `
                func $() {
                       x := [...]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
                       copy(x[1:], x[:])
                }
		`,
		neg: []string{"memmove"},
	},
	{
		fn: `
		func $(p int, q *int) bool {
			return p < *q
		}
		`,
		pos: []string{"CMPQ\t\\(.*\\), [A-Z]"},
	},
	{
		fn: `
		func $(p *int, q int) bool {
			return *p < q
		}
		`,
		pos: []string{"CMPQ\t\\(.*\\), [A-Z]"},
	},
	{
		fn: `
		func $(p *int) bool {
			return *p < 7
		}
		`,
		pos: []string{"CMPQ\t\\(.*\\), [$]7"},
	},
	{
		fn: `
		func $(p *int) bool {
			return 7 < *p
		}
		`,
		pos: []string{"CMPQ\t\\(.*\\), [$]7"},
	},
	{
		fn: `
		func $(p **int) {
			*p = nil
		}
		`,
		pos: []string{"CMPL\truntime.writeBarrier\\(SB\\), [$]0"},
	},
}

var linux386Tests = []*asmTest{
	{
		// check that stack store is optimized away
		fn: `
		func $() int {
			var x int
			return *(&x)
		}
		`,
		pos: []string{"TEXT\t.*, [$]0-4"},
	},
	// Check that len() and cap() div by a constant power of two
	// are compiled into SHRL.
	{
		fn: `
		func $(a []int) int {
			return len(a) / 1024
		}
		`,
		pos: []string{"\tSHRL\t\\$10,"},
	},
	{
		fn: `
		func $(s string) int {
			return len(s) / (4097 >> 1)
		}
		`,
		pos: []string{"\tSHRL\t\\$11,"},
	},
	{
		fn: `
		func $(a []int) int {
			return cap(a) / ((1 << 11) + 2048)
		}
		`,
		pos: []string{"\tSHRL\t\\$12,"},
	},
	// Check that len() and cap() mod by a constant power of two
	// are compiled into ANDL.
	{
		fn: `
		func $(a []int) int {
			return len(a) % 1024
		}
		`,
		pos: []string{"\tANDL\t\\$1023,"},
	},
	{
		fn: `
		func $(s string) int {
			return len(s) % (4097 >> 1)
		}
		`,
		pos: []string{"\tANDL\t\\$2047,"},
	},
	{
		fn: `
		func $(a []int) int {
			return cap(a) % ((1 << 11) + 2048)
		}
		`,
		pos: []string{"\tANDL\t\\$4095,"},
	},
	{
		// Test that small memmove was replaced with direct movs
		fn: `
                func $() {
                       x := [...]byte{1, 2, 3, 4, 5, 6, 7}
                       copy(x[1:], x[:])
                }
		`,
		neg: []string{"memmove"},
	},
	{
		// Same as above but with different size
		fn: `
                func $() {
                       x := [...]byte{1, 2, 3, 4}
                       copy(x[1:], x[:])
                }
		`,
		neg: []string{"memmove"},
	},
}

var linuxS390XTests = []*asmTest{
	// Fused multiply-add/sub instructions.
	{
		fn: `
		func f14(x, y, z float64) float64 {
			return x * y + z
		}
		`,
		pos: []string{"\tFMADD\t"},
	},
	{
		fn: `
		func f15(x, y, z float64) float64 {
			return x * y - z
		}
		`,
		pos: []string{"\tFMSUB\t"},
	},
	{
		fn: `
		func f16(x, y, z float32) float32 {
			return x * y + z
		}
		`,
		pos: []string{"\tFMADDS\t"},
	},
	{
		fn: `
		func f17(x, y, z float32) float32 {
			return x * y - z
		}
		`,
		pos: []string{"\tFMSUBS\t"},
	},
	{
		// check that stack store is optimized away
		fn: `
		func $() int {
			var x int
			return *(&x)
		}
		`,
		pos: []string{"TEXT\t.*, [$]0-8"},
	},
}

var linuxARMTests = []*asmTest{
	{
		// make sure assembly output has matching offset and base register.
		fn: `
		func f13(a, b int) int {
			runtime.GC() // use some frame
			return b
		}
		`,
		pos: []string{"b\\+4\\(FP\\)"},
	},
	{
		// check that stack store is optimized away
		fn: `
		func $() int {
			var x int
			return *(&x)
		}
		`,
		pos: []string{"TEXT\t.*, [$]-4-4"},
	},
}

var linuxARM64Tests = []*asmTest{
	{
		fn: `
		func $(x, y uint32) uint32 {
			return x &^ y
		}
		`,
		pos: []string{"\tBIC\t"},
		neg: []string{"\tAND\t"},
	},
	{
		fn: `
		func $(x, y uint32) uint32 {
			return x ^ ^y
		}
		`,
		pos: []string{"\tEON\t"},
		neg: []string{"\tXOR\t"},
	},
	{
		fn: `
		func $(x, y uint32) uint32 {
			return x | ^y
		}
		`,
		pos: []string{"\tORN\t"},
		neg: []string{"\tORR\t"},
	},
	{
		fn: `
		func f34(a uint64) uint64 {
			return a & ((1<<63)-1)
		}
		`,
		pos: []string{"\tAND\t"},
	},
	{
		fn: `
		func f35(a uint64) uint64 {
			return a & (1<<63)
		}
		`,
		pos: []string{"\tAND\t"},
	},
	{
		// make sure offsets are folded into load and store.
		fn: `
		func f36(_, a [20]byte) (b [20]byte) {
			b = a
			return
		}
		`,
		pos: []string{"\tMOVD\t\"\"\\.a\\+[0-9]+\\(FP\\), R[0-9]+", "\tMOVD\tR[0-9]+, \"\"\\.b\\+[0-9]+\\(FP\\)"},
	},
	{
		// check that stack store is optimized away
		fn: `
		func $() int {
			var x int
			return *(&x)
		}
		`,
		pos: []string{"TEXT\t.*, [$]-8-8"},
	},
	{
		// check that we don't emit comparisons for constant shift
		fn: `
//go:nosplit
		func $(x int) int {
			return x << 17
		}
		`,
		pos: []string{"LSL\t\\$17"},
		neg: []string{"CMP"},
	},
	{
		fn: `
		func $(a int32, ptr *int) {
			if a >= 0 {
				*ptr = 0
			}
		}
		`,
		pos: []string{"TBNZ"},
	},
	{
		fn: `
		func $(a int64, ptr *int) {
			if a >= 0 {
				*ptr = 0
			}
		}
		`,
		pos: []string{"TBNZ"},
	},
	{
		fn: `
		func $(a int32, ptr *int) {
			if a < 0 {
				*ptr = 0
			}
		}
		`,
		pos: []string{"TBZ"},
	},
	{
		fn: `
		func $(a int64, ptr *int) {
			if a < 0 {
				*ptr = 0
			}
		}
		`,
		pos: []string{"TBZ"},
	},
	// Load-combining tests.
	{
		fn: `
		func $(s []byte) uint16 {
			return uint16(s[0]) | uint16(s[1]) << 8
		}
		`,
		pos: []string{"\tMOVHU\t\\(R[0-9]+\\)"},
		neg: []string{"ORR\tR[0-9]+<<8\t"},
	},
	{
		// make sure that CSEL is emitted for conditional moves
		fn: `
		func f37(c int) int {
		     x := c + 4
		     if c < 0 {
		     	x = 182
		     }
		     return x
		}
		`,
		pos: []string{"\tCSEL\t"},
	},
	// Check that zero stores are combine into larger stores
	{
		fn: `
		func $(b []byte) {
			_ = b[1] // early bounds check to guarantee safety of writes below
			b[0] = 0
			b[1] = 0
		}
		`,
		pos: []string{"MOVH\tZR"},
		neg: []string{"MOVB"},
	},
	{
		fn: `
		func $(b []byte) {
			_ = b[1] // early bounds check to guarantee safety of writes below
			b[1] = 0
			b[0] = 0
		}
		`,
		pos: []string{"MOVH\tZR"},
		neg: []string{"MOVB"},
	},
	{
		fn: `
		func $(b []byte) {
			_ = b[3] // early bounds check to guarantee safety of writes below
			b[0] = 0
			b[1] = 0
			b[2] = 0
			b[3] = 0
		}
		`,
		pos: []string{"MOVW\tZR"},
		neg: []string{"MOVB", "MOVH"},
	},
	{
		fn: `
		func $(b []byte) {
			_ = b[3] // early bounds check to guarantee safety of writes below
			b[2] = 0
			b[3] = 0
			b[1] = 0
			b[0] = 0
		}
		`,
		pos: []string{"MOVW\tZR"},
		neg: []string{"MOVB", "MOVH"},
	},
	{
		fn: `
		func $(h []uint16) {
			_ = h[1] // early bounds check to guarantee safety of writes below
			h[0] = 0
			h[1] = 0
		}
		`,
		pos: []string{"MOVW\tZR"},
		neg: []string{"MOVB", "MOVH"},
	},
	{
		fn: `
		func $(h []uint16) {
			_ = h[1] // early bounds check to guarantee safety of writes below
			h[1] = 0
			h[0] = 0
		}
		`,
		pos: []string{"MOVW\tZR"},
		neg: []string{"MOVB", "MOVH"},
	},
	{
		fn: `
		func $(b []byte) {
			_ = b[7] // early bounds check to guarantee safety of writes below
			b[0] = 0
			b[1] = 0
			b[2] = 0
			b[3] = 0
			b[4] = 0
			b[5] = 0
			b[6] = 0
			b[7] = 0
		}
		`,
		pos: []string{"MOVD\tZR"},
		neg: []string{"MOVB", "MOVH", "MOVW"},
	},
	{
		fn: `
		func $(h []uint16) {
			_ = h[3] // early bounds check to guarantee safety of writes below
			h[0] = 0
			h[1] = 0
			h[2] = 0
			h[3] = 0
		}
		`,
		pos: []string{"MOVD\tZR"},
		neg: []string{"MOVB", "MOVH", "MOVW"},
	},
	{
		fn: `
		func $(h []uint16) {
			_ = h[3] // early bounds check to guarantee safety of writes below
			h[2] = 0
			h[3] = 0
			h[1] = 0
			h[0] = 0
		}
		`,
		pos: []string{"MOVD\tZR"},
		neg: []string{"MOVB", "MOVH", "MOVW"},
	},
	{
		fn: `
		func $(w []uint32) {
			_ = w[1] // early bounds check to guarantee safety of writes below
			w[0] = 0
			w[1] = 0
		}
		`,
		pos: []string{"MOVD\tZR"},
		neg: []string{"MOVB", "MOVH", "MOVW"},
	},
	{
		fn: `
		func $(w []uint32) {
			_ = w[1] // early bounds check to guarantee safety of writes below
			w[1] = 0
			w[0] = 0
		}
		`,
		pos: []string{"MOVD\tZR"},
		neg: []string{"MOVB", "MOVH", "MOVW"},
	},
	{
		fn: `
		func $(b []byte) {
			_ = b[15] // early bounds check to guarantee safety of writes below
			b[0] = 0
			b[1] = 0
			b[2] = 0
			b[3] = 0
			b[4] = 0
			b[5] = 0
			b[6] = 0
			b[7] = 0
			b[8] = 0
			b[9] = 0
			b[10] = 0
			b[11] = 0
			b[12] = 0
			b[13] = 0
			b[15] = 0
			b[14] = 0
		}
		`,
		pos: []string{"STP"},
		neg: []string{"MOVB", "MOVH", "MOVW"},
	},
	{
		fn: `
		func $(h []uint16) {
			_ = h[7] // early bounds check to guarantee safety of writes below
			h[0] = 0
			h[1] = 0
			h[2] = 0
			h[3] = 0
			h[4] = 0
			h[5] = 0
			h[6] = 0
			h[7] = 0
		}
		`,
		pos: []string{"STP"},
		neg: []string{"MOVB", "MOVH"},
	},
	{
		fn: `
		func $(w []uint32) {
			_ = w[3] // early bounds check to guarantee safety of writes below
			w[0] = 0
			w[1] = 0
			w[2] = 0
			w[3] = 0
		}
		`,
		pos: []string{"STP"},
		neg: []string{"MOVB", "MOVH"},
	},
	{
		fn: `
		func $(w []uint32) {
			_ = w[3] // early bounds check to guarantee safety of writes below
			w[1] = 0
			w[0] = 0
			w[3] = 0
			w[2] = 0
		}
		`,
		pos: []string{"STP"},
		neg: []string{"MOVB", "MOVH"},
	},
	{
		fn: `
		func $(d []uint64) {
			_ = d[1] // early bounds check to guarantee safety of writes below
			d[0] = 0
			d[1] = 0
		}
		`,
		pos: []string{"STP"},
		neg: []string{"MOVB", "MOVH"},
	},
	{
		fn: `
		func $(d []uint64) {
			_ = d[1] // early bounds check to guarantee safety of writes below
			d[1] = 0
			d[0] = 0
		}
		`,
		pos: []string{"STP"},
		neg: []string{"MOVB", "MOVH"},
	},
	{
		fn: `
		func $(a *[39]byte) {
			*a = [39]byte{}
		}
		`,
		pos: []string{"MOVD"},
		neg: []string{"MOVB", "MOVH", "MOVW"},
	},
	{
		fn: `
		func $(a *[30]byte) {
			*a = [30]byte{}
		}
		`,
		pos: []string{"STP"},
		neg: []string{"MOVB", "MOVH", "MOVW"},
	},
}

var linuxMIPSTests = []*asmTest{
	{
		// check that stack store is optimized away
		fn: `
		func $() int {
			var x int
			return *(&x)
		}
		`,
		pos: []string{"TEXT\t.*, [$]-4-4"},
	},
}

var linuxMIPS64Tests = []*asmTest{
	{
		// check that we don't emit comparisons for constant shift
		fn: `
		func $(x int) int {
			return x << 17
		}
		`,
		pos: []string{"SLLV\t\\$17"},
		neg: []string{"SGT"},
	},
}

var linuxPPC64LETests = []*asmTest{
	// Fused multiply-add/sub instructions.
	{
		fn: `
		func f0(x, y, z float64) float64 {
			return x * y + z
		}
		`,
		pos: []string{"\tFMADD\t"},
	},
	{
		fn: `
		func f1(x, y, z float64) float64 {
			return x * y - z
		}
		`,
		pos: []string{"\tFMSUB\t"},
	},
	{
		fn: `
		func f2(x, y, z float32) float32 {
			return x * y + z
		}
		`,
		pos: []string{"\tFMADDS\t"},
	},
	{
		fn: `
		func f3(x, y, z float32) float32 {
			return x * y - z
		}
		`,
		pos: []string{"\tFMSUBS\t"},
	},
	{
		// check that stack store is optimized away
		fn: `
		func $() int {
			var x int
			return *(&x)
		}
		`,
		pos: []string{"TEXT\t.*, [$]0-8"},
	},
}

var plan9AMD64Tests = []*asmTest{
	// We should make sure that the compiler doesn't generate floating point
	// instructions for non-float operations on Plan 9, because floating point
	// operations are not allowed in the note handler.
	// Array zeroing.
	{
		fn: `
		func $() [16]byte {
			var a [16]byte
			return a
		}
		`,
		pos: []string{"\tMOVQ\t\\$0, \"\""},
	},
	// Array copy.
	{
		fn: `
		func $(a [16]byte) (b [16]byte) {
			b = a
			return
		}
		`,
		pos: []string{"\tMOVQ\t\"\"\\.a\\+[0-9]+\\(SP\\), (AX|CX)", "\tMOVQ\t(AX|CX), \"\"\\.b\\+[0-9]+\\(SP\\)"},
	},
}

// TestLineNumber checks to make sure the generated assembly has line numbers
// see issue #16214
func TestLineNumber(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	dir, err := ioutil.TempDir("", "TestLineNumber")
	if err != nil {
		t.Fatalf("could not create directory: %v", err)
	}
	defer os.RemoveAll(dir)

	src := filepath.Join(dir, "x.go")
	err = ioutil.WriteFile(src, []byte(issue16214src), 0644)
	if err != nil {
		t.Fatalf("could not write file: %v", err)
	}

	cmd := exec.Command(testenv.GoToolPath(t), "tool", "compile", "-S", "-o", filepath.Join(dir, "out.o"), src)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fail to run go tool compile: %v", err)
	}

	if strings.Contains(string(out), "unknown line number") {
		t.Errorf("line number missing in assembly:\n%s", out)
	}
}

var issue16214src = `
package main

func Mod32(x uint32) uint32 {
	return x % 3 // frontend rewrites it as HMUL with 2863311531, the LITERAL node has unknown Pos
}
`
