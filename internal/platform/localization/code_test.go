package localization

import (
	"encoding/json"
	"testing"
)

func TestCode_String(t *testing.T) {
	if got := Code("HELLO").String(); got != "HELLO" {
		t.Errorf("String() = %q, want HELLO", got)
	}
}

func TestCode_JSONEncoding_IsPlainString(t *testing.T) {
	payload := struct {
		Code Code `json:"code"`
	}{Code: CodeInvalidRequest}

	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"code":"INVALID_REQUEST"}`
	if string(b) != want {
		t.Errorf("got %s, want %s", b, want)
	}
}
