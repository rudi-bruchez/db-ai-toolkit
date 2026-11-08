package lab

import (
	"context"
	"strings"
	"testing"

	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/godta/conn"
	"github.com/rudi-bruchez/db-ai-toolkit/tools/internal/sqlq"
)

func labProfile() sqlq.Profile {
	return sqlq.Profile{Name: "lab", Mode: sqlq.ModeReadWrite, Environment: "lab"}
}

func TestCheckProfileAcceptsLab(t *testing.T) {
	if err := CheckProfile(labProfile()); err != nil {
		t.Errorf("CheckProfile: %v, want nil", err)
	}
}

func TestCheckProfileRefusesMissingEnvironment(t *testing.T) {
	p := labProfile()
	p.Environment = ""
	if err := CheckProfile(p); err == nil {
		t.Error("CheckProfile accepted a profile without environment")
	}
}

func TestCheckProfileRefusesCapitalizedLab(t *testing.T) {
	p := labProfile()
	p.Environment = "Lab"
	if err := CheckProfile(p); err == nil {
		t.Error(`CheckProfile accepted environment "Lab"`)
	}
}

func TestCheckProfileRefusesProd(t *testing.T) {
	p := labProfile()
	p.Environment = "prod"
	if err := CheckProfile(p); err == nil {
		t.Error(`CheckProfile accepted environment "prod"`)
	}
}

func TestCheckProfileRefusesReadOnly(t *testing.T) {
	p := labProfile()
	p.Mode = sqlq.ModeReadOnly
	if err := CheckProfile(p); err == nil {
		t.Error("CheckProfile accepted a readonly profile")
	}
}

func instance(server, machine string) conn.Instance {
	return conn.Instance{ServerName: server, MachineName: machine, Major: 16, Level: "RTM", Version: "16.0.4265.3"}
}

func TestCheckDistinctAcceptsDifferent(t *testing.T) {
	if err := CheckDistinct(instance("SRC", "SRCHOST"), instance("LAB", "LABHOST")); err != nil {
		t.Errorf("CheckDistinct: %v, want nil", err)
	}
}

func TestCheckDistinctRefusesSameMachineDifferentCase(t *testing.T) {
	// Two named instances on one host: the server names differ, the machine
	// is the same, spelled in another case.
	if err := CheckDistinct(instance(`HOST\SRC`, "HOST"), instance(`HOST\LAB`, "host")); err == nil {
		t.Error("CheckDistinct accepted the same machine name in another case")
	}
}

func TestCheckDistinctRefusesSameServerNameDifferentCase(t *testing.T) {
	if err := CheckDistinct(instance("SQL01", "HOSTA"), instance("sql01", "HOSTB")); err == nil {
		t.Error("CheckDistinct accepted the same server name in another case")
	}
}

func TestCheckDistinctRefusesEmptyIdentity(t *testing.T) {
	// @@SERVERNAME can be NULL (Identify reads it as ""): nothing to compare.
	if err := CheckDistinct(instance("", "SRCHOST"), instance("LAB", "LABHOST")); err == nil {
		t.Error("CheckDistinct accepted an empty server name")
	}
}

func TestCheckSameVersionAcceptsIdentical(t *testing.T) {
	warn, err := CheckSameVersion(instance("SRC", "A"), instance("LAB", "B"))
	if err != nil || warn != "" {
		t.Errorf("CheckSameVersion: warning %q, error %v; want none", warn, err)
	}
}

func TestCheckSameVersionRefusesOtherMajor(t *testing.T) {
	lab := instance("LAB", "B")
	lab.Major = 17
	if _, err := CheckSameVersion(instance("SRC", "A"), lab); err == nil {
		t.Error("CheckSameVersion accepted another major version")
	}
}

func TestCheckSameVersionWarnsOnLevel(t *testing.T) {
	lab := instance("LAB", "B")
	lab.Level = "SP1"
	warn, err := CheckSameVersion(instance("SRC", "A"), lab)
	if err != nil {
		t.Fatalf("CheckSameVersion refused a level difference: %v", err)
	}
	if !strings.Contains(warn, "SP1") {
		t.Errorf("warning %q, want one naming the lab level SP1", warn)
	}
}

func TestCheckSameVersionWarnsOnVersion(t *testing.T) {
	lab := instance("LAB", "B")
	lab.Version = "16.0.4250.1"
	warn, err := CheckSameVersion(instance("SRC", "A"), lab)
	if err != nil {
		t.Fatalf("CheckSameVersion refused a build difference: %v", err)
	}
	if !strings.Contains(warn, "16.0.4250.1") {
		t.Errorf("warning %q, want one naming the lab build 16.0.4250.1", warn)
	}
}

func TestCheckCloneDBRefusesNonRunName(t *testing.T) {
	// A nil Querier: the name is refused before any query is sent.
	if err := CheckCloneDB(context.Background(), nil, "PachaDataFormation"); err == nil {
		t.Error("CheckCloneDB accepted a name outside goDTA's form")
	}
}
