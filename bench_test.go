package protojsonx

import (
	"testing"
	"time"

	"github.com/sudorandom/protojsonx/internal/testpb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func createUserProfile() *testpb.UserProfile {
	return &testpb.UserProfile{
		Id:       "12345",
		Email:    "alice@example.com",
		Name:     "Alice Smith",
		Age:      30,
		IsActive: true,
		Status:   testpb.UserStatus_STATUS_ACTIVE,
		Tags:     []string{"go", "protobuf", "json", "performance"},
		Metadata: map[string]string{
			"env":     "production",
			"region":  "us-west-2",
			"version": "1.4.2",
		},
		Address: &testpb.Address{
			Street:  "123 Main St",
			City:    "Seattle",
			State:   "WA",
			Zip:     "98101",
			Country: "USA",
		},
		Sessions: []*testpb.Session{
			{
				SessionId:      "sess-abc12345",
				LoginTimestamp: 1672531200,
				IpAddress:      "192.168.1.1",
			},
			{
				SessionId:      "sess-xyz67890",
				LoginTimestamp: 1672617600,
				IpAddress:      "192.168.1.2",
			},
		},
	}
}

func BenchmarkProtoJSON_Marshal(b *testing.B) {
	p := createUserProfile()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := protojson.Marshal(p)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkProtoJSON_Unmarshal(b *testing.B) {
	p := createUserProfile()
	data, err := protojson.Marshal(p)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var out testpb.UserProfile
		err := protojson.Unmarshal(data, &out)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkProtojsonx_Marshal(b *testing.B) {
	p := createUserProfile()
	_ = GetTable(p)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := Marshal(p)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkProtojsonx_Unmarshal(b *testing.B) {
	p := createUserProfile()
	data, err := Marshal(p)
	if err != nil {
		b.Fatal(err)
	}
	_ = GetTable(p)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var out testpb.UserProfile
		err := UnmarshalOptions{}.Unmarshal(data, &out)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkProtojsonx_ZeroCopy_Unmarshal(b *testing.B) {
	p := createUserProfile()
	data, err := Marshal(p)
	if err != nil {
		b.Fatal(err)
	}
	_ = GetTable(p)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var out testpb.UserProfile
		err := UnmarshalOptions{ZeroCopy: true}.Unmarshal(data, &out)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkProtoBinary_Marshal(b *testing.B) {
	p := createUserProfile()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := proto.Marshal(p)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkProtoBinary_Unmarshal(b *testing.B) {
	p := createUserProfile()
	data, err := proto.Marshal(p)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var out testpb.UserProfile
		err := proto.Unmarshal(data, &out)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func createBenchComplexMessage() *testpb.ComplexMessage {
	return &testpb.ComplexMessage{
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
		EnumField:      testpb.TestEnum_TEST_ENUM_FIRST,
		TimestampField: timestamppb.New(time.Date(2026, 6, 21, 8, 30, 0, 123000000, time.UTC)),
		DurationField:  durationpb.New(123*time.Second + 456*time.Millisecond),
		ChildField: &testpb.ChildMessage{
			Name:  "nested child",
			Value: 99,
		},
		RepeatedString: []string{"apple", "banana", "cherry"},
		RepeatedMessage: []*testpb.ChildMessage{
			{Name: "item1", Value: 10},
			{Name: "item2", Value: 20},
		},
		MapStringString: map[string]string{
			"key1": "value1",
			"key2": "value2",
		},
	}
}

func BenchmarkComplexProtoJSON_Marshal(b *testing.B) {
	p := createBenchComplexMessage()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := protojson.Marshal(p)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkComplexProtoJSON_Unmarshal(b *testing.B) {
	p := createBenchComplexMessage()
	data, err := protojson.Marshal(p)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var out testpb.ComplexMessage
		err := protojson.Unmarshal(data, &out)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkComplexProtojsonx_Marshal(b *testing.B) {
	p := createBenchComplexMessage()
	_ = GetTable(p)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := Marshal(p)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkComplexProtojsonx_Unmarshal(b *testing.B) {
	p := createBenchComplexMessage()
	data, err := Marshal(p)
	if err != nil {
		b.Fatal(err)
	}
	_ = GetTable(p)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var out testpb.ComplexMessage
		err := UnmarshalOptions{}.Unmarshal(data, &out)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkComplexProtojsonx_ZeroCopy_Unmarshal(b *testing.B) {
	p := createBenchComplexMessage()
	data, err := Marshal(p)
	if err != nil {
		b.Fatal(err)
	}
	_ = GetTable(p)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var out testpb.ComplexMessage
		err := UnmarshalOptions{ZeroCopy: true}.Unmarshal(data, &out)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkComplexProtoBinary_Marshal(b *testing.B) {
	p := createBenchComplexMessage()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := proto.Marshal(p)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkComplexProtoBinary_Unmarshal(b *testing.B) {
	p := createBenchComplexMessage()
	data, err := proto.Marshal(p)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var out testpb.ComplexMessage
		err := proto.Unmarshal(data, &out)
		if err != nil {
			b.Fatal(err)
		}
	}
}
