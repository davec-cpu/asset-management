BINARY := bin/server

.PHONY: generate build run test clean

## generate: minify + version hóa (content-hash) + nén sẵn (.gz/.br)
## web/static → web/dist — PHẢI chạy trước build/test (go:embed nhúng dist)
generate:
	go run ./tools/assetbuild

## build: generate rồi build ra MỘT binary tự chứa
build: generate
	go build -trimpath -ldflags="-s -w" -o $(BINARY) .
	@ls -lh $(BINARY)

## run: build rồi chạy server tại :8088
run: build
	./$(BINARY)

## test: generate (test cần biến thể nén) rồi chạy toàn bộ test
test: generate
	go test -race ./...

## clean: xóa binary và toàn bộ output build asset
clean:
	rm -rf bin web/dist
