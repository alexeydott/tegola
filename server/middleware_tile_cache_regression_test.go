package server

import (
	"bytes"
	"net/http/httptest"
	"testing"
)

func TestTileCacheResponseWriterCachesImplicitSuccessfulWrite(t *testing.T) {
	var cached bytes.Buffer
	recorder := httptest.NewRecorder()
	writer := newTileCacheResponseWriter(recorder, &cached)

	body := []byte("tile")
	if n, err := writer.Write(body); err != nil {
		t.Fatal(err)
	} else if n != len(body) {
		t.Fatalf("Write returned %d bytes, want %d", n, len(body))
	}

	if recorder.Code != 200 {
		t.Fatalf("response status = %d, want 200", recorder.Code)
	}
	if !bytes.Equal(cached.Bytes(), body) {
		t.Fatalf("cached body = %q, want %q", cached.Bytes(), body)
	}
	if !bytes.Equal(recorder.Body.Bytes(), body) {
		t.Fatalf("response body = %q, want %q", recorder.Body.Bytes(), body)
	}
}
