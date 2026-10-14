package litmus

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// testLeg is a service with a running run whose head and base checkouts are two copies of one tiny Go module.
func testLeg(t *testing.T, testFile string) (*Service, string) {
	t.Helper()
	svc, now := newSvc(t)
	run := Run{ID: "run_s1", SessionID: "s1", State: RunRunning, Deadline: now.Add(RunBudget)}
	for _, leg := range []string{"head", "base"} {
		dir := filepath.Join(t.TempDir(), "checkout")
		if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o750); err != nil {
			t.Fatal(err)
		}
		for name, body := range map[string]string{"go.mod": "module fixture\n\ngo 1.21\n", "pkg/pkg_test.go": testFile} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		run.Legs = append(run.Legs, Leg{Name: leg, Worktree: dir, State: "running"})
	}
	is(t, "")(nil, svc.store.Save(run))
	return svc, filepath.Join(svc.store.root, run.ID)
}

const passingTest = `package pkg

import "testing"

func TestOne(t *testing.T) {}
`

func TestTestVerbAllowList(t *testing.T) {
	for _, argv := range [][]string{
		{"go", "test", "./..."}, {"go", "vet", "./..."}, {"npm", "test"}, {"npm", "run", "test"}, {"npm", "run", "test:unit"},
		{"npx", "vitest", "run"}, {"npx", "jest"}, {"pnpm", "test", "--", "-u"},
	} {
		if !testAllowed(argv) {
			t.Errorf("%v refused", argv)
		}
	}
	for _, argv := range [][]string{
		{}, {"go"}, {"go", "build", "./..."}, {"go", "run", "x.go"}, {"go", "test", "-exec", "sh", "./..."}, {"go", "test", "-toolexec=sh"},
		{"go", "vet", "-vettool=/tmp/x", "./..."}, {"go", "vet", "-vettool", "/tmp/x"}, {"go", "test", "--vettool=/tmp/x"}, {"go", "test", "-vettool", "/tmp/x"}, {"go", "vet", "--exec=sh"},
		{"npm", "run", "build"}, {"npm", "install"}, {"npx", "prettier"}, {"pnpm", "run", "test"}, {"sh", "-c", "go test"}, {"rm", "-rf", "x"}, {"npm"},
	} {
		if testAllowed(argv) {
			t.Errorf("%v accepted", argv)
		}
	}
	svc, _ := testLeg(t, passingTest)
	is(t, "TESTING_TEST_NOT_ALLOWED")(svc.Test(ctx, "s1", "head", "", []string{"sh", "-c", "echo hi"}, "", 0))
	is(t, "TESTING_LEG_INVALID")(svc.Test(ctx, "s1", "main", "", []string{"go", "test"}, "", 0))
	is(t, "TESTING_TEST_DIR_INVALID")(svc.Test(ctx, "s1", "head", "../escape", []string{"go", "test"}, "", 0))
	is(t, "TESTING_TEST_DIR_INVALID")(svc.Test(ctx, "s1", "head", "nope", []string{"go", "test"}, "", 0))
}

func TestTestVerbRefusesAnUnpreparedLeg(t *testing.T) {
	svc, _ := newSvc(t)
	if _, err := svc.Intake(ctx, "s1", prURL); err != nil {
		t.Fatal(err)
	}
	is(t, "TESTING_LEG_NOT_PREPARED")(svc.Test(ctx, "s1", "head", "", []string{"go", "test"}, "", 0))
}

func TestTestVerbRunsInTheCheckoutAndRecordsAReceipt(t *testing.T) {
	svc, runDir := testLeg(t, passingTest)
	for _, name := range []string{"pkg", "pkg"} {
		leg := map[bool]string{true: "head", false: "base"}[len(mustChecks(t, svc)) == 0]
		c, err := svc.CheckAdd(ctx, "s1", name, "tests pass", leg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.CheckStart(ctx, "s1", c.ID); err != nil {
			t.Fatal(err)
		}
		res, err := svc.Test(ctx, "s1", leg, "", []string{"go", "test", "./pkg/", "-count=1"}, "", 0)
		if err != nil || res.Exit != 0 || res.TimedOut || res.Summary != "1 ok, 0 FAIL, 0 --- FAIL" || res.Ran == nil || *res.Ran != 1 || res.NotBuilt || !strings.Contains(res.Output, "ok  \tfixture/pkg") {
			t.Fatalf("%s: %+v err %v", leg, res, err)
		}
		full, err := os.ReadFile(res.Receipt)
		if filepath.Ext(res.Receipt) != ".txt" || err != nil || !strings.Contains(string(full), "$ go test ./pkg/ -count=1") || !strings.Contains(string(full), "exit 0") {
			t.Fatalf("receipt %s: %q err %v", res.Receipt, full, err)
		}
		// A .txt receipt satisfies the check verb.
		if got, err := svc.CheckEnd(ctx, "s1", c.ID, CheckPass, "all green", false); err != nil || got.Receipts[0].OutputPath != res.Receipt || got.Receipts[0].AfterPath != "" {
			t.Fatalf("check end: %+v err %v", got, err)
		} else if r := got.Receipts[0]; r.Exit == nil || *r.Exit != 0 || r.Ran == nil || *r.Ran != 1 || r.NotBuilt {
			t.Fatalf("the receipt does not say the run exited 0 and counted one test: %+v", r)
		}
	}
	data, err := os.ReadFile(filepath.Join(runDir, "actions.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Seq     int
		Leg     string
		Action  string
		Args    []string
		Exit    *int
		After   string
		NoTests bool
	}
	line, _, _ := strings.Cut(string(data), "\n")
	if err := json.Unmarshal([]byte(line), &rec); err != nil || rec.Seq != 1 || rec.Leg != "head" || rec.Action != "test" || rec.Exit == nil || *rec.Exit != 0 || rec.After != "" || rec.Args[0] != "go" || rec.NoTests {
		t.Fatalf("actions.jsonl %q: %+v err %v", line, rec, err)
	}
	// ... and the submit guard.
	v, err := svc.Submit(ctx, "s1", "", "")
	if err != nil || len(v.Checks) != 2 {
		t.Fatalf("submit: %+v err %v", v, err)
	}
	if md, _ := os.ReadFile(filepath.Join(runDir, "verdict.md")); !strings.Contains(string(md), "receipts/1-test.txt") {
		t.Fatalf("verdict.md does not link the test receipt:\n%s", md)
	}
}

func mustChecks(t *testing.T, svc *Service) []Check {
	t.Helper()
	checks, err := svc.store.Checks("run_s1")
	if err != nil {
		t.Fatal(err)
	}
	return checks
}

func TestTestVerbScrubsTheEnvironment(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "leaked")
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "cli")
	t.Setenv("CI", "")
	t.Setenv("GH_TOKEN", "ghp_secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-secret")
	svc, _ := testLeg(t, `package pkg

import (
	"os"
	"strings"
	"testing"
)

func TestEnv(t *testing.T) {
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "AO_") || strings.HasPrefix(kv, "CLAUDE") || strings.HasPrefix(kv, "GH_TOKEN") || strings.HasPrefix(kv, "AWS_") {
			t.Fatalf("leaked %s", kv)
		}
	}
	if os.Getenv("CI") != "1" {
		t.Fatalf("CI = %q", os.Getenv("CI"))
	}
}
`)
	res, err := svc.Test(ctx, "s1", "head", "", []string{"go", "test", "./pkg/", "-count=1"}, "", 0)
	if err != nil || res.Exit != 0 {
		t.Fatalf("%+v err %v", res, err)
	}
	if env := strings.Join(testEnv(""), "\n"); strings.Contains(env, "AO_SESSION_ID") || strings.Contains(env, "CLAUDE") || !strings.Contains(env, "CI=1") {
		t.Fatal("testEnv leaks or lacks CI")
	}
}

func TestTestEnvIsAnAllowList(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghp_secret")
	t.Setenv("GITHUB_TOKEN", "ghs_secret")
	t.Setenv("ANTHROPIC_API_KEY", "sk-secret")
	t.Setenv("OPENAI_API_KEY", "sk-secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-secret")
	t.Setenv("GOPROXY", "https://proxy.example")
	t.Setenv("GOFLAGS", "-mod=mod")
	env := testEnv("")
	joined := strings.Join(env, "\n")
	for _, secret := range []string{"GH_TOKEN", "GITHUB_TOKEN", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "AWS_", "-secret"} {
		if strings.Contains(joined, secret) {
			t.Errorf("child env carries %q:\n%s", secret, joined)
		}
	}
	for _, want := range []string{"CI=1", "GOPROXY=https://proxy.example", "GOFLAGS=-mod=mod"} {
		if !slices.Contains(env, want) {
			t.Errorf("child env lacks %s", want)
		}
	}
	t.Setenv("GOFLAGS", "-mod=mod -toolexec=/tmp/x")
	if strings.Contains(strings.Join(testEnv(""), "\n"), "GOFLAGS") {
		t.Error("GOFLAGS naming a program to run was passed on")
	}
	nodeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(nodeDir, "node"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	env = testEnv(nodeDir)
	if got, err := lookIn(env, "node"); err != nil || got != filepath.Join(nodeDir, "node") {
		t.Errorf("node = %q, %v", got, err)
	}
	if want, err := exec.LookPath("go"); err != nil {
		t.Skip("no go on PATH")
	} else if got, err := lookIn(env, "go"); err != nil || got == "" {
		t.Errorf("go not on the child's PATH (parent has %s): %v", want, err)
	}
}

func TestTestVerbReportsAFailure(t *testing.T) {
	svc, _ := testLeg(t, `package pkg

import "testing"

func TestBad(t *testing.T) { t.Fatal("boom") }
`)
	res, err := svc.Test(ctx, "s1", "head", "", []string{"go", "test", "./pkg/", "-count=1"}, "", 0)
	if err != nil || res.Exit != 1 || res.Summary != "0 ok, 1 FAIL, 1 --- FAIL" || res.Ran == nil || *res.Ran != 1 || res.NotBuilt || !strings.Contains(res.Output, "boom") {
		t.Fatalf("%+v err %v", res, err)
	}
}

func TestTestVerbKillsLeftoverChildren(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups")
	}
	for name, script := range map[string]string{
		"detached":       "sleep 300 >/dev/null 2>&1 </dev/null & echo $! > pid",
		"holds the pipe": "sleep 300 & echo $! > pid",
	} {
		dir := t.TempDir()
		res, err := execTest(ctx, dir, []string{"sh", "-c", script}, os.Environ(), time.Minute, filepath.Join(dir, "receipts", "1-test.txt"))
		if err != nil || res.Exit != 0 || res.TimedOut {
			t.Fatalf("%s: %+v err %v", name, res, err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, "pid"))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
		for i := 0; i < 50 && syscall.Kill(pid, 0) == nil; i++ {
			time.Sleep(100 * time.Millisecond)
		}
		if err := syscall.Kill(pid, 0); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Errorf("%s: sleeper %d survived the verb", name, pid)
		}
	}
}

func TestTestVerbTimedOutOnlyWhenKilled(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "receipts", "1-test.txt")
	res, err := execTest(ctx, dir, []string{"sh", "-c", "exit 0"}, os.Environ(), time.Minute, file)
	if err != nil || res.TimedOut || res.Exit != 0 {
		t.Fatalf("%+v err %v", res, err)
	}
	res, err = execTest(ctx, dir, []string{"sleep", "30"}, os.Environ(), 300*time.Millisecond, file)
	if err != nil || !res.TimedOut || res.Exit != -1 {
		t.Fatalf("killed command: %+v err %v", res, err)
	}
	r := Receipt{Seq: 1, Action: "test", OutputPath: file, Exit: &res.Exit, Ran: ranN(1), Failing: true}
	if r.failedTest() {
		t.Fatal("a timed-out test counts as a failed test")
	}
	_, _, err = checkEvidence(Check{Receipts: []Receipt{r}}, CheckFail, "timed out")
	is(t, "TESTING_CHECK_EVIDENCE")(nil, err)
	if !strings.Contains(err.Error(), "the test timed out or the command was missing; raise --minutes or end it skipped") {
		t.Fatalf("timeout refusal: %v", err)
	}
}

func TestTestVerbTimeCapKillsTheProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups")
	}
	svc, _ := testLeg(t, `package pkg

import (
	"os"
	"strconv"
	"testing"
	"time"
)

func TestSleep(t *testing.T) {
	_ = os.WriteFile("../pid", []byte(strconv.Itoa(os.Getpid())), 0o600)
	time.Sleep(time.Minute)
}
`)
	svc.testCap = 2 * time.Second
	t0 := time.Now()
	res, err := svc.Test(ctx, "s1", "head", "", []string{"go", "test", "./pkg/", "-count=1"}, "", 0)
	if err != nil || !res.TimedOut || res.Exit == 0 || time.Since(t0) > 30*time.Second {
		t.Fatalf("%+v err %v after %s", res, err, time.Since(t0))
	}
	full, _ := os.ReadFile(res.Receipt)
	if !strings.Contains(string(full), "timed out after 2s") {
		t.Fatalf("receipt lacks the timeout:\n%s", full)
	}
	svc.mu.Lock()
	run, _ := svc.store.Get("run_s1")
	svc.mu.Unlock()
	raw, err := os.ReadFile(filepath.Join(run.Legs[0].Worktree, "pid"))
	if err != nil {
		t.Skipf("the test binary never started: %v", err)
	}
	pid, _ := strconv.Atoi(string(raw))
	for i := 0; i < 50 && syscall.Kill(pid, 0) == nil; i++ { // the test binary is a grandchild of go test
		time.Sleep(100 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) == nil {
		t.Fatalf("test binary %d survived the cap", pid)
	}
}

func TestTestVerbIsLimitedPerLeg(t *testing.T) {
	svc, runDir := testLeg(t, passingTest)
	var lines string
	for i := 1; i <= maxTests; i++ {
		lines += `{"seq":` + strconv.Itoa(i) + `,"leg":"head","action":"test"}` + "\n"
	}
	if err := os.WriteFile(filepath.Join(runDir, "actions.jsonl"), []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	is(t, "TESTING_TEST_LIMIT")(svc.Test(ctx, "s1", "head", "", []string{"go", "test", "./pkg/", "-count=1"}, "", 0))
	if res, err := svc.Test(ctx, "s1", "base", "", []string{"go", "test", "./pkg/", "-count=1"}, "", 0); err != nil || res.Seq != maxTests+1 {
		t.Fatalf("base leg is counted on its own: %+v err %v", res, err)
	}
}

func TestTestVerbDoesNotCountRunsThatRanNothing(t *testing.T) {
	svc, runDir := testLeg(t, "package pkg\n") // a test file with no tests: go test exits 0 with "[no tests to run]"
	res, err := svc.Test(ctx, "s1", "head", "", []string{"go", "test", "./pkg/", "-count=1"}, "", 0)
	if err != nil || res.Exit != 0 || res.Ran == nil || *res.Ran != 0 || res.Summary != "1 ok, 0 FAIL, 0 --- FAIL" {
		t.Fatalf("%+v err %v", res, err)
	}
	if data, _ := os.ReadFile(filepath.Join(runDir, "actions.jsonl")); !strings.Contains(string(data), `"noTests":true`) {
		t.Fatalf("a zero-test run is not marked, so it would count toward the cap:\n%s", data)
	}
	// Records of runs that ran nothing do not fill the cap; counted ones do.
	seed := func(counted, empty int) {
		var lines string
		for i := 1; i <= counted+empty; i++ {
			lines += `{"seq":` + strconv.Itoa(i) + `,"leg":"head","action":"test"` + map[bool]string{true: `,"noTests":true`}[i > counted] + "}\n"
		}
		if err := os.WriteFile(filepath.Join(runDir, "actions.jsonl"), []byte(lines), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	seed(maxTests-1, 4)
	is(t, "")(svc.Test(ctx, "s1", "head", "", []string{"go", "test", "./pkg/", "-count=1"}, "", 0))
	seed(maxTests, 4)
	is(t, "TESTING_TEST_LIMIT")(svc.Test(ctx, "s1", "head", "", []string{"go", "test", "./pkg/", "-count=1"}, "", 0))
}

func TestTestsRan(t *testing.T) {
	goTest, vet := []string{"go", "test", "./..."}, []string{"go", "vet", "./..."}
	for _, c := range []struct {
		name string
		argv []string
		out  string
		want int // -1: the runner prints no counts (nil)
	}{
		{"go packages that ran", goTest, "ok  \tfixture/a\t0.1s\nok  \tfixture/b\t(cached)\n?   \tfixture/c\t[no test files]\n", 2},
		{"go package with no tests to run", goTest, "ok  \tfixture/pkg\t0.001s [no tests to run]\n", 0},
		{"go package that did not build", goTest, "FAIL\tfixture/pkg [build failed]\n", 0},
		{"go package that did not set up", goTest, "FAIL\tfixture/pkg [setup failed]\n", 0},
		{"go package that failed a test", goTest, "--- FAIL: TestBad (0.00s)\nFAIL\nFAIL\tfixture/pkg\t0.1s\n", 1},
		{"go vet prints no counts", vet, "ok  \tfixture/pkg\n", -1},
		{"vitest", []string{"npx", "vitest", "run"}, " Test Files  1 failed | 2 passed (3)\n      Tests  1 failed | 13 passed (14)\n", 14},
		{"jest", []string{"npx", "jest"}, "Tests:       1 failed, 13 passed, 14 total\n", 14},
		{"npm without the script", []string{"npm", "test"}, "npm error Missing script: \"test\"\n", -1},
		{"npm script that prints a Tests line", []string{"npm", "test"}, "Tests:       2 passed, 2 total\n", 2},
		{"npx vitest that loaded no suite", []string{"npx", "vitest", "run"}, " Test Files  1 failed (1)\n      Tests  no tests\n", 0},
	} {
		got := -1
		if n := testsRan(c.argv, c.out); n != nil {
			got = *n
		}
		if got != c.want {
			t.Errorf("%s: testsRan = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestFailingTestAndJSCompileErrors(t *testing.T) {
	for out, want := range map[string]bool{
		"--- FAIL: TestBad (0.00s)\n":         true,
		"not ok 3 - adds\n":                   true,
		"  2 failing\n":                       true,
		" \u2717 adds two numbers\n":          true,
		"FAIL  src/a.test.ts\n":               false, // a suite that did not load names no test
		"      Tests  1 failed | 13 passed\n": false, // the counted runners are judged by their count
		"# pkg\nfoo.go:3:2: undefined: x\n":   false,
	} {
		if got := failingTest.MatchString(out); got != want {
			t.Errorf("failingTest(%q) = %v, want %v", out, got, want)
		}
	}
	for out, want := range map[string]bool{
		"Error: Failed to resolve import \"./gone\" from \"src/a.test.ts\"": true,
		"Error: Cannot find module './gone'":                                true,
		"SyntaxError: Unexpected token":                                     true,
		"src/a.test.ts(3,1): error TS2304: Cannot find name 'x'":            true,
		"AssertionError: expected 1 to be 2":                                false,
	} {
		if got := jsCompileErr.MatchString(out); got != want {
			t.Errorf("jsCompileErr(%q) = %v, want %v", out, got, want)
		}
	}
}

// TestTestVerbSmoke runs the verb on a real prepared checkout. It is off unless LITMUS_SMOKE_CHECKOUT names one
// (the repo clone dir of a head SHA); the run in the host daemon is never touched.
func TestTestVerbSmoke(t *testing.T) {
	checkout := os.Getenv("LITMUS_SMOKE_CHECKOUT")
	if checkout == "" {
		t.Skip("set LITMUS_SMOKE_CHECKOUT to a prepared clone to run this")
	}
	svc, now := newSvc(t)
	run := Run{ID: "run_smoke", SessionID: "smoke", State: RunPrepared, Deadline: now.Add(RunBudget), Legs: []Leg{{Name: "head", Worktree: checkout, State: "prepared"}}}
	is(t, "")(nil, svc.store.Save(run))
	res, err := svc.Test(ctx, "smoke", "head", "backend", []string{"go", "test", "./internal/cli/", "-run", "Paste", "-count=1"}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("SMOKE exit=%d timedOut=%t %.1fs summary=%q receipt=%s\n%s", res.Exit, res.TimedOut, res.Seconds, res.Summary, res.Receipt, res.Output)
	if res.Exit != 0 {
		t.Fatalf("exit %d", res.Exit)
	}
}

func TestPRTestFilesSelection(t *testing.T) {
	list := "# status\tadded\tdeleted\tpath\t[from]\n" +
		"M\t1\t1\tbackend/internal/cli/hooks.go\n" +
		"M\t1\t1\tbackend/internal/cli/hooks_test.go\n" +
		"A\t1\t0\tfrontend/src/new.test.tsx\n" +
		"M\t1\t1\tfrontend/src/a b.spec.ts\n" +
		"A\t1\t0\tfrontend/src/__tests__/deep/util.ts\n" +
		"D\t0\t1\tbackend/old_test.go\n" +
		"R\t1\t1\tbackend/moved_test.go\tbackend/was.go\n" +
		"M\t-\t-\tfrontend/src/shot.spec.tsx\n" +
		"M\t1\t1\tfrontend/src/latest.tsx\n" +
		"M\t1\t1\tfrontend/src/helpers.go\n" +
		"M\t1\t1\t../escape_test.go\n" +
		"M\t1\t1\tbackend/internal/cli/hooks_test.go\n"
	want := []string{
		"backend/internal/cli/hooks_test.go", "frontend/src/new.test.tsx", "frontend/src/a b.spec.ts",
		"frontend/src/__tests__/deep/util.ts", "backend/moved_test.go", "frontend/src/shot.spec.tsx",
	}
	if got := prTestFiles(list); !slices.Equal(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if got := prTestFiles("M\t1\t1\tx.go\n"); len(got) != 0 {
		t.Fatalf("non-test file selected %q", got)
	}
	if got := prTestFiles(""); len(got) != 0 {
		t.Fatalf("empty list selected %q", got)
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// headTestsRun is a service with a prepared base checkout (a real git repo whose Add returns 0) and a head checkout
// holding the PR's test file pkg/fix_test.go; diff.patch in the run dir lists the files the PR changed.
func headTestsRun(t *testing.T, fixTest string, diffFiles ...string) (svc *Service, runDir, base string) {
	t.Helper()
	svc, now := newSvc(t)
	svc.runner = execRunner{} // the scratch worktree is real git
	run := Run{ID: "run_s1", SessionID: "s1", State: RunPrepared, Deadline: now.Add(RunBudget)}
	for _, leg := range []string{"base", "head"} {
		dir := filepath.Join(t.TempDir(), "checkout")
		if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o750); err != nil {
			t.Fatal(err)
		}
		files := map[string]string{"go.mod": "module fixture\n\ngo 1.21\n", "pkg/pkg.go": "package pkg\n\nfunc Add(a, b int) int { return 0 }\n", "pkg/pkg_test.go": passingTest}
		if leg == "head" {
			files["pkg/fix_test.go"] = fixTest
		}
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if leg == "base" {
			base = dir
			gitIn(t, dir, "init", "-q")
			gitIn(t, dir, "add", "-A")
			gitIn(t, dir, "commit", "-qm", "main")
		}
		run.Legs = append(run.Legs, Leg{Name: leg, Worktree: dir, State: "prepared"})
	}
	is(t, "")(nil, svc.store.Save(run))
	runDir = filepath.Join(svc.store.root, run.ID)
	var list strings.Builder
	for _, f := range diffFiles {
		list.WriteString("M\t1\t0\t" + f + "\n")
	}
	if err := os.MkdirAll(runDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "files.txt"), []byte(list.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return svc, runDir, base
}

const fixTest = `package pkg

import "testing"

func TestFix(t *testing.T) {
	if got := Add(1, 1); got != 2 {
		t.Fatalf("Add(1, 1) = %d", got)
	}
}
`

// assertScratchGone checks the worktree lifecycle ended: the scratch dir is gone, git no longer lists it, and the
// base checkout has no new file and no change.
func assertScratchGone(t *testing.T, runDir, base string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(runDir, "base-tests")); !os.IsNotExist(err) {
		t.Errorf("base-tests still exists: %v", err)
	}
	if list := gitIn(t, base, "worktree", "list", "--porcelain"); strings.Count(list, "worktree ") != 1 {
		t.Errorf("git still lists a scratch worktree:\n%s", list)
	}
	if st := gitIn(t, base, "status", "--porcelain"); st != "" {
		t.Errorf("base checkout is dirty:\n%s", st)
	}
	if _, err := os.Stat(filepath.Join(base, "pkg", "fix_test.go")); !os.IsNotExist(err) {
		t.Errorf("the PR's test file leaked into the base checkout: %v", err)
	}
}

func TestTestVerbWithTestsFromRunsThePRsTestsOnBase(t *testing.T) {
	svc, runDir, base := headTestsRun(t, fixTest, "pkg/pkg.go", "pkg/fix_test.go")
	c, err := svc.CheckAdd(ctx, "s1", "fix", "the PR's test passes", "base")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CheckStart(ctx, "s1", c.ID); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Test(ctx, "s1", "base", "", []string{"go", "test", "./pkg/", "-count=1", "-v"}, "head", 0)
	if err != nil || res.Exit == 0 || res.Summary != "0 ok, 1 FAIL, 1 --- FAIL" || !strings.Contains(res.Output, "Add(1, 1) = 0") {
		t.Fatalf("%+v err %v", res, err)
	}
	if res.WithTestsFrom != "head" || !slices.Equal(res.CopiedFiles, []string{"pkg/fix_test.go"}) {
		t.Fatalf("result lacks the copied files: %+v", res)
	}
	full, err := os.ReadFile(res.Receipt)
	if err != nil || !strings.HasPrefix(string(full), "test files copied from the PR's head checkout onto main's code:\n  pkg/fix_test.go\n") ||
		!strings.Contains(string(full), "$ go test ./pkg/ -count=1 -v   (in "+filepath.Join(runDir, "base-tests")) || !strings.Contains(string(full), "--- FAIL: TestFix") {
		t.Fatalf("receipt (%v):\n%s", err, full)
	}
	data, err := os.ReadFile(filepath.Join(runDir, "actions.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Leg, Action, WithTestsFrom string
		CopiedFiles                []string
		Exit                       *int
	}
	line, _, _ := strings.Cut(string(data), "\n")
	if err := json.Unmarshal([]byte(line), &rec); err != nil || rec.Leg != "base" || rec.Action != "test" || rec.WithTestsFrom != "head" || !slices.Equal(rec.CopiedFiles, []string{"pkg/fix_test.go"}) || rec.Exit == nil || *rec.Exit == 0 {
		t.Fatalf("actions.jsonl %q: %+v err %v", line, rec, err)
	}
	if got, _ := svc.store.Checks("run_s1"); got[0].Receipts[0].OutputPath != res.Receipt {
		t.Fatalf("the open check did not get the receipt: %+v", got)
	} else if r := got[0].Receipts[0]; !r.failedTest() || r.NotBuilt || r.Ran == nil || *r.Ran != 1 {
		t.Fatalf("a test that compiled, ran and failed on main must read as one: %+v", r)
	}
	assertScratchGone(t, runDir, base)
}

func TestTestVerbWithTestsFromPassesWhenMainAlreadyHasTheFix(t *testing.T) {
	svc, runDir, base := headTestsRun(t, "package pkg\n\nimport \"testing\"\n\nfunc TestFix(t *testing.T) {}\n", "pkg/fix_test.go")
	res, err := svc.Test(ctx, "s1", "base", "", []string{"go", "test", "./pkg/", "-count=1"}, "head", 0)
	if err != nil || res.Exit != 0 || res.Summary != "1 ok, 0 FAIL, 0 --- FAIL" {
		t.Fatalf("%+v err %v", res, err)
	}
	assertScratchGone(t, runDir, base)
}

func TestTestVerbWithTestsFromReportsTestFilesThatDoNotCompile(t *testing.T) {
	svc, runDir, base := headTestsRun(t, "package pkg\n\nimport \"testing\"\n\nfunc TestNew(t *testing.T) { NewThing() }\n", "pkg/fix_test.go")
	res, err := svc.Test(ctx, "s1", "base", "", []string{"go", "test", "./pkg/", "-count=1"}, "head", 0)
	if err != nil || res.Exit == 0 || res.Summary != "1 test file from the PR does not compile against main" || !res.NotBuilt || res.Ran == nil || *res.Ran != 0 {
		t.Fatalf("%+v err %v", res, err)
	}
	if !strings.Contains(res.Output, "undefined: NewThing") {
		t.Fatalf("output lacks the compiler error:\n%s", res.Output)
	}
	assertScratchGone(t, runDir, base)

	if got := uncompiledTestFiles("FAIL\tx [build failed]\nbackend/a_test.go:1:2: undefined: X\n./b_test.go:3: bad\nc.go:4:1: oops\n", []string{"backend/a_test.go", "pkg/b_test.go", "pkg/d_test.go"}); got != 2 {
		t.Errorf("uncompiled = %d, want 2", got)
	}
	if got := uncompiledTestFiles("--- FAIL: TestX\na_test.go:9: boom\n", []string{"a_test.go"}); got != 0 {
		t.Errorf("a runtime failure counted as a compile error: %d", got)
	}
}

func TestTestVerbWithTestsFromRemovesTheWorktreeWhenSetupFails(t *testing.T) {
	svc, runDir, base := headTestsRun(t, fixTest, "pkg/fix_test.go", "pkg/gone_test.go") // gone_test.go is not in the head checkout
	is(t, "TESTING_TEST_FAILED")(svc.Test(ctx, "s1", "base", "", []string{"go", "test", "./pkg/", "-count=1"}, "head", 0))
	assertScratchGone(t, runDir, base)

	svc, runDir, base = headTestsRun(t, fixTest, "pkg/fix_test.go") // a bad --dir in the base commit
	if _, err := svc.testWithHeadTests(ctx, runDir, base, base, "nope", []string{"pkg/pkg_test.go"}, []string{"go", "test"}, nil, time.Minute, filepath.Join(runDir, "r.txt")); err == nil {
		t.Fatal("a folder missing from the base commit was accepted")
	}
	assertScratchGone(t, runDir, base)
}

func TestTestVerbWithTestsFromReplacesALeftoverWorktree(t *testing.T) {
	svc, runDir, base := headTestsRun(t, fixTest, "pkg/fix_test.go")
	gitIn(t, base, "worktree", "add", "--detach", filepath.Join(runDir, "base-tests"), "HEAD") // a daemon died mid-run
	res, err := svc.Test(ctx, "s1", "base", "", []string{"go", "test", "./pkg/", "-count=1"}, "head", 0)
	if err != nil || res.Exit == 0 {
		t.Fatalf("%+v err %v", res, err)
	}
	assertScratchGone(t, runDir, base)
}

func TestTestVerbWithTestsFromLinksNodeModulesAndLeavesThemAlone(t *testing.T) {
	svc, runDir, base := headTestsRun(t, fixTest, "pkg/fix_test.go")
	// frontend/ is committed (so the worktree has it); its node_modules is not (so only the symlink provides it).
	if err := os.MkdirAll(filepath.Join(base, "frontend"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "frontend", "keep"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, base, "add", "frontend/keep")
	gitIn(t, base, "commit", "-qm", "frontend")
	if err := os.MkdirAll(filepath.Join(base, "frontend", "node_modules", "dep"), 0o750); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(base, "frontend", "node_modules", "dep", "x.js")
	if err := os.WriteFile(marker, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte("#!/bin/sh\nls frontend/node_modules/dep/x.js\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	res, err := svc.testWithHeadTests(ctx, runDir, base, base, "", []string{"pkg/pkg_test.go"}, []string{"npm", "test"}, env, time.Minute, filepath.Join(runDir, "r.txt"))
	if err != nil || res.Exit != 0 {
		t.Fatalf("%+v err %v", res, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the base checkout's node_modules was touched: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runDir, "base-tests")); !os.IsNotExist(err) {
		t.Fatalf("worktree left behind: %v", err)
	}
}

func TestTestVerbWithTestsFromRefusals(t *testing.T) {
	svc, runDir, base := headTestsRun(t, fixTest, "pkg/fix_test.go")
	argv := []string{"go", "test", "./pkg/", "-count=1"}
	is(t, "TESTING_TEST_NOT_ALLOWED")(svc.Test(ctx, "s1", "head", "", argv, "head", 0))
	is(t, "TESTING_TEST_NOT_ALLOWED")(svc.Test(ctx, "s1", "base", "", argv, "base", 0))
	is(t, "TESTING_TEST_NOT_ALLOWED")(svc.Test(ctx, "s1", "base", "", []string{"sh", "-c", "echo hi"}, "head", 0))
	if err := os.WriteFile(filepath.Join(runDir, "files.txt"), []byte("M\t1\t1\tpkg/pkg.go\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Test(ctx, "s1", "base", "", argv, "head", 0)
	var e *apierr.Error
	if !errors.As(err, &e) || e.Code != "TESTING_TEST_NOT_ALLOWED" || !strings.Contains(e.Message, "the PR adds no test files") {
		t.Fatalf("err = %v", err)
	}
	if err := os.Remove(filepath.Join(runDir, "files.txt")); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Test(ctx, "s1", "base", "", argv, "head", 0)
	if !errors.As(err, &e) || e.Code != "TESTING_TEST_NOT_ALLOWED" || !strings.Contains(e.Message, "no files.txt") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(runDir, "base-tests")); !os.IsNotExist(err) {
		t.Fatalf("a refused call made a worktree: %v", err)
	}
	if st := gitIn(t, base, "status", "--porcelain"); st != "" {
		t.Fatalf("base dirty after refusals: %s", st)
	}
	if acts := readActions(runDir); len(acts) != 0 {
		t.Fatalf("a refused call was recorded: %+v", acts)
	}
}

// TestTestVerbWithPRTestsSmoke runs the PR's own test files against main's code on real checkouts. It is off unless
// LITMUS_SMOKE_HEAD (the head clone), LITMUS_SMOKE_BASE (the base clone) and LITMUS_SMOKE_DIFF (the run's diff.patch)
// are set; no daemon is involved.
func TestTestVerbWithPRTestsSmoke(t *testing.T) {
	head, base, diff := os.Getenv("LITMUS_SMOKE_HEAD"), os.Getenv("LITMUS_SMOKE_BASE"), os.Getenv("LITMUS_SMOKE_DIFF")
	if head == "" || base == "" || diff == "" {
		t.Skip("set LITMUS_SMOKE_HEAD, LITMUS_SMOKE_BASE and LITMUS_SMOKE_DIFF to run this")
	}
	svc, now := newSvc(t)
	run := Run{ID: "run_smoke", SessionID: "smoke", State: RunPrepared, Deadline: now.Add(RunBudget), Legs: []Leg{{Name: "head", Worktree: head, State: "prepared"}, {Name: "base", Worktree: base, State: "prepared"}}}
	is(t, "")(nil, svc.store.Save(run))
	data, err := os.ReadFile(diff)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.store.WriteFile("run_smoke", "diff.patch", data); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Test(ctx, "smoke", "base", "backend", []string{"go", "test", "./internal/cli/", "-run", "Paste|Report", "-count=1"}, "head", 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("SMOKE exit=%d timedOut=%t %.1fs copied=%v summary=%q receipt=%s", res.Exit, res.TimedOut, res.Seconds, res.CopiedFiles, res.Summary, res.Receipt)
	receipt, _ := os.ReadFile(res.Receipt)
	t.Logf("SMOKE RECEIPT\n%s", receipt)
	if st := gitIn(t, base, "status", "--porcelain"); strings.Contains(st, "_test.go") {
		t.Errorf("base checkout got a test file:\n%s", st)
	}
	if res.Exit == 0 {
		t.Errorf("the PR's tests passed on main (exit 0); expected a failure")
	}
}

func TestTestVerbMinutesReplacesTheDefaultCap(t *testing.T) {
	svc, _ := testLeg(t, `package pkg

import (
	"testing"
	"time"
)

func TestSlowButFine(t *testing.T) { time.Sleep(3 * time.Second) }
`)
	for _, minutes := range []int{-1, MaxTestMin + 1} {
		is(t, "TESTING_TEST_MINUTES_INVALID")(svc.Test(ctx, "s1", "head", "", []string{"go", "test", "./pkg/"}, "", minutes))
	}
	svc.testCap = time.Second
	res, err := svc.Test(ctx, "s1", "head", "", []string{"go", "test", "./pkg/", "-count=1"}, "", 1)
	if err != nil || res.TimedOut || res.Exit != 0 {
		t.Fatalf("--minutes 1 should outlast a 1s default cap: %+v err %v", res, err)
	}
}
