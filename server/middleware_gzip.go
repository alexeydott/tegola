package server

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// GZipHandler is responsible for determining if the incoming request should be served gzipped data.
// All response data is assumed to be compressed prior to being passed to this handler.
//
// If the incoming request has the "Accept-Encoding" header set with the values of "gzip" or "*",
// successful responses with a body are returned with the "Content-Encoding: gzip" header.
// Error and no-content responses are returned without a content encoding.
//
// If no "Accept-Encoding" header is present or "Accept-Encoding" has a value of "gzip;q=0" or
// "*;q=0" the response is decompressed prior to being sent to the client.
func GZipHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		acceptEncoding := r.Header.Get("Accept-Encoding")
		if acceptEncoding == "" {
			// decompress
			next.ServeHTTP(&gzipDecompressResponseWriter{resp: w}, r)
			return
		}

		decompress := false
		for _, v := range strings.Split(acceptEncoding, ",") {
			if (strings.Contains(v, "gzip") || strings.Contains(v, "*")) && strings.HasSuffix(v, ";q=0") {
				decompress = true
			}
		}

		if decompress {
			next.ServeHTTP(&gzipDecompressResponseWriter{resp: w}, r)
			return
		}

		next.ServeHTTP(&gzipResponseWriter{resp: w}, r)
		return
	})
}

// gzipResponseWriter delays setting Content-Encoding until the downstream
// handler has selected a successful response that can contain a body.
type gzipResponseWriter struct {
	status int
	resp   http.ResponseWriter
}

func (w *gzipResponseWriter) Header() http.Header {
	return w.resp.Header()
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.resp.Write(b)
}

func (w *gzipResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}

	w.status = status
	if status >= http.StatusOK && status < http.StatusMultipleChoices && status != http.StatusNoContent {
		w.resp.Header().Set("Content-Encoding", "gzip")
	} else {
		w.resp.Header().Del("Content-Encoding")
		if status == http.StatusNoContent {
			w.resp.Header().Del("Content-Length")
		}
	}
	w.resp.WriteHeader(status)
}

// gzipDecompressResponseWriter is responsible for decompressing responses
// when the http status code == 200.
type gzipDecompressResponseWriter struct {
	status int
	resp   http.ResponseWriter
}

func (w *gzipDecompressResponseWriter) Header() http.Header {
	return w.resp.Header()
}

func (w *gzipDecompressResponseWriter) Write(b []byte) (int, error) {
	//	check that we have an OK response, if not, don't process the body
	if w.status != http.StatusOK {
		return w.resp.Write(b)
	}

	//	setup new gzip reader
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	defer r.Close()

	var respSize int64
	respSize, err = io.Copy(w.resp, r)
	if err != nil {
		return 0, err
	}
	w.resp.Header().Set("Content-Length", fmt.Sprintf("%d", respSize))
	return int(respSize), nil
}

func (w *gzipDecompressResponseWriter) WriteHeader(i int) {
	w.resp.Header().Del("Content-Length")
	w.status = i
	w.resp.WriteHeader(i)
}
