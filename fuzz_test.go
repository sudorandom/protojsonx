package protojsonx

import (
	"testing"

	"github.com/sudorandom/protojsonx/internal/testpb"
)

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
		var msg testpb.ComplexMessage
		_ = Unmarshal(data, &msg)

		var msgZC testpb.ComplexMessage
		_ = UnmarshalOptions{ZeroCopy: true}.Unmarshal(data, &msgZC)

		var addr testpb.Address
		_ = Unmarshal(data, &addr)
	})
}
