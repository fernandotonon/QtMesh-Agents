APP=qma
MODULE=github.com/fernandotonon/QtMesh-Agents
VERSION?=0.1.0-dev

.PHONY: build web test vet run install clean release-snapshot

web:
	cd web && npm install && npm run build

# Uses committed/embedded internal/web/dist. Run `make web` first after SPA changes.
build:
	go build -ldflags "-X $(MODULE)/pkg/version.Version=$(VERSION)" -o bin/$(APP) ./cmd/qma

test:
	go test ./...

vet:
	go vet ./...

run: build
	./bin/$(APP)

install:
	@if [ -d web ] && command -v npm >/dev/null 2>&1; then $(MAKE) web; fi
	go install -ldflags "-X $(MODULE)/pkg/version.Version=$(VERSION)" ./cmd/qma
	@# Prefer a directory already on PATH (~/.local/bin or ~/bin) over GOPATH/bin.
	@dest=""; \
	for d in "$$HOME/.local/bin" "$$HOME/bin"; do \
	  case ":$$PATH:" in *":$$d:"*) dest="$$d"; break;; esac; \
	done; \
	gobin="$$(go env GOBIN)"; \
	[ -n "$$gobin" ] || gobin="$$(go env GOPATH)/bin"; \
	if [ -n "$$dest" ] && [ "$$dest" != "$$gobin" ]; then \
	  mkdir -p "$$dest"; \
	  ln -sf "$$gobin/qma" "$$dest/qma"; \
	  ln -sf "$$gobin/qma" "$$dest/qtmesh-agents"; \
	  echo "installed qma -> $$dest/qma"; \
	else \
	  echo "installed qma -> $$gobin/qma"; \
	  echo "note: add $$gobin to PATH if 'qma' is not found"; \
	fi
	@echo "restart daemon to pick up the new binary: qma daemon stop && qma daemon start"

clean:
	rm -rf bin dist web/node_modules

# Build release archives locally without publishing or pushing tags.
release-snapshot:
	goreleaser release --snapshot --clean
