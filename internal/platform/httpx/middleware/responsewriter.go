package middleware

import (
	"bytes"
	"net/http"
)

// rwCapture wraps http.ResponseWriter to capture the status code and bytes
// written. Shared by AccessLog and Metrics middleware.
//
// When bodyLimit > 0 it also keeps a copy of the response body, capped at
// bodyLimit bytes. The cap is applied while buffering, not afterwards, so a
// large response never costs more than bodyLimit of memory per request.
type rwCapture struct {
	http.ResponseWriter
	status    int
	bytes     int
	body      bytes.Buffer
	bodyLimit int
}

func newRWCapture(w http.ResponseWriter) *rwCapture {
	return &rwCapture{ResponseWriter: w, status: http.StatusOK}
}

// newRWCaptureWithBody additionally buffers up to limit bytes of the response.
func newRWCaptureWithBody(w http.ResponseWriter, limit int) *rwCapture {
	return &rwCapture{ResponseWriter: w, status: http.StatusOK, bodyLimit: limit}
}

func (rw *rwCapture) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *rwCapture) Write(b []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(b)
	rw.bytes += n
	if room := rw.bodyLimit - rw.body.Len(); room > 0 {
		rw.body.Write(b[:min(room, n)])
	}
	return n, err
}

// capturedBody returns the buffered response bytes, or nil when capture is off.
func (rw *rwCapture) capturedBody() []byte {
	if rw.bodyLimit <= 0 {
		return nil
	}
	return rw.body.Bytes()
}
