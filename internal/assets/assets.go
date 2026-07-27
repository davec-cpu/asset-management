// Package assets serve file static đã nhúng trong binary, ưu tiên trả bản
// nén sẵn (.br > .gz) theo header Accept-Encoding của client.
//
// Nội dung đến từ web/dist do tools/assetbuild sinh ra: đã minify, tên file
// đã version hóa theo content-hash (app.3f9a1c88.js). Nhờ đó:
//   - File có hash trong tên  → Cache-Control immutable 1 năm (URL đổi khi
//     nội dung đổi, browser không bao giờ cần hỏi lại).
//   - File giữ tên (index.html) → no-cache: browser luôn revalidate bằng
//     ETag, nhận 304 khi chưa đổi.
//
// Toàn bộ metadata được tính MỘT LẦN lúc khởi động — embed.FS bất biến nên
// không cần cache runtime nào khác, server hoàn toàn stateless.
package assets

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"go_embed_files_example/web"
)

const (
	//no-cache: Trình duyệt được lưu cache, nhưng response buộc phải validate với server gốc trước khi sử dụng,
	//ngay cả khi cache đã ngắt kết nối khỏi server gốc
	//validate: nghĩa là kiểm tra xem bản cache có còn đúng với bản trên server hay không
	//server gốc: nghĩa là server tạo ra và quản lý phiên bản gốc của tài nguyên
	//immutable: nghĩa là cache không thể thay đổi trong suốt thời gian max-age có hiệu lực
	cacheImmutable  = "public, max-age=31536000, immutable"
	cacheRevalidate = "no-cache" // lưu được, nhưng luôn revalidate (ETag → 304)
)

type asset struct {
	contentType  string
	cacheControl string
	etag         string // ETag của bản identity, đã bọc dấu nháy kép
	identity     []byte
	gzip         []byte // nil nếu không có biến thể
	brotli       []byte // nil nếu không có biến thể
}

var (
	files    map[string]*asset
	manifest map[string]string // tên gốc → tên đã hash, từ dist/manifest.json
)

// Khi chạy/build app, tất cả các package được import, hoặc nằm trong đồ thị import tới main.go sẽ
// được khởi tạo. Trong quá trình khởi tạo, nếu package có hàm init(), hàm này sẽ được gọi
func init() {
	files, manifest = mustLoad()
}

// Path đổi tên asset gốc sang tên đã version hóa: Path("app.js") →
// "app.3f9a1c88.js". Dùng khi template/handler cần trỏ tới asset; tên không
// có trong manifest (vd "index.html") trả về nguyên vẹn.
func Path(name string) string {
	if hashed, ok := manifest[name]; ok {
		return hashed
	}
	return name
}

// mustLoad quét embed.FS, ghép mỗi file với biến thể .gz/.br và đọc manifest.
func mustLoad() (map[string]*asset, map[string]string) {
	distFS, err := fs.Sub(web.Static, "dist")
	if err != nil {
		panic(fmt.Errorf("assets: sub dist: %w", err))
	}

	man := map[string]string{}
	manData, err := fs.ReadFile(distFS, "manifest.json")
	if err != nil {
		panic(fmt.Errorf("assets: đọc manifest.json (chạy make generate trước khi build?): %w", err))
	}
	if err := json.Unmarshal(manData, &man); err != nil {
		panic(fmt.Errorf("assets: parse manifest.json: %w", err))
	}
	//Tạo một map các tên file, cho biết rằng đây là các file đã hash tên
	hashed := map[string]bool{}
	for _, h := range man {
		hashed[h] = true
	}

	m := map[string]*asset{}
	err = fs.WalkDir(distFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		ext := path.Ext(p)
		// manifest.json là metadata nội bộ, không phải resource công khai.
		if d.IsDir() || ext == ".gz" || ext == ".br" || p == "manifest.json" {
			return nil
		}
		data, err := fs.ReadFile(distFS, p)
		if err != nil {
			return fmt.Errorf("đọc %s: %w", p, err)
		}
		sum := sha256.Sum256(data)
		//Chính sách cache
		cc := cacheRevalidate
		if hashed[p] {
			cc = cacheImmutable
		}
		m[p] = &asset{
			contentType:  detectContentType(ext, data),
			cacheControl: cc,
			etag:         `"` + hex.EncodeToString(sum[:8]) + `"`,
			identity:     data,
			gzip:         readVariant(distFS, p+".gz"),
			brotli:       readVariant(distFS, p+".br"),
		}
		return nil
	})
	if err != nil {
		panic(fmt.Errorf("assets: walk: %w", err))
	}
	return m, man
}

// readVariant trả nil khi biến thể không tồn tại — assetbuild chỉ sinh
// biến thể khi nén có lợi, nên thiếu là bình thường, không phải lỗi.
func readVariant(fsys fs.FS, name string) []byte {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil
	}
	return data
}

// Lấy kiểu MIME, dùng cho header Content-Type
func detectContentType(ext string, data []byte) string {
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	return http.DetectContentType(data)
}

// Handler serve các đường dẫn tương đối bên trong web/dist
// (mount sau http.StripPrefix, ví dụ /static/).
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		Serve(w, r, name)
	})
}

// map file đã được khởi tạo bằng mustLoad() khi chạy go build/run, gồm các thông tin header, cache control, gzip, brotlin
// Serve trả về file `name` (đường dẫn tương đối trong web/dist),
// negotiation theo Accept-Encoding và hỗ trợ If-None-Match → 304.
func Serve(w http.ResponseWriter, r *http.Request, name string) {
	//set phương thức
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a, ok := files[name]
	if !ok {
		http.NotFound(w, r)
		return
	}

	// Ưu tiên brotli (nhỏ nhất) rồi gzip; fallback bản gốc.
	// Mỗi representation phải có ETag riêng theo RFC 9110.
	data, encoding, etag := a.identity, "", a.etag
	accept := r.Header.Get("Accept-Encoding")
	switch {
	case a.brotli != nil && acceptsEncoding(accept, "br"):
		data, encoding = a.brotli, "br"
		etag = strings.TrimSuffix(a.etag, `"`) + `-br"`
	case a.gzip != nil && acceptsEncoding(accept, "gzip"):
		data, encoding = a.gzip, "gzip"
		etag = strings.TrimSuffix(a.etag, `"`) + `-gz"`
	}

	h := w.Header()
	h.Set("Content-Type", a.contentType)
	h.Set("ETag", etag)
	// Vary bắt buộc: cùng URL trả body khác nhau tùy Accept-Encoding,
	// thiếu nó proxy/CDN có thể cache bản br rồi trả cho client không hỗ trợ.
	h.Set("Vary", "Accept-Encoding")
	h.Set("Cache-Control", a.cacheControl)
	h.Set("X-Content-Type-Options", "nosniff")
	if encoding != "" {
		h.Set("Content-Encoding", encoding)
		// ServeContent bỏ qua Content-Length khi có Content-Encoding
		// (không biết length là của bản nào); ở đây data chính là bản nén
		// nên set được — tránh chunked transfer không cần thiết.
		h.Set("Content-Length", strconv.Itoa(len(data)))
	}

	// ServeContent lo If-None-Match/304 và Range dựa trên ETag đã set;
	// modtime zero vì nội dung nhúng không có mốc thời gian có nghĩa.
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

// acceptsEncoding kiểm tra enc có trong Accept-Encoding với q > 0 không.
// Parser tối giản: đủ cho các giá trị browser thực tế gửi
// ("gzip, deflate, br", "br;q=1.0, gzip;q=0.8", "gzip;q=0"…).
func acceptsEncoding(header, enc string) bool {
	for _, part := range strings.Split(header, ",") {
		token, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(token), enc) {
			continue
		}
		q := strings.TrimSpace(params)
		if q, ok := strings.CutPrefix(q, "q="); ok {
			if v := strings.TrimSpace(q); v == "0" || strings.HasPrefix(v, "0.") && strings.Trim(v[2:], "0") == "" {
				return false
			}
		}
		return true
	}
	return false
}
