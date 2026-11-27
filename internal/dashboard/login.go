package dashboard

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const sessionCookie = "sre_aiobs_session"
const sessionLifetime = 8 * time.Hour

type Login struct {
	secret    []byte
	namespace string
	cache     *redis.Client
}

func NewLogin(secret, password string, cache *redis.Client) (*Login, error) {
	if secret == "" {
		return nil, nil
	}
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimRight(secret, "=")))
	if err != nil || len(key) < 20 || len(key) > 64 || password == "" || cache == nil {
		return nil, errors.New("dashboard TOTP requires a valid Base32 secret (20–64 decoded bytes), dashboard password and Redis")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("sre-aiobs dashboard session\x00" + password))
	return &Login{secret: key, namespace: fmt.Sprintf("sre-aiobs:dashboard:%x:", mac.Sum(nil)), cache: cache}, nil
}

func hotp(key []byte, counter int64) string {
	var input [8]byte
	binary.BigEndian.PutUint64(input[:], uint64(counter))
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(input[:])
	digest := mac.Sum(nil)
	offset := digest[len(digest)-1] & 15
	value := binary.BigEndian.Uint32(digest[offset:]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1000000)
}

var trackAttempts = redis.NewScript(`
local attempts = redis.call('INCR', KEYS[1])
if attempts == 1 then redis.call('EXPIRE', KEYS[1], 60) end
return attempts
`)

func credentialsMatch(username, password string, expected [32]byte) bool {
	supplied := sha256.Sum256([]byte(password))
	return subtle.ConstantTimeCompare(expected[:], supplied[:]) == 1 && username == "operator"
}

func (a *Login) session(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || len(cookie.Value) != 64 {
		return ""
	}
	return a.namespace + "session:" + cookie.Value
}

func (a *Login) authenticate(w http.ResponseWriter, r *http.Request) bool {
	if key := a.session(r); key != "" {
		n, err := a.cache.Exists(r.Context(), key).Result()
		if err != nil {
			http.Error(w, "登录服务暂不可用", http.StatusServiceUnavailable)
			return false
		}
		if n == 1 {
			return true
		}
	}
	if r.Method == http.MethodGet && r.URL.Path == "/" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	} else {
		w.Header().Set("X-SRE-AIObs-Login", "/login")
		http.Error(w, "请先登录", http.StatusUnauthorized)
	}
	return false
}

func (a *Login) mount(mux *http.ServeMux, expected [32]byte) {
	page, err := assets.ReadFile("web/login.html")
	if err != nil {
		panic(err)
	}
	show := func(w http.ResponseWriter, status int, message string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(strings.Replace(string(page), "<!--login-status-->", message, 1)))
	}
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, _ *http.Request) {
		dashboardHeaders(w)
		show(w, http.StatusOK, "")
	})
	mux.HandleFunc("GET /login.css", func(w http.ResponseWriter, r *http.Request) {
		dashboardHeaders(w)
		http.ServeFileFS(w, r, assets, "web/login.css")
	})
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		dashboardHeaders(w)
		if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" || site == "same-site" {
			http.Error(w, "禁止跨站登录", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := r.ParseForm(); err != nil {
			show(w, http.StatusBadRequest, "登录请求无效。")
			return
		}
		const invalid = "账号、密码或动态验证码无效。"
		if !credentialsMatch(r.PostForm.Get("username"), r.PostForm.Get("password"), expected) {
			show(w, http.StatusUnauthorized, invalid)
			return
		}
		ctx := r.Context()
		attempts, err := trackAttempts.Run(ctx, a.cache, []string{a.namespace + "attempts"}).Int()
		if err != nil {
			show(w, http.StatusServiceUnavailable, "登录服务暂不可用。")
			return
		}
		if attempts > 6 {
			w.Header().Set("Retry-After", "60")
			show(w, http.StatusTooManyRequests, "验证次数过多，请稍后再试。")
			return
		}

		code := r.PostForm.Get("code")
		counter := time.Now().Unix() / 30
		matched := int64(-1)
		for _, step := range []int64{counter, counter - 1, counter + 1} {
			if subtle.ConstantTimeCompare([]byte(hotp(a.secret, step)), []byte(code)) == 1 && matched < 0 {
				matched = step
			}
		}
		if matched < 0 {
			show(w, http.StatusUnauthorized, invalid)
			return
		}
		fresh, err := a.cache.SetNX(ctx, a.namespace+"code:"+strconv.FormatInt(matched, 10), "1", 90*time.Second).Result()
		if err != nil {
			show(w, http.StatusServiceUnavailable, "登录服务暂不可用。")
			return
		}
		if !fresh {
			show(w, http.StatusUnauthorized, "验证码已使用，请等待下一个动态码。")
			return
		}
		var random [32]byte
		rand.Read(random[:])
		token := hex.EncodeToString(random[:])
		if err := a.cache.Set(ctx, a.namespace+"session:"+token, "1", sessionLifetime).Err(); err != nil {
			show(w, http.StatusServiceUnavailable, "登录服务暂不可用。")
			return
		}
		if key := a.session(r); key != "" {
			_ = a.cache.Del(ctx, key).Err()
		}
		setSessionCookie(w, token, int(sessionLifetime.Seconds()))
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})
	mux.HandleFunc("POST /logout", func(w http.ResponseWriter, r *http.Request) {
		dashboardHeaders(w)
		if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" || site == "same-site" {
			http.Error(w, "禁止跨站退出", http.StatusForbidden)
			return
		}
		if key := a.session(r); key != "" {
			if err := a.cache.Del(r.Context(), key).Err(); err != nil {
				http.Error(w, "登录服务暂不可用", http.StatusServiceUnavailable)
				return
			}
		}
		setSessionCookie(w, "", -1)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
}

func setSessionCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
}
