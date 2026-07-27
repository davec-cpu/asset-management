// Package web giữ toàn bộ asset tĩnh được nhúng vào binary.
//
// Directive go:embed chỉ nhúng được file nằm trong (hoặc dưới) thư mục của
// package chứa nó — không dùng được ".." — nên file này đặt ngay tại web/.
package web

// Một file Go A import "embed" có thể sử dụng directive `//go:embed` để
// khởi tạo một biến kiểu `string`, `[]byte` hoặc `embed.FS` bằng nội dung của các
// file khớp với pattern trong `go:embed`
import "embed"

// Static nhúng web/dist — OUTPUT của tools/assetbuild (đã minify, đã đổi tên
// theo content-hash, kèm biến thể nén .gz/.br và manifest.json), KHÔNG phải
// source web/static. Vì dist nằm trong .gitignore, `go build` khi chưa chạy
// `make generate` sẽ FAIL với "no matching files" — đây là guard có chủ đích
// buộc build đúng thứ tự (luôn build qua Makefile).
//
//go:embed all:dist
var Static embed.FS
