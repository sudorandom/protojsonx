package protojsonx

import (
	"bytes"
	"fmt"
	"math"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/sudorandom/protojsonx/pb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func createComplexMessage() *pb.ComplexMessage {
	return &pb.ComplexMessage{
		DoubleField:    123.456,
		FloatField:     78.9,
		Int32Field:     -42,
		Int64Field:     -9000000000,
		Uint32Field:    4200,
		Uint64Field:    90000000000,
		Sint32Field:    -55,
		Sint64Field:    -8800000,
		Fixed32Field:   999,
		Fixed64Field:   888888888,
		Sfixed32Field:  -111,
		Sfixed64Field:  -222222222,
		BoolField:      true,
		StringField:    "hello world \n \t \" \\",
		BytesField:     []byte{1, 2, 3, 4, 5},
		EnumField:      pb.TestEnum_TEST_ENUM_FIRST,
		TimestampField: timestamppb.New(time.Date(2026, 6, 21, 8, 30, 0, 123000000, time.UTC)),
		DurationField:  durationpb.New(123*time.Second + 456*time.Millisecond),
		ChildField: &pb.ChildMessage{
			Name:  "nested child",
			Value: 99,
		},
		RepeatedString: []string{"apple", "banana", "cherry"},
		RepeatedMessage: []*pb.ChildMessage{
			{Name: "item1", Value: 10},
			{Name: "item2", Value: 20},
		},
		MapStringString: map[string]string{
			"key1": "value1",
			"key2": "value2",
		},
	}
}

func TestComprehensiveShapes(t *testing.T) {
	msg := createComplexMessage()

	t.Run("Standard Marshal/Unmarshal roundtrip", func(t *testing.T) {
		// Marshal using protojsonx
		data, err := Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}

		// Unmarshal using protojsonx
		var out pb.ComplexMessage
		err = Unmarshal(data, &out)
		if err != nil {
			t.Fatal(err)
		}

		// Compare semantically
		if !proto.Equal(msg, &out) {
			t.Errorf("Roundtrip result not equal to original:\nOrig: %+v\nGot:  %+v", msg, &out)
		}
	})

	t.Run("Cross-compatibility with standard protojson", func(t *testing.T) {
		// Marshal using protojsonx
		data, err := Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}

		// Unmarshal using standard protojson
		var out pb.ComplexMessage
		err = protojson.Unmarshal(data, &out)
		if err != nil {
			t.Fatalf("protojson failed to parse protojsonx output: %v", err)
		}

		if !proto.Equal(msg, &out) {
			t.Errorf("protojson unmarshal of protojsonx output not equal to original")
		}

		// Marshal using standard protojson
		stdData, err := protojson.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}

		// Unmarshal using protojsonx
		var tableOut pb.ComplexMessage
		err = Unmarshal(stdData, &tableOut)
		if err != nil {
			t.Fatalf("protojsonx failed to parse protojson output: %v", err)
		}

		if !proto.Equal(msg, &tableOut) {
			t.Errorf("protojsonx unmarshal of protojson output not equal to original")
		}
	})

	t.Run("ZeroCopy Option", func(t *testing.T) {
		data, err := Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}

		var out pb.ComplexMessage
		err = UnmarshalOptions{ZeroCopy: true}.Unmarshal(data, &out)
		if err != nil {
			t.Fatal(err)
		}

		if !proto.Equal(msg, &out) {
			t.Errorf("ZeroCopy roundtrip not equal")
		}
	})

	t.Run("EmitUnpopulated Option", func(t *testing.T) {
		emptyMsg := &pb.ComplexMessage{}

		// Default: false
		dataDefault, err := Marshal(emptyMsg)
		if err != nil {
			t.Fatal(err)
		}
		if string(dataDefault) != "{}" {
			t.Errorf("Expected empty json '{}', got: %s", string(dataDefault))
		}

		// EmitUnpopulated: true
		dataEmit, err := MarshalOptions{EmitUnpopulated: true}.Marshal(emptyMsg)
		if err != nil {
			t.Fatal(err)
		}

		// Verify fields are present
		if !bytes.Contains(dataEmit, []byte(`"doubleField"`)) ||
			!bytes.Contains(dataEmit, []byte(`"int32Field"`)) ||
			!bytes.Contains(dataEmit, []byte(`"boolField"`)) {
			t.Errorf("Unpopulated fields missing in EmitUnpopulated output: %s", string(dataEmit))
		}
	})

	t.Run("UseProtoNames Option", func(t *testing.T) {
		data, err := MarshalOptions{UseProtoNames: true}.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}

		// Should use snake_case proto name "double_field" instead of json camelCase "doubleField"
		if !bytes.Contains(data, []byte(`"double_field"`)) {
			t.Errorf("UseProtoNames did not produce proto snake_case name: %s", string(data))
		}
	})

	t.Run("Float extreme values", func(t *testing.T) {
		extremeMsg := &pb.ComplexMessage{
			DoubleField: math.NaN(),
			FloatField:  float32(math.Inf(1)),
		}

		data, err := Marshal(extremeMsg)
		if err != nil {
			t.Fatal(err)
		}

		if !bytes.Contains(data, []byte(`"doubleField":"NaN"`)) ||
			!bytes.Contains(data, []byte(`"floatField":"Infinity"`)) {
			t.Errorf("NaN/Inf formatting incorrect: %s", string(data))
		}

		var out pb.ComplexMessage
		if err := Unmarshal(data, &out); err != nil {
			t.Fatalf("failed to unmarshal special floats: %v", err)
		}
		if !math.IsNaN(out.DoubleField) || !math.IsInf(float64(out.FloatField), 1) {
			t.Errorf("special floats did not round-trip: double=%v float=%v", out.DoubleField, out.FloatField)
		}
	})
}

func TestUnmarshalRejectsTrailingData(t *testing.T) {
	var out pb.Address
	if err := Unmarshal([]byte(`{"street":"123"} trailing`), &out); err == nil {
		t.Fatal("expected trailing data error")
	}
}

func TestDiscardUnknownRejectsInvalidJSONValue(t *testing.T) {
	var out pb.UserProfile
	err := (UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(`{"unknown":,"id":"123"}`), &out)
	if err == nil {
		t.Fatal("expected invalid unknown value to be rejected")
	}
}

func TestUnmarshalUnicodeEscapes(t *testing.T) {
	var out pb.Address
	if err := Unmarshal([]byte(`{"street":"\u20ac \ud83d\ude00"}`), &out); err != nil {
		t.Fatal(err)
	}
	if out.Street != "€ 😀" {
		t.Fatalf("unicode escapes decoded incorrectly: %q", out.Street)
	}
}

func TestUnmarshalRejectsMalformedStringEscapes(t *testing.T) {
	tests := [][]byte{
		[]byte(`{"street":"bad \u12"}`),
		[]byte("{\"street\":\"bad \n raw\"}"),
		[]byte(`{"street":"bad \q"}`),
	}

	for _, data := range tests {
		var out pb.Address
		if err := Unmarshal(data, &out); err == nil {
			t.Fatalf("expected malformed string error for %s", data)
		}
	}
}

func TestUnmarshalRejectsUnknownEnumName(t *testing.T) {
	var out pb.ComplexMessage
	if err := Unmarshal([]byte(`{"enumField":"NOT_A_REAL_ENUM"}`), &out); err == nil {
		t.Fatal("expected unknown enum error")
	}
}

func TestUnmarshalRejectsQuotedRegularFloat(t *testing.T) {
	var out pb.ComplexMessage
	if err := Unmarshal([]byte(`{"doubleField":"1.25"}`), &out); err == nil {
		t.Fatal("expected quoted non-special float error")
	}
}

func TestUnmarshalRejectsDuplicateFieldNames(t *testing.T) {
	userPayloads := [][]byte{
		[]byte(`{"id":"first","id":"second"}`),
		[]byte(`{"isActive":true,"is_active":false}`),
	}
	for _, data := range userPayloads {
		var out pb.UserProfile
		if err := Unmarshal(data, &out); err == nil {
			t.Fatalf("expected duplicate field error for user payload %s", data)
		}
	}

	var complex pb.ComplexMessage
	if err := Unmarshal([]byte(`{"doubleField":1,"double_field":2}`), &complex); err == nil {
		t.Fatal("expected duplicate field error for complex payload")
	}
}

func TestUnmarshalResetsExistingMessage(t *testing.T) {
	out := &pb.UserProfile{
		Id:   "old",
		Name: "stale",
		Metadata: map[string]string{
			"stale": "value",
		},
		Tags: []string{"old"},
	}

	if err := Unmarshal([]byte(`{"id":"new","metadata":{"fresh":"value"}}`), out); err != nil {
		t.Fatal(err)
	}
	if out.Id != "new" || out.Name != "" {
		t.Fatalf("scalar fields were not reset correctly: %+v", out)
	}
	if _, ok := out.Metadata["stale"]; ok {
		t.Fatalf("map retained stale entry: %+v", out.Metadata)
	}
	if len(out.Tags) != 0 {
		t.Fatalf("repeated field retained stale entries: %+v", out.Tags)
	}
}

func TestUnmarshalNullScalarsAndContainers(t *testing.T) {
	out := &pb.UserProfile{
		Id:       "old",
		Age:      42,
		IsActive: true,
		Tags:     []string{"stale"},
		Metadata: map[string]string{"stale": "value"},
	}

	data := []byte(`{"id":null,"age":null,"isActive":null,"tags":null,"metadata":null}`)
	if err := Unmarshal(data, out); err != nil {
		t.Fatal(err)
	}
	if out.Id != "" || out.Age != 0 || out.IsActive || out.Tags != nil || out.Metadata != nil {
		t.Fatalf("null fields were not cleared: id=%q age=%d active=%v tags=%v metadata=%v", out.Id, out.Age, out.IsActive, out.Tags, out.Metadata)
	}
}

func TestUnmarshalNullMessages(t *testing.T) {
	data := []byte(`{"childField":null,"timestampField":null,"durationField":null}`)
	var out pb.ComplexMessage
	if err := Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.ChildField != nil || out.TimestampField != nil || out.DurationField != nil {
		t.Fatalf("null message fields were not cleared: child=%v timestamp=%v duration=%v", out.ChildField, out.TimestampField, out.DurationField)
	}
}

func TestRepeatedMessageNullElementRoundTrip(t *testing.T) {
	msg := &pb.ComplexMessage{
		RepeatedMessage: []*pb.ChildMessage{
			nil,
			{Name: "after nil", Value: 7},
		},
	}

	data, err := Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"repeatedMessage":[null,`)) {
		t.Fatalf("expected nil repeated message element to marshal as null: %s", data)
	}

	var out pb.ComplexMessage
	if err := Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.RepeatedMessage) != 2 || out.RepeatedMessage[0] != nil || out.RepeatedMessage[1].GetName() != "after nil" {
		t.Fatalf("nil repeated message element did not round-trip: %+v", out.RepeatedMessage)
	}
}

func TestDurationPrecision(t *testing.T) {
	msg := &pb.ComplexMessage{
		DurationField: &durationpb.Duration{
			Seconds: 9007199254740991,
			Nanos:   123456789,
		},
	}

	data, err := Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"9007199254740991.123456789s"`)) {
		t.Fatalf("duration lost precision: %s", data)
	}

	var out pb.ComplexMessage
	if err := Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.DurationField.GetSeconds() != msg.DurationField.GetSeconds() ||
		out.DurationField.GetNanos() != msg.DurationField.GetNanos() {
		t.Fatalf("duration did not round-trip: got %v want %v", out.DurationField, msg.DurationField)
	}
}

func TestNegativeDurationRoundTrip(t *testing.T) {
	msg := &pb.ComplexMessage{
		DurationField: &durationpb.Duration{
			Seconds: 0,
			Nanos:   -500000000,
		},
	}

	data, err := Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"-0.500s"`)) {
		t.Fatalf("negative subsecond duration formatted incorrectly: %s", data)
	}

	var out pb.ComplexMessage
	if err := Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.DurationField.GetSeconds() != 0 || out.DurationField.GetNanos() != -500000000 {
		t.Fatalf("negative duration did not round-trip: %v", out.DurationField)
	}
}

func TestDiscardUnknownSkipsNestedMixedValues(t *testing.T) {
	data := []byte(`{"unknown":{"arr":[{"nested":true},["x",{"y":1}]],"keep":null},"id":"123"}`)
	var out pb.UserProfile
	if err := (UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Id != "123" {
		t.Fatalf("known field after unknown value was not decoded: id=%q", out.Id)
	}
}

func TestConcurrentColdTableUse(t *testing.T) {
	cacheMutex.Lock()
	delete(tableCache, reflect.TypeOf(pb.ComplexMessage{}))
	delete(tableCache, reflect.TypeOf(pb.ChildMessage{}))
	cacheMutex.Unlock()

	const workers = 128
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			<-start
			data, err := Marshal(createComplexMessage())
			if err != nil {
				errs <- err
				return
			}
			if string(data) == "{}" {
				errs <- fmt.Errorf("marshal used an empty table")
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestAddressAndSessions(t *testing.T) {
	addr := &pb.Address{
		Street:  "123 Main St",
		City:    "Seattle",
		State:   "WA",
		Zip:     "98101",
		Country: "USA",
	}

	data, err := Marshal(addr)
	if err != nil {
		t.Fatal(err)
	}

	var out pb.Address
	err = Unmarshal(data, &out)
	if err != nil {
		t.Fatal(err)
	}

	if out.Street != addr.Street || out.City != addr.City {
		t.Errorf("Address mismatch: %+v vs %+v", &out, addr)
	}
}

// Fuzz test to ensure the unsafe deserializer is memory-safe and never panics
func FuzzUnmarshal(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"doubleField":123.45,"int32Field":-42,"stringField":"hello","boolField":true}`))
	f.Add([]byte(`{"repeatedString":["a","b"],"mapStringString":{"k":"v"}}`))
	f.Add([]byte(`{"childField":{"name":"child","value":12},"repeatedMessage":[{"name":"c","value":1}]}`))
	f.Add([]byte(`{"timestampField":"2026-06-21T08:30:00Z","durationField":"123.456s"}`))
	f.Add([]byte(`{"doubleField":"NaN","floatField":"-Infinity"}`))
	f.Add([]byte(`{"stringField":"\u20ac \ud83d\ude00","repeatedString":["\u0000","line\nbreak"]}`))
	f.Add([]byte(`{"childField":null,"timestampField":null,"durationField":null}`))
	f.Add([]byte(`{"durationField":"-0.500s"}`))
	f.Add([]byte(`{"durationField":"9007199254740991.123456789s"}`))
	f.Add([]byte(`{"enumField":"NOT_A_REAL_ENUM"}`))
	f.Add([]byte(`{"doubleField":"1.25"}`))
	f.Add([]byte(`{"stringField":"bad \u12"}`))
	f.Add([]byte("{\"stringField\":\"bad \n raw\"}"))
	f.Add([]byte(`{"unknown":{"arr":[{"nested":true},["x",{"y":1}]],"keep":null},"stringField":"ok"}`))
	f.Add([]byte(`{"unknown":,"stringField":"bad"}`))
	f.Add([]byte(`{"doubleField":1,"double_field":2}`))
	f.Add([]byte(`{"stringField":null,"repeatedString":null,"mapStringString":null}`))
	f.Add([]byte(`{"repeatedMessage":[null,{"name":"after nil","value":7}]}`))
	f.Add([]byte(`{"stringField":"ok"} trailing`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var msg pb.ComplexMessage
		_ = Unmarshal(data, &msg)

		var msgZC pb.ComplexMessage
		_ = UnmarshalOptions{ZeroCopy: true}.Unmarshal(data, &msgZC)

		var addr pb.Address
		_ = Unmarshal(data, &addr)
	})
}

func TestReflectCompiles(t *testing.T) {
	tbl := GetTable(&pb.ComplexMessage{})
	if tbl == nil {
		t.Fatal("table compilation returned nil")
		return
	}

	if tbl.fieldMap["doubleField"] == nil || tbl.fieldMap["double_field"] == nil {
		t.Errorf("fieldMap lookup failed for double field")
	}
}
