package serve

import (
	"net/http"
	"testing"
)

func TestRunBadAddress(t *testing.T) {
	if err := Run("127.0.0.1:-1", http.NotFoundHandler()); err == nil {
		t.Error("bad address: no error")
	}
}
