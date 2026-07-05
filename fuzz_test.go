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
	f.Add([]byte(`{"doubleField":-1.25e+10,"floatField":6.02e-23,"int64Field":"-9007199254740991","uint64Field":"18446744073709551615","bytesField":"AAECAwQ="}`))
	f.Add([]byte(`{"doubleField":01}`))
	f.Add([]byte(`{"floatField":1e,"int32Field":+12}`))
	f.Add([]byte(`{"uint32Field":-1,"uint64Field":"-1"}`))
	f.Add([]byte(`{"mapStringString":{"":"empty","quote\"key":"slash\\value","unicode\u20ac":"\ud83d\ude00","line\nkey":"tab\tvalue"}}`))
	f.Add([]byte(`{"repeatedString":["","alpha","quote\"","slash\\","unicode\u20ac","\ud83d\ude00"],"mapStringString":{"a":"1","b":"2"}}`))
	f.Add([]byte(`{"childField":{"name":"first","value":1},"repeatedMessage":[{"name":"a","value":1},null,{"name":"b","value":-2},{"name":"","value":0}]}`))
	f.Add([]byte(`{"timestampField":"0001-01-01T00:00:00Z","durationField":"315576000000.999999999s"}`))
	f.Add([]byte(`{"timestampField":"9999-12-31T23:59:59.999999999Z","durationField":"-315576000000.999999999s"}`))
	f.Add([]byte(`{"timestampField":"not-a-time","durationField":"1.0000000000s"}`))
	f.Add([]byte(`{"unknown":[{"deep":{"arr":[true,false,null,{"n":-12.34e-5}]}}],"childField":{"name":"kept","value":3}}`))
	f.Add([]byte(`{"childField":{"name":"x","name":"duplicate"},"repeatedMessage":[{"value":1,"value":2}]}`))
	f.Add([]byte(`{"doubleField":1,"double_field":2,"int64Field":"3","int64_field":"4"}`))
	f.Add([]byte(`{"stringField":null,"childField":{"name":null,"value":null},"repeatedMessage":null,"mapStringString":{}}`))
	f.Add([]byte(`{"repeatedString":[null],"mapStringString":{"k":null},"repeatedMessage":[{"name":null}]}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var msg testpb.ComplexMessage
		_ = Unmarshal(data, &msg)

		var addr testpb.Address
		_ = Unmarshal(data, &addr)
	})
}
