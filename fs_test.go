package sessions

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newTestStore 临时目录上的文件存储
func newTestStore(t *testing.T, maxAge int) *FilesystemStore {
	t.Helper()

	return NewFilesystemStore(maxAge, t.TempDir())
}

// countFiles 数一下目录里有多少个会话文件
func countFiles(t *testing.T, dir string) int {
	t.Helper()

	n := 0

	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			n++
		}

		return nil
	})

	return n
}

// sidOf 取响应里发的会话 cookie
func sidOf(t *testing.T, rec *httptest.ResponseRecorder, name string) string {
	t.Helper()

	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c.Value
		}
	}

	t.Fatalf("响应里没有 %s cookie", name)

	return ""
}

// TestAnonymousRequestWritesNoFile 匿名请求不该写会话文件
// 这是这个库最要紧的一个 bug：以前 ServeHTTP 对每个请求都 Store 一次，
// 未登录的 GET / 也会落一个文件，不带 Cookie 的脚本能把 sess_path 堆满。
func TestAnonymousRequestWritesNoFile(t *testing.T) {
	store := newTestStore(t, 3600)
	ss := New(store, 3600, 3600, "id")

	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)

		ss.ServeHTTP(rec, req, func(w http.ResponseWriter, r *http.Request) {})
	}

	if n := countFiles(t, store.dir); n != 0 {
		t.Fatalf("匿名请求不该写会话文件，实际 %d 个", n)
	}
}

// TestStoreAndReload 存了数据的会话要落盘，带着 cookie 能读回来
func TestStoreAndReload(t *testing.T) {
	store := newTestStore(t, 3600)
	ss := New(store, 3600, 3600, "id")

	rec := httptest.NewRecorder()
	ss.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil),
		func(w http.ResponseWriter, r *http.Request) {
			GetSession(r).Store("user", "a@b.com")
		})

	if n := countFiles(t, store.dir); n != 1 {
		t.Fatalf("有数据的会话应写 1 个文件，实际 %d", n)
	}

	sid := sidOf(t, rec, "id")

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "id", Value: sid})

	got := ""
	ss.ServeHTTP(rec, req, func(w http.ResponseWriter, r *http.Request) {
		if v, ok := GetSession(r).Load("user"); ok {
			got = v.(string)
		}
	})

	if got != "a@b.com" {
		t.Fatalf("会话里的数据应能读回来，实际 %q", got)
	}
}

// TestEmptySessionRemovesFile 会话被清空（退出登录）时文件要删掉
func TestEmptySessionRemovesFile(t *testing.T) {
	store := newTestStore(t, 3600)
	ss := New(store, 3600, 3600, "id")

	rec := httptest.NewRecorder()
	ss.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil),
		func(w http.ResponseWriter, r *http.Request) {
			GetSession(r).Store("user", "a@b.com")
		})

	sid := sidOf(t, rec, "id")

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "id", Value: sid})

	ss.ServeHTTP(rec, req, func(w http.ResponseWriter, r *http.Request) {
		GetSession(r).Delete("user")
	})

	if n := countFiles(t, store.dir); n != 0 {
		t.Fatalf("清空后不该留下会话文件，实际 %d 个", n)
	}
}

// TestMaxAgeFromStore 过期时间按当前配置算，不认文件里的旧值
// 以前 MaxAge/Expires 写进文件，调大 sess_ttl 只对新会话生效。
func TestMaxAgeFromStore(t *testing.T) {
	dir := t.TempDir()
	store := NewFilesystemStore(60, dir)

	sid := "0123456789abcdef0123456789abcdef"
	store.Store(nil, sid, &FileSession{
		MaxAge:  60,
		Expires: time.Now().Add(time.Minute),
		Payload: map[string]interface{}{"user": "a@b.com"},
	})

	long := NewFilesystemStore(3600, dir)
	s, created := long.LoadOrCreate(nil, sid)
	if created {
		t.Fatal("文件存在时不该当成新会话")
	}

	if s.(*FileSession).MaxAge != 3600 {
		t.Fatalf("MaxAge 应取当前配置 3600，实际 %d", s.(*FileSession).MaxAge)
	}

	if time.Until(s.(*FileSession).Expires) < 3000*time.Second {
		t.Fatalf("Expires 应按新配置顺延，实际 %v", s.(*FileSession).Expires)
	}
}

// TestValidSID 只认 32 位字母数字，避免拿别人家的 cookie 当 SID
func TestValidSID(t *testing.T) {
	cases := map[string]bool{
		"0123456789abcdef0123456789abcdef": true, // 新格式
		"0123456789ABCDEFGHIJKLMNOPQRSTUV": true, // 旧格式也认，老会话不用重登
		"short":                            false,
		"0123456789abcdef0123456789abcde":  false, // 31 位
		"0123456789abcdef0123456789abcde/": false, // 非法字符
	}

	for sid, want := range cases {
		if got := validSID(sid); got != want {
			t.Errorf("validSID(%q) = %v，应为 %v", sid, got, want)
		}
	}
}

// TestCookieFlags cookie 要有 HttpOnly/SameSite，https 下要有 Secure
func TestCookieFlags(t *testing.T) {
	store := newTestStore(t, 3600)
	ss := New(store, 3600, 3600, "id")

	cases := []struct {
		name      string
		forwarded string
		secure    bool
	}{
		{"http", "", false},
		{"nginx https", "https", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.forwarded != "" {
				req.Header.Set("X-Forwarded-Proto", tc.forwarded)
			}

			ss.ServeHTTP(rec, req, func(w http.ResponseWriter, r *http.Request) {})

			c := rec.Result().Cookies()
			if len(c) != 1 {
				t.Fatalf("应发 1 个 cookie，实际 %d", len(c))
			}

			if !c[0].HttpOnly {
				t.Error("cookie 应有 HttpOnly")
			}
			if c[0].SameSite != http.SameSiteLaxMode {
				t.Error("cookie 应有 SameSite=Lax")
			}
			if c[0].Secure != tc.secure {
				t.Errorf("Secure 应为 %v，实际 %v", tc.secure, c[0].Secure)
			}
		})
	}
}

// TestRandomSID 生成的 SID 长度固定、且是十六进制
func TestRandomSID(t *testing.T) {
	seen := map[string]bool{}

	for i := 0; i < 100; i++ {
		sid := randomString(LengthOfSID)
		if len(sid) != LengthOfSID {
			t.Fatalf("SID 长度应为 %d，实际 %d", LengthOfSID, len(sid))
		}
		if !validSID(sid) {
			t.Fatalf("生成的 SID 应该合法：%q", sid)
		}
		if seen[sid] {
			t.Fatalf("SID 重复：%q", sid)
		}

		seen[sid] = true
	}
}
