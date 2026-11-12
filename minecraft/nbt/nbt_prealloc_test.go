package nbt

import (
	"bytes"
	"runtime"
	"testing"
)

// A list or int array that claims far more elements than the data holds must be refused without
// allocating for the claimed length.
func TestTruncatedLengthsDoNotPreallocate(t *testing.T) {
	cases := map[string]byte{"list": byte(tagSlice), "int array": byte(tagInt32Array), "long array": byte(tagInt64Array)}
	for name, tag := range cases {
		t.Run(name, func(t *testing.T) {
			var raw bytes.Buffer
			w := &offsetWriter{Writer: &raw, WriteByte: raw.WriteByte}
			_ = w.WriteByte(byte(tagStruct))
			_ = NetworkLittleEndian.WriteString(w, "")
			_ = w.WriteByte(tag)
			_ = NetworkLittleEndian.WriteString(w, "x")
			if tag == byte(tagSlice) {
				_ = w.WriteByte(byte(tagStruct))
			}
			_ = NetworkLittleEndian.WriteInt32(w, 1<<24)

			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			var data map[string]any
			err := NewDecoderWithEncoding(bytes.NewReader(raw.Bytes()), NetworkLittleEndian).Decode(&data)
			runtime.ReadMemStats(&after)
			if err == nil {
				t.Fatal("truncated data was accepted")
			}
			if n := after.TotalAlloc - before.TotalAlloc; n > 1<<20 {
				t.Fatalf("allocated %d bytes for %d bytes of input", n, raw.Len())
			}
		})
	}
}
