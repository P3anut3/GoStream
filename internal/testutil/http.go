// Package testutil provides in-process net/http requests for HTTP contract tests.
package testutil

import (
	"io"
	"net/http"
	"net/http/httptest"
)

type Header struct{ Key, Value string }
type Body struct {
	Body io.Reader
	Len  int
}
type Recorder struct{ recorder *httptest.ResponseRecorder }
type Response struct {
	Header   responseHeader
	recorder *httptest.ResponseRecorder
}
type responseHeader struct{ header http.Header }

func (h responseHeader) ContentType() []byte { return []byte(h.header.Get("Content-Type")) }
func (r Response) StatusCode() int           { return r.recorder.Code }
func (r Response) Body() []byte              { return r.recorder.Body.Bytes() }
func (r Recorder) Result() Response          { return Response{responseHeader{r.recorder.Header()}, r.recorder} }

func PerformRequest(handler http.Handler, method, path string, body *Body, headers ...Header) Recorder {
	var reader io.Reader
	if body != nil {
		reader = body.Body
	}
	request := httptest.NewRequest(method, path, reader)
	if body != nil {
		request.ContentLength = int64(body.Len)
	}
	for _, header := range headers {
		request.Header.Set(header.Key, header.Value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if request.MultipartForm != nil {
		request.MultipartForm.RemoveAll()
	}
	return Recorder{recorder}
}
