package service

import "testing"

func TestValidateAddInput(t *testing.T) {
	name := "Heartbeat"
	command := "/bin/echo ok"
	workingDir := "/tmp"
	env := map[string]string{"FOO": "BAR"}
	if err := validateAddInput(ServiceInput{
		Name: &name, Command: &command, WorkingDir: &workingDir, Environment: &env,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestValidateAddInputRejectsInvalidEnvironment(t *testing.T) {
	name := "Heartbeat"
	command := "/bin/echo ok"
	env := map[string]string{"bad-key": "BAR"}
	if err := validateAddInput(ServiceInput{Name: &name, Command: &command, Environment: &env}); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateStoredServiceRejectsBadID(t *testing.T) {
	if err := validateStoredService(ServiceRecord{ID: "bad", Name: "Heartbeat", Command: "/bin/echo ok"}); err == nil {
		t.Fatal("expected validation error")
	}
}
