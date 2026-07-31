package stdio

import "testing"

func FuzzDecodeRequestNeverPanics(f *testing.F) {
	f.Add([]byte(`{"v":1,"id":"seed","type":"command","method":"relay.status"}`))
	f.Add([]byte(`{"v":1,"id":"x","id":"y","type":"command","method":"relay.status"}`))
	f.Add([]byte("not-json"))
	f.Fuzz(func(t *testing.T, frame []byte) {
		_, _ = decodeRequest(frame)
	})
}
