package notifications

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"wallet-scan/internal/db"
)

func TestWeComClientSendsMarkdownWithoutCredentials(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buffer, _ := io.ReadAll(r.Body)
		body = string(buffer)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := NewWeComClient(server.URL, time.Second)
	err := client.SendPositive(context.Background(), db.NotificationView{Address: "0xabc", Label: "test", Findings: []db.PositiveView{{Chain: "ethereum", Balance: "1", AssetSymbol: "ETH"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "0xabc") || strings.Contains(body, "private") {
		t.Fatalf("unexpected body: %s", body)
	}
}
