package assets

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
)

// Các test này yêu cầu web/dist đã được sinh (make generate) — thiếu thì
// binary còn không build được (go:embed fail), nên tới đây là dist đã có.
// requireVariants fail sớm với thông báo rõ ràng thay vì pass giả.
func requireVariants(t *testing.T, name string) *asset {
	t.Helper()
	a, ok := files[name]
	if !ok {
		t.Fatalf("thiếu asset %q trong embed", name)
	}
	if a.gzip == nil || a.brotli == nil {
		t.Fatalf("asset %q chưa có biến thể nén — chạy `make generate` trước khi test", name)
	}
	return a
}

func get(t *testing.T, name, acceptEncoding string, extraHeader map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/"+name, nil)
	if acceptEncoding != "" {
		r.Header.Set("Accept-Encoding", acceptEncoding)
	}
	for k, v := range extraHeader {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	Handler().ServeHTTP(w, r)
	return w
}

func TestPathResolvesHashedName(t *testing.T) {
	for orig, pattern := range map[string]string{
		"app.js":  `^app\.[0-9a-f]{8}\.js$`,
		"app.css": `^app\.[0-9a-f]{8}\.css$`,
	} {
		hashed := Path(orig)
		if !regexp.MustCompile(pattern).MatchString(hashed) {
			t.Errorf("Path(%q) = %q, không khớp %s", orig, hashed, pattern)
		}
	}
	// Tên không có trong manifest trả về nguyên vẹn.
	if got := Path("index.html"); got != "index.html" {
		t.Errorf("Path(index.html) = %q, want index.html", got)
	}
}

func TestOriginalNamesNotServed(t *testing.T) {
	// Sau khi version hóa, tên gốc không còn tồn tại — chỉ tên hash được serve.
	for _, name := range []string{"app.js", "app.css", "manifest.json"} {
		if w := get(t, name, "", nil); w.Code != http.StatusNotFound {
			t.Errorf("GET %q status = %d, want 404", name, w.Code)
		}
	}
}

func TestMinifiedOutput(t *testing.T) {
	css := requireVariants(t, Path("app.css"))
	if bytes.Contains(css.identity, []byte("\n")) || bytes.Contains(css.identity, []byte("/*")) {
		t.Error("app.css chưa được minify (còn xuống dòng/comment)")
	}
	html := files["index.html"]
	if html == nil {
		t.Fatal("thiếu index.html")
	}
	if !bytes.Contains(html.identity, []byte("/static/"+Path("app.css"))) {
		t.Error("index.html chưa được rewrite sang tên asset đã hash")
	}
}

func TestCacheControl(t *testing.T) {
	if w := get(t, Path("app.js"), "", nil); w.Header().Get("Cache-Control") != cacheImmutable {
		t.Errorf("asset hash: Cache-Control = %q, want %q", w.Header().Get("Cache-Control"), cacheImmutable)
	}
	if w := get(t, "index.html", "", nil); w.Header().Get("Cache-Control") != cacheRevalidate {
		t.Errorf("index.html: Cache-Control = %q, want %q", w.Header().Get("Cache-Control"), cacheRevalidate)
	}
}

func TestServeIdentity(t *testing.T) {
	name := Path("app.css")
	a := requireVariants(t, name)
	w := get(t, name, "", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if enc := w.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding = %q, want empty", enc)
	}
	if !bytes.Equal(w.Body.Bytes(), a.identity) {
		t.Error("body khác bản gốc")
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("Content-Type = %q, want text/css", ct)
	}
	if w.Header().Get("Vary") != "Accept-Encoding" {
		t.Errorf("Vary = %q, want Accept-Encoding", w.Header().Get("Vary"))
	}
}

func TestServeBrotliPreferred(t *testing.T) {
	name := Path("app.js")
	a := requireVariants(t, name)
	w := get(t, name, "gzip, deflate, br, zstd", nil)

	if enc := w.Header().Get("Content-Encoding"); enc != "br" {
		t.Fatalf("Content-Encoding = %q, want br", enc)
	}
	// Body phải giải nén ra đúng bản gốc.
	decoded, err := io.ReadAll(brotli.NewReader(w.Body))
	if err != nil {
		t.Fatalf("giải nén brotli: %v", err)
	}
	if !bytes.Equal(decoded, a.identity) {
		t.Error("body brotli giải nén khác bản gốc")
	}
	// Content-Type vẫn là của file gốc, không phải octet-stream.
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type = %q, want javascript", ct)
	}
}

func TestServeGzipFallback(t *testing.T) {
	name := Path("app.js")
	a := requireVariants(t, name)
	w := get(t, name, "gzip, deflate", nil)

	if enc := w.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", enc)
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatalf("mở gzip reader: %v", err)
	}
	decoded, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("giải nén gzip: %v", err)
	}
	if !bytes.Equal(decoded, a.identity) {
		t.Error("body gzip giải nén khác bản gốc")
	}
}

func TestQZeroDisablesEncoding(t *testing.T) {
	name := Path("app.js")
	requireVariants(t, name)
	w := get(t, name, "br;q=0, gzip;q=0", nil)
	if enc := w.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding = %q, want empty khi q=0", enc)
	}
}

func TestETagPerEncodingAndNotModified(t *testing.T) {
	name := Path("app.js")
	requireVariants(t, name)

	identity := get(t, name, "", nil)
	br := get(t, name, "br", nil)
	if identity.Header().Get("ETag") == br.Header().Get("ETag") {
		t.Error("ETag của identity và brotli phải khác nhau (representation khác nhau)")
	}

	// Gửi lại If-None-Match với đúng ETag → 304.
	w := get(t, name, "br", map[string]string{"If-None-Match": br.Header().Get("ETag")})
	if w.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304", w.Code)
	}
}

func TestNotFoundAndTraversal(t *testing.T) {
	for _, name := range []string{"khong-ton-tai.js", "../go.mod", "..%2Fgo.mod"} {
		if w := get(t, name, "", nil); w.Code != http.StatusNotFound {
			t.Errorf("GET %q status = %d, want 404", name, w.Code)
		}
	}
}

// Biến thể nén không được truy cập trực tiếp như một file riêng —
// tránh URL .gz trả về với Content-Type sai.
func TestVariantsNotServedDirectly(t *testing.T) {
	name := Path("app.js")
	requireVariants(t, name)
	for _, suffix := range []string{".gz", ".br"} {
		if w := get(t, name+suffix, "", nil); w.Code != http.StatusNotFound {
			t.Errorf("GET %q status = %d, want 404", name+suffix, w.Code)
		}
	}
}

func TestAcceptsEncoding(t *testing.T) {
	cases := []struct {
		header, enc string
		want        bool
	}{
		{"gzip, deflate, br", "br", true},
		{"gzip, deflate, br", "gzip", true},
		{"gzip, deflate", "br", false},
		{"br;q=1.0, gzip;q=0.8", "gzip", true},
		{"gzip;q=0", "gzip", false},
		{"gzip;q=0.000", "gzip", false},
		{"GZIP", "gzip", true},
		{"", "gzip", false},
	}
	for _, c := range cases {
		if got := acceptsEncoding(c.header, c.enc); got != c.want {
			t.Errorf("acceptsEncoding(%q, %q) = %v, want %v", c.header, c.enc, got, c.want)
		}
	}
}
