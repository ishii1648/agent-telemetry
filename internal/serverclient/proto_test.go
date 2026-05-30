package serverclient

import (
	"testing"

	"google.golang.org/protobuf/proto"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

// TestMarshalOTLPLogsProtobuf_RoundTrip verifies our JSON-shaped payload maps
// onto the OTLP proto message with timestamps and typed attribute values
// preserved: string, int (carried as a decimal string in JSON), and bool.
func TestMarshalOTLPLogsProtobuf_RoundTrip(t *testing.T) {
	p := otlpPayload{ResourceLogs: []resourceLog{{
		Resource: otlpResource{Attributes: []otlpAttribute{
			stringAttr("service.name", "agent-telemetry"),
		}},
		ScopeLogs: []scopeLog{{
			Scope: otlpScope{Name: "agent-telemetry/client"},
			LogRecords: []logRecord{{
				TimeUnixNano:         "1715600000000000000",
				ObservedTimeUnixNano: "1715600000000000000",
				SeverityNumber:       9,
				EventName:            "agent.session.started",
				Attributes: []otlpAttribute{
					stringAttr("session_id", "s1"),
					intAttr("local_sequence", 42),
					boolAttr("is_merged", true),
				},
			}},
		}},
	}}}

	raw, err := marshalOTLPLogsProtobuf(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got logspb.LogsData
	if err := proto.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.ResourceLogs) != 1 {
		t.Fatalf("resourceLogs: got %d", len(got.ResourceLogs))
	}
	rec := got.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	if rec.EventName != "agent.session.started" {
		t.Errorf("eventName: got %q", rec.EventName)
	}
	if rec.TimeUnixNano != 1715600000000000000 {
		t.Errorf("timeUnixNano: got %d", rec.TimeUnixNano)
	}
	if rec.SeverityNumber != logspb.SeverityNumber(9) {
		t.Errorf("severityNumber: got %d", rec.SeverityNumber)
	}

	attrs := map[string]*commonpb.AnyValue{}
	for _, kv := range rec.Attributes {
		attrs[kv.Key] = kv.Value
	}
	if v := attrs["session_id"].GetStringValue(); v != "s1" {
		t.Errorf("session_id: got %q", v)
	}
	if v := attrs["local_sequence"].GetIntValue(); v != 42 {
		t.Errorf("local_sequence: got %d, want 42 (int, not string)", v)
	}
	if v := attrs["is_merged"].GetBoolValue(); v != true {
		t.Errorf("is_merged: got %v", v)
	}
}
