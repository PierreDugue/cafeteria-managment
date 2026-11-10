package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRoutes(t *testing.T) {
	s, err := New("test")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		path     string
		wantCode int
		wantBody string
	}{
		{"/", http.StatusOK, "Gestion cantine"},
		{"/healthz", http.StatusOK, "ok"},
		{"/static/style.css", http.StatusOK, "body"},
		{"/missing", http.StatusNotFound, ""},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))

			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Fatalf("body does not contain %q", tt.wantBody)
			}
		})
	}
}
