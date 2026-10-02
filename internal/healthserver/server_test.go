package healthserver

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler(t *testing.T) {
	request := httptest.NewRequest("GET", "/health", nil)
	recorder := httptest.NewRecorder()
	Handler().ServeHTTP(recorder, request)
	if recorder.Code != 200 {
		t.Fatalf("unexpected status: %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `"status":"ok"`) {
		t.Fatalf("unexpected body: %s", recorder.Body.String())
	}
}
