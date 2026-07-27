// go_embed_files_example — ví dụ nhúng static asset (JS/CSS/HTML) đã nén sẵn
// vào MỘT binary Go duy nhất.
//
// Quy trình build (xem Makefile):
//  1. go run ./tools/assetbuild → web/static → web/dist:
//     minify js/css/html + đổi tên theo content-hash (app.3f9a1c88.js)
//     + rewrite tham chiếu trong HTML/CSS + sinh .gz/.br + manifest.json
//  2. go build                  → go:embed nhúng toàn bộ web/dist
//
// Kết quả: một binary tự chứa, deploy chỉ cần copy 1 file; asset đã minify,
// version hóa (cache immutable 1 năm) và nén sẵn — không tốn CPU nén runtime.
package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"go_embed_files_example/internal/assets"
)

func main() {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8088"
	}
	//Mux là một bộ định tuyến, tương tự như chi framework
	mux := http.NewServeMux()
	// Trang chủ serve thẳng index.html đã nhúng (vẫn được negotiation nén).
	//negotiation (đàm phán): Là cơ chế mà client và server thỏa thuận với nhau để quyết định xem
	// dùng phiên bản nào của một tài nguyên
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		assets.Serve(w, r, "index.html")
	})
	mux.Handle("GET /static/", http.StripPrefix("/static/", assets.Handler()))

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("listening on http://localhost%s", addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
