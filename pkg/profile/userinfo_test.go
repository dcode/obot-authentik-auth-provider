package profile

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dcode/obot-authentik-auth-provider/pkg/authentikapi"
)

func TestGetUserInfo(t *testing.T) {
	var gotAuth string
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"userinfo_endpoint":"` + srv.URL + `/application/o/userinfo/"}`))
	})
	mux.HandleFunc("/application/o/userinfo/", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"sub":"42","name":"Ada Lovelace","preferred_username":"ada"}`))
	})

	client := authentikapi.New("https://unused.example", "service-token")
	info, err := GetUserInfo(context.Background(), client, srv.URL, "user-access-token")
	if err != nil {
		t.Fatalf("GetUserInfo() error = %v", err)
	}
	if info.ID != "42" || info.Name != "Ada Lovelace" {
		t.Errorf("info = %+v, want {ID:42 Name:\"Ada Lovelace\"}", info)
	}
	if gotAuth != "Bearer user-access-token" {
		t.Errorf("Authorization header = %q, want the caller's own access token, not the service API token", gotAuth)
	}
}

func TestGetUserInfo_FallsBackToPreferredUsername(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"userinfo_endpoint":"` + srv.URL + `/userinfo/"}`))
	})
	mux.HandleFunc("/userinfo/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"sub":"42","preferred_username":"ada"}`))
	})

	client := authentikapi.New("https://unused.example", "")
	info, err := GetUserInfo(context.Background(), client, srv.URL, "token")
	if err != nil {
		t.Fatalf("GetUserInfo() error = %v", err)
	}
	if info.Name != "ada" {
		t.Errorf("Name = %q, want fallback to preferred_username %q", info.Name, "ada")
	}
}
