//go:build integration

package integration_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// doctorReport mirrors what "cernbox doctor --output json" produces.
type doctorReport struct {
	Checks []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Detail string `json:"detail"`
		Remedy string `json:"remedy"`
	} `json:"checks"`
	Problems int `json:"problems"`
	Warnings int `json:"warnings"`
}

func (r doctorReport) check(t *testing.T, name string) (status, detail, remedy string) {
	t.Helper()
	for _, c := range r.Checks {
		if c.Name == name {
			return c.Status, c.Detail, c.Remedy
		}
	}
	t.Fatalf("no %q check in the report: %+v", name, r.Checks)
	return "", "", ""
}

// TestDoctorChecksThisDeployment runs the doctor against the real thing.
//
// It does not require a clean bill of health, and that is deliberate: a test
// that demanded everything be green would have to be disabled the moment it
// found something, which is the opposite of what this command is for. What it
// requires is that every check ran, reached a verdict, and named a remedy for
// whatever it did not like.
func TestDoctorChecksThisDeployment(t *testing.T) {
	e := setup(t)

	stdout, stderr, _ := e.run("--output", "json", "doctor")
	var rep doctorReport
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("doctor produced no JSON: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}

	// The checks that must work here, whatever else does not: if any of these
	// failed, nothing else in this suite could have passed.
	for _, name := range []string{"endpoint", "clock", "credentials", "identity", "spaces", "write"} {
		status, detail, _ := rep.check(t, name)
		if status != "ok" {
			t.Errorf("%s = %s (%s), and the rest of this suite could not have passed", name, status, detail)
		}
	}

	// Quota and trash have to reach a verdict, but which one depends on the
	// state of the storage rather than on anything this test controls.
	for _, name := range []string{"quota", "trash", "editor"} {
		if status, _, _ := rep.check(t, name); status == "" {
			t.Errorf("%s reached no verdict", name)
		}
	}

	// Anything the doctor was unhappy about has to say what to do next. That is
	// the whole difference between this command and "status".
	for _, c := range rep.Checks {
		if (c.Status == "problem" || c.Status == "warning") && c.Remedy == "" {
			t.Errorf("%s is a %s with no remedy: %q", c.Name, c.Status, c.Detail)
		}
	}
}

// TestDoctorLeavesNothingBehind is the condition for running this daily: the
// write probe has to be gone from the tree and from the recycle bin, since a
// deletion goes to the bin rather than away.
func TestDoctorLeavesNothingBehind(t *testing.T) {
	e := setup(t)

	before := e.mustRun("trash", "list", "--since", "1d")
	e.run("doctor")

	for _, name := range e.names(homeRoot) {
		if strings.Contains(name, "cernbox-doctor") {
			t.Errorf("the probe was left in the home directory: %s", name)
		}
	}

	after := e.mustRun("trash", "list", "--since", "1d")
	if n := strings.Count(after, "cernbox-doctor"); n > 0 {
		t.Errorf("%d probe entries left in the recycle bin\nbefore:\n%s\nafter:\n%s",
			n, before, after)
	}
}

// TestDoctorSkipWriteChangesNothing is what makes the command safe to run
// against an account you do not own.
func TestDoctorSkipWriteChangesNothing(t *testing.T) {
	e := setup(t)

	stdout, stderr, _ := e.run("--output", "json", "doctor", "--skip-write")
	var rep doctorReport
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("doctor produced no JSON: %v\nstderr:\n%s", err, stderr)
	}

	status, detail, _ := rep.check(t, "write")
	if status != "skipped" || !strings.Contains(detail, "--skip-write") {
		t.Errorf("write = %s (%s), want it skipped and said so", status, detail)
	}
	for _, name := range e.names(homeRoot) {
		if strings.Contains(name, "cernbox-doctor") {
			t.Errorf("--skip-write wrote %s anyway", name)
		}
	}
}

// TestDoctorFailsOnAnEndpointThatIsNotThere checks the report a user gets when
// nothing works: one failure with a remedy, a non-zero exit, and none of the
// checks that depend on it pretending to have an opinion.
func TestDoctorFailsOnAnEndpointThatIsNotThere(t *testing.T) {
	e := setup(t)

	a := e.self()
	a.endpoint = "https://127.0.0.1:1"
	stdout, _, code := e.runAs(a, "doctor")
	if code == 0 {
		t.Fatalf("doctor against a dead endpoint exited 0:\n%s", stdout)
	}
	if !strings.Contains(stdout, "endpoint") {
		t.Errorf("the endpoint was not named:\n%s", stdout)
	}
	for _, name := range []string{"credentials", "write", "quota", "trash"} {
		if strings.Contains(stdout, name) {
			t.Errorf("%s was reported against a server that does not answer:\n%s", name, stdout)
		}
	}
}
