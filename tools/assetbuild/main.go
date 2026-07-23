// Command assetbuild là bước build asset chạy TRƯỚC go build (xem Makefile):
//
//	web/static (source, commit)  →  web/dist (artifact, gitignore, được embed)
//
// Với mỗi file nó thực hiện lần lượt:
//  1. MINIFY js/css/html/svg/json/xml (tdewolff/minify — thuần Go, không cần Node).
//  2. VERSION HÓA: đổi tên theo hash nội dung (app.js → app.3f9a1c88.js) để
//     serve được với Cache-Control immutable; file HTML giữ nguyên tên vì là
//     entry point (URL phải ổn định).
//  3. REWRITE tham chiếu trong CSS/HTML sang tên đã hash (theo manifest).
//  4. PRECOMPRESS: sinh .gz (gzip -9) và .br (brotli q11) cạnh từng file.
//
// Cuối cùng ghi web/dist/manifest.json (tên gốc → tên đã hash) để runtime
// biết file nào immutable và template tra được đường dẫn thật.
//
// Thứ tự hash có chủ đích: asset "lá" (ảnh, font, js…) hash trước, rồi CSS
// (nội dung đổi sau khi rewrite ref → hash phải tính sau), HTML cuối cùng.
package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/tdewolff/minify/v2"
	"github.com/tdewolff/minify/v2/css"
	"github.com/tdewolff/minify/v2/html"
	"github.com/tdewolff/minify/v2/js"
	mjson "github.com/tdewolff/minify/v2/json"
	"github.com/tdewolff/minify/v2/svg"
	"github.com/tdewolff/minify/v2/xml"
)

const (
	srcRoot = "web/static"
	outRoot = "web/dist"
	hashLen = 8 // số ký tự hex của sha256 đưa vào tên file
)

var minifyMime = map[string]string{
	".css":  "text/css",
	".htm":  "text/html",
	".html": "text/html",
	".js":   "text/javascript",
	".json": "application/json",
	".mjs":  "text/javascript",
	".svg":  "image/svg+xml",
	".xml":  "text/xml",
}

// Định dạng text-based đáng nén; ảnh/video/woff2 đã nén sẵn.
var compressibleExt = map[string]bool{
	".css": true, ".htm": true, ".html": true, ".js": true, ".json": true,
	".map": true, ".mjs": true, ".svg": true, ".txt": true, ".wasm": true, ".xml": true,
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("assetbuild: %v", err)
	}
}

func run() error {
	// dist sinh lại từ đầu mỗi lần build — không bao giờ có artifact stale.
	// artifact stale: các bản file cũ, chưa được cập nhật mới nhất

	if err := os.RemoveAll(outRoot); err != nil {
		return err
	}

	var others, cssFiles, htmlFiles []string
	//phần này, WalkDir sẽ duyệt qua tất cả các file trong srcRoot, và với mỗi file, sẽ gọi tới callback được cung cấp
	//Callback có dạng: func(p string, d fs.DirEntry, err error), trong đó:
	// - path: đường dẫn đầy đủ của các file trong thư mục đang được duyệt
	// - d: đối tượng đang xét
	// - err: lỗi xảy ra, nếu có. Nếu không có lỗi thì giá trị là nil

	//duyệt từng file trong srcRoot, tạo đường dẫn tương đối và chuẩn hóa dấu ngăn cách file
	//đẩy các file html, css vào các mảng tương ứng (cssFiles, htmlFiles, others)
	err := filepath.WalkDir(srcRoot, func(p string, d fs.DirEntry, err error) error {
		//IsDir: kiếm tra xem đối tượng đang xét có phải thư mục không, trả về true false
		if err != nil || d.IsDir() {
			return err
		}
		//Rel(basepath, targetpath) tính đường dẫn tương đối từ basepath tới targetpath
		rel, err := filepath.Rel(srcRoot, p)
		if err != nil {
			return err
		}
		//Chuyển mọi dấu phân cách thư mục sang dấu "/"
		rel = filepath.ToSlash(rel)
		//path.Ext(rel): lấy phần mở rộng (extension) của đường dẫn hoặc file
		switch path.Ext(rel) {
		case ".gz", ".br":
			// Biến thể nén không bao giờ là source — bỏ qua nếu ai đó lỡ
			// commit hoặc sót lại từ pipeline cũ.
			return nil
		case ".css":
			cssFiles = append(cssFiles, rel)
		case ".html", ".htm":
			htmlFiles = append(htmlFiles, rel)
		default:
			others = append(others, rel)
		}
		return nil
	})
	if err != nil {
		return err
	}
	//sort.Strings sắp xếp các string theo thứ tự tăng dần
	sort.Strings(others)
	sort.Strings(cssFiles)
	sort.Strings(htmlFiles)

	//minifier là chương trình có nhiệm vụ minify
	//manifest là map các
	m := newMinifier()
	manifest := map[string]string{}

	//Duyệt qua các mảng vừa được tạo ở trên, sửa lại tham chiếu bên trong file, đặt lại tên và ghi ra ổ đĩa
	// Pass 1 — asset lá: minify + hash.
	for _, rel := range others {
		if err := buildOne(m, rel, manifest, nil, true); err != nil {
			return err
		}
	}
	// Pass 2 — CSS: rewrite ref (url(...) tới asset đã hash) + minify + hash.
	for _, rel := range cssFiles {
		if err := buildOne(m, rel, manifest, rewriteCSS, true); err != nil {
			return err
		}
	}
	// Pass 3 — HTML: rewrite ref + minify, GIỮ NGUYÊN TÊN (entry point).
	for _, rel := range htmlFiles {
		if err := buildOne(m, rel, manifest, rewriteHTML, false); err != nil {
			return err
		}
	}

	if err := writeManifest(manifest); err != nil {
		return err
	}
	return precompressDist()
}

// buildOne đọc src, sửa lại các tham chiếu bên trong file (nếu cần), minify (nếu hỗ trợ), rồi ghi ra
// dist dưới tên đã hash (hoặc tên gốc với HTML).
// m: minifier
// rel: đường dẫn tương đối tới file (relative)
// manifest:
// rewrite: hàm đặt lại tên
func buildOne(m *minify.M, rel string, manifest map[string]string, rewrite func([]byte, map[string]string) []byte, hashed bool) error {
	src, err := os.ReadFile(filepath.Join(srcRoot, rel))
	if err != nil {
		return fmt.Errorf("đọc %s: %w", rel, err)
	}

	out := src
	//Sửa lại các tham chiếu trong file, bởi vì tên các file đã được version hóa
	if rewrite != nil {
		out = rewrite(out, manifest)
	}
	//tra cứu map minifyMime, lấy ra phần tử. Nếu key có tồn tại, thực hiện minify
	//Chỉ minify những file có extension trong mảng minifyMime
	if mime, ok := minifyMime[path.Ext(rel)]; ok {
		//m.Bytes minify một mảng các bytes
		minified, err := m.Bytes(mime, out)
		if err != nil {
			return fmt.Errorf("minify %s: %w", rel, err)
		}
		out = minified
	}

	outRel := rel
	if hashed {
		outRel = hashedName(rel, out)
		manifest[rel] = outRel
	}
	//FromSlash: ngược lại với ToSlash, chuyển các dấu "/" thành cách dấu ngăn cách file đúng chuẩn của hệ điều hành hiện tại
	dst := filepath.Join(outRoot, filepath.FromSlash(outRel))
	//MkdirAll: tạo toàn bộ thư mục cần thiết. VD: Tạo toàn bộ các thư mục cần thiết trong filepath.Dir(dst)
	//0o755 là quyền truy cập thư mục
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(dst, out, 0o644); err != nil {
		return err
	}
	fmt.Printf("  %-16s → %-28s %6dB → %6dB (minify %2.0f%%)\n",
		rel, outRel, len(src), len(out), 100*float64(len(out))/float64(len(src)))
	return nil
}

// hashedName chèn hash nội dung vào trước đuôi file: img/logo.svg → img/logo.ab12cd34.svg.
func hashedName(rel string, data []byte) string {
	sum := sha256.Sum256(data)
	ext := path.Ext(rel)
	return strings.TrimSuffix(rel, ext) + "." + hex.EncodeToString(sum[:])[:hashLen] + ext
}

// rewriteHTML thay mọi tham chiếu /static/<gốc> bằng /static/<đã hash>.
func rewriteHTML(src []byte, manifest map[string]string) []byte {
	s := string(src)
	for orig, hashed := range manifest {
		s = strings.ReplaceAll(s, "/static/"+orig, "/static/"+hashed)
	}
	return []byte(s)
}

// rewriteCSS xử lý cả dạng tuyệt đối /static/x lẫn url(x) tương đối cùng thư mục.
// Thay thế chuỗi đơn giản là đủ cho demo; dự án thật để bundler (Vite/esbuild)
// lo việc này — cơ chế hash + immutable phía server thì không đổi.
func rewriteCSS(src []byte, manifest map[string]string) []byte {
	s := string(src)
	for orig, hashed := range manifest {
		s = strings.ReplaceAll(s, "/static/"+orig, "/static/"+hashed)
		for _, q := range []string{`url(`, `url("`, `url('`} {
			s = strings.ReplaceAll(s, q+orig, q+hashed)
		}
	}
	return []byte(s)
}

func newMinifier() *minify.M {
	m := minify.New()
	m.AddFunc("text/css", css.Minify)
	m.AddFunc("text/html", html.Minify)
	m.AddFunc("text/javascript", js.Minify)
	m.AddFunc("application/json", mjson.Minify)
	m.AddFunc("image/svg+xml", svg.Minify)
	m.AddFunc("text/xml", xml.Minify)
	return m
}

func writeManifest(manifest map[string]string) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outRoot, "manifest.json"), data, 0o644)
}

// precompressDist sinh .gz/.br cho mọi file nén được trong dist,
// chỉ giữ biến thể khi thực sự nhỏ hơn bản gốc.
func precompressDist() error {
	return filepath.WalkDir(outRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !compressibleExt[filepath.Ext(p)] {
			return err
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		gz, err := gzipBytes(src)
		if err != nil {
			return fmt.Errorf("gzip %s: %w", p, err)
		}
		if len(gz) < len(src) {
			if err := os.WriteFile(p+".gz", gz, 0o644); err != nil {
				return err
			}
		}
		br, err := brotliBytes(src)
		if err != nil {
			return fmt.Errorf("brotli %s: %w", p, err)
		}
		if len(br) < len(src) {
			if err := os.WriteFile(p+".br", br, 0o644); err != nil {
				return err
			}
		}
		fmt.Printf("  nén %-40s %6dB  gz %6dB  br %6dB\n",
			strings.TrimPrefix(p, outRoot+"/"), len(src), len(gz), len(br))
		return nil
	})
}

func gzipBytes(src []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(src); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func brotliBytes(src []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := brotli.NewWriterLevel(&buf, brotli.BestCompression)
	if _, err := w.Write(src); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
