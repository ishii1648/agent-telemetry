package serverclient

import (
	"fmt"
	"strconv"

	"google.golang.org/protobuf/proto"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// marshalOTLPLogsProtobuf serializes the same logical OTLP Logs payload that
// the JSON path sends, but as binary protobuf (Content-Type:
// application/x-protobuf). Datadog's direct OTLP Logs intake requires the
// protobuf exporter — JSON is not accepted on /v1/logs — so this is the wire
// encoding for `encoding = "protobuf"` targets.
//
// We deliberately convert from the existing otlpPayload struct (built by
// buildOTLPPayload) rather than adopting the full OTel log SDK exporter: that
// keeps the per-target cursor, batching, and OTLP partialSuccess handling in
// flush.go unchanged, and lets a single payload be emitted as either JSON or
// protobuf depending on the destination. See
// issues/0042-feat-flush-export-target-array.md.
func marshalOTLPLogsProtobuf(p otlpPayload) ([]byte, error) {
	msg, err := toProtoLogs(p)
	if err != nil {
		return nil, err
	}
	return proto.Marshal(msg)
}

// toProtoLogs maps our JSON-shaped otlpPayload onto the generated OTLP proto
// LogsData message. The two are structurally identical (resourceLogs →
// scopeLogs → logRecords); only the value typing differs (our timestamps and
// int attributes are carried as strings, matching OTLP/JSON, and are parsed
// back to their numeric proto types here).
func toProtoLogs(p otlpPayload) (*logspb.LogsData, error) {
	out := &logspb.LogsData{ResourceLogs: make([]*logspb.ResourceLogs, 0, len(p.ResourceLogs))}
	for _, rl := range p.ResourceLogs {
		protoRL := &logspb.ResourceLogs{
			Resource:  &resourcepb.Resource{Attributes: toProtoAttrs(rl.Resource.Attributes)},
			ScopeLogs: make([]*logspb.ScopeLogs, 0, len(rl.ScopeLogs)),
		}
		for _, sl := range rl.ScopeLogs {
			protoSL := &logspb.ScopeLogs{
				Scope:      &commonpb.InstrumentationScope{Name: sl.Scope.Name},
				LogRecords: make([]*logspb.LogRecord, 0, len(sl.LogRecords)),
			}
			for _, lr := range sl.LogRecords {
				t, err := parseUint64(lr.TimeUnixNano)
				if err != nil {
					return nil, fmt.Errorf("timeUnixNano: %w", err)
				}
				ot, err := parseUint64(lr.ObservedTimeUnixNano)
				if err != nil {
					return nil, fmt.Errorf("observedTimeUnixNano: %w", err)
				}
				protoSL.LogRecords = append(protoSL.LogRecords, &logspb.LogRecord{
					TimeUnixNano:         t,
					ObservedTimeUnixNano: ot,
					SeverityNumber:       logspb.SeverityNumber(lr.SeverityNumber),
					EventName:            lr.EventName,
					Attributes:           toProtoAttrs(lr.Attributes),
				})
			}
			protoRL.ScopeLogs = append(protoRL.ScopeLogs, protoSL)
		}
		out.ResourceLogs = append(out.ResourceLogs, protoRL)
	}
	return out, nil
}

func toProtoAttrs(attrs []otlpAttribute) []*commonpb.KeyValue {
	if len(attrs) == 0 {
		return nil
	}
	out := make([]*commonpb.KeyValue, 0, len(attrs))
	for _, a := range attrs {
		out = append(out, &commonpb.KeyValue{Key: a.Key, Value: toProtoAnyValue(a.Value)})
	}
	return out
}

// toProtoAnyValue mirrors the omitempty-driven encoding of otlpValue: exactly
// one of StringValue / IntValue / BoolValue is meaningful per attribute (set by
// stringAttr / intAttr / boolAttr). IntValue is carried as a decimal string
// (OTLP/JSON convention) and parsed back to int64 here. A bare bool false and
// an empty string are valid values, so we fall through to a string value when
// no numeric/bool signal is present.
func toProtoAnyValue(v otlpValue) *commonpb.AnyValue {
	if v.IntValue != "" {
		if n, err := strconv.ParseInt(v.IntValue, 10, 64); err == nil {
			return &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: n}}
		}
	}
	if v.BoolValue {
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: true}}
	}
	return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v.StringValue}}
}

func parseUint64(s string) (uint64, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.ParseUint(s, 10, 64)
}
