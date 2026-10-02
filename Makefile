.PHONY: build frontend test vet clean

build:
	./build.sh

frontend:
	cd web && pnpm install --frozen-lockfile && pnpm run typecheck && pnpm run build

test:
	go test ./...

vet:
	go vet ./...

clean:
	find dist -mindepth 1 -maxdepth 1 -delete
