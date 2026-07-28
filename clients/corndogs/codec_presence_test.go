package corndogs

import "testing"

func TestUpdateTaskPayloadPresenceRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		payload *[]byte
	}{
		{name: "absent"},
		{name: "present empty", payload: bytesPointer([]byte{})},
		{name: "present nonempty", payload: bytesPointer([]byte("new"))},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := UpdateTaskRequest{
				Uuid:            "id",
				Queue:           "q",
				CurrentState:    "old",
				AutoTargetState: "working",
				NewState:        "new",
				Payload:         test.payload,
			}
			decoded, err := DecodeUpdateTaskRequest(EncodeUpdateTaskRequest(request))
			if err != nil {
				t.Fatal(err)
			}
			if test.payload == nil {
				if decoded.Payload != nil {
					t.Fatal("absent payload became present")
				}
				return
			}
			if decoded.Payload == nil {
				t.Fatal("present payload became absent")
			}
			if string(*decoded.Payload) != string(*test.payload) {
				t.Fatalf("payload = %q, want %q", *decoded.Payload, *test.payload)
			}
		})
	}
}

func bytesPointer(value []byte) *[]byte {
	return &value
}
