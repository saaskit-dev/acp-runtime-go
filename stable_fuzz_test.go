package acpruntime

import (
	"encoding/json"
	"os"
	"testing"
)

func FuzzStablePermission(f *testing.F) {
	raw, err := os.ReadFile("testdata/acp/wire/permission-request.json")
	if err != nil {
		f.Fatal(err)
	}
	f.Add(raw)
	f.Add([]byte(`null`))
	f.Add([]byte(`{"sessionId":"s","toolCall":{"toolCallId":"t"},"options":[]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64*1024 {
			return
		}
		var req PermissionRequest
		if json.Unmarshal(data, &req) != nil {
			return
		}
		_, _ = json.Marshal(req)
		decision := defaultDenyPermissionDecision(req)
		if decision.Outcome == "selected" {
			found := false
			for _, o := range req.Options {
				if o.ID == decision.OptionID && (o.Kind == "reject_once" || o.Kind == "reject_always") {
					found = true
				}
			}
			if !found {
				t.Fatal("default deny selected an unoffered/nonreject option")
			}
		} else if decision.Outcome != "cancelled" {
			t.Fatal("default deny produced unknown outcome")
		}
		encoded, err := json.Marshal(decision)
		if err != nil {
			t.Fatal(err)
		}
		var roundtrip PermissionDecision
		if err := json.Unmarshal(encoded, &roundtrip); err != nil || roundtrip != decision {
			t.Fatalf("outcome roundtrip=%+v %v", roundtrip, err)
		}
		invalid := validatedPermissionDecision(req, PermissionDecision{Outcome: "allow", OptionID: "arbitrary"})
		if invalid.Outcome != "cancelled" {
			t.Fatal("unknown outcome authorized")
		}
	})
}
func FuzzStableConfig(f *testing.F) {
	for _, name := range []string{"boolean-option", "boolean-set"} {
		raw, err := os.ReadFile("testdata/acp/wire/" + name + ".json")
		if err != nil {
			f.Fatal(err)
		}
		f.Add(raw)
	}
	f.Add([]byte(`null`))
	f.Add([]byte(`{"id":"x","name":"X","type":"boolean","currentValue":null}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64*1024 {
			return
		}
		var option SessionConfigOption
		if json.Unmarshal(data, &option) == nil {
			if option.Type == "boolean" {
				if _, ok := option.Value.(bool); !ok {
					t.Fatal("boolean decoded as other type")
				}
			}
			_, _ = json.Marshal(option)
		}
		var request SetSessionConfigOptionRequest
		if json.Unmarshal(data, &request) == nil {
			if request.Type == "boolean" {
				if _, ok := request.Value.(bool); !ok {
					t.Fatal("boolean request decoded as other type")
				}
			}
			_, _ = json.Marshal(request)
		}
	})
}
func FuzzStableLoad(f *testing.F) {
	for _, seed := range []string{`{}`, `{"modes":{"currentModeId":"code","availableModes":[]}}`, `{"sessionId":"conflicting"}`, `null`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64*1024 {
			return
		}
		var response existingSessionWireResponse
		if json.Unmarshal(data, &response) != nil {
			return
		}
		normalized, err := response.normalized("requested-session")
		if err == nil {
			if normalized.SessionID != "requested-session" {
				t.Fatal("load rebound request identity")
			}
			_, _ = json.Marshal(normalized)
		}
	})
}
