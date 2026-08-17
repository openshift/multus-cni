

.PHONY: deps-update
deps-update: ; $(info  Updating dependencies...) @ ## Update dependencies
	go mod tidy
	go mod vendor

PHONY: build test

build:
	./hack/build-go.sh

test:
	sudo ./hack/test-go.sh

.PHONY: build-e2e-tests
build-e2e-tests:
	@echo "Building multus-cni-tests-ext binary..."
	$(MAKE) -C test build
