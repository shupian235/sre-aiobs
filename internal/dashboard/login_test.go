package dashboard

import (
	"context"
	"encoding/base32"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestHOTPReferenceVectors(t *testing.T) {
	key := []byte("12345678901234567890")
	for counter, want := range []string{"755224", "287082", "359152", "969429"} {
		if got := hotp(key, int64(counter)); got != want {
			t.Fatalf("counter %d: got %s, want %s", counter, got, want)
		}
	}
}

func TestTOTPLoginWithRedis(t *testing.T) {
	address := os.Getenv("TEST_DASHBOARD_REDIS")
	if address == "" {
		t.Skip("TEST_DASHBOARD_REDIS is not set")
	}
	cache := redis.NewClient(&redis.Options{Addr: address})
	t.Cleanup(func() { _ = cache.Close() })
	password := t.Name() + time.Now().String()
	key := []byte("12345678901234567890")
	login, err := NewLogin(base32.StdEncoding.EncodeToString(key), password, cache)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		keys, err := cache.Keys(ctx, login.namespace+"*").Result()
		if err == nil && len(keys) != 0 {
			_ = cache.Del(ctx, keys...).Err()
		}
	})
	mux := http.NewServeMux()
	Mount(mux, &fixtureReader{}, password, SourceAccess{Login: login})
	request := func(method, path string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.SetBasicAuth("operator", password)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if r := request("GET", "/", nil, nil); r.Code != 303 || r.Header().Get("Location") != "/login" {
		t.Fatalf("basic credentials bypassed login: %d", r.Code)
	}
	form := url.Values{"username": {"operator"}, "password": {password}, "code": {hotp(key, time.Now().Unix()/30)}}
	r := request("POST", "/login", form, nil)
	if r.Code != 303 || len(r.Result().Cookies()) != 1 {
		t.Fatalf("login failed: %d", r.Code)
	}
	cookie := r.Result().Cookies()[0]
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("missing protective cookie attributes")
	}
	if r := request("POST", "/login", form, nil); r.Code != 401 {
		t.Fatalf("replayed code accepted: %d", r.Code)
	}
	if r := request("GET", "/api/dashboard", nil, cookie); r.Code != 200 {
		t.Fatalf("session rejected: %d", r.Code)
	}
	if r := request("POST", "/logout", nil, cookie); r.Code != 303 {
		t.Fatalf("logout failed: %d", r.Code)
	}
	if r := request("GET", "/api/dashboard", nil, cookie); r.Code != 401 {
		t.Fatalf("revoked session accepted: %d", r.Code)
	}
}
