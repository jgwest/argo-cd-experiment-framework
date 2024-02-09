
.PHONY: test
test: ## Run tests.
	go test -coverprofile cover.out  ./...
# `go list ./...`

.PHONY: run
run: ## Run main
	go run .
