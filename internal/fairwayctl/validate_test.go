package fairwayctl

import "testing"

func TestActionValidate_messageSend_allowsEmptyTargetAndProvider(t *testing.T) {
	action := Action{Type: ActionMessageSend}
	if err := action.Validate(); err != nil {
		t.Fatalf("Validate() error = %v; want nil", err)
	}
}

func TestActionValidate_telegramHandle_allowsEmptyTarget(t *testing.T) {
	action := Action{Type: ActionTelegramHandle}
	if err := action.Validate(); err != nil {
		t.Fatalf("Validate() error = %v; want nil", err)
	}
}

func TestActionValidate_httpForward_matchesDaemonSchemeCheck(t *testing.T) {
	action := Action{Type: ActionHTTPForward, URL: "http:///path-only"}
	if err := action.Validate(); err != nil {
		t.Fatalf("Validate() error = %v; want nil", err)
	}
}
