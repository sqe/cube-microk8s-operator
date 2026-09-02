.PHONY: build build-go build-kopf test test-go test-kopf generate manifests manifests-go manifests-kopf verify kind-up kind-up-go kind-up-kopf kind-e2e kind-down init validate plan apply bootstrap destroy

build: build-go

build-go:
	go build ./cmd/manager

build-kopf:
	docker build -f python/Dockerfile -t cube-operator-kopf:v0.1.0 .

test: test-go test-kopf

test-go:
	go test ./...

test-kopf:
	PYTHONPATH=python python3 -m unittest discover -s python/tests -v

generate:
	go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.19.0 object paths=./api/...
	go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.19.0 crd rbac:roleName=cube-operator 'paths=./api/...;./internal/controller/...' output:crd:artifacts:config=config/crd/bases output:rbac:artifacts:config=config/rbac

manifests: manifests-go

manifests-go:
	kustomize build config/default

manifests-kopf:
	kustomize build config/kopf

verify:
	./scripts/verify.sh

kind-up:
	./scripts/kind-up.sh

kind-up-go:
	CONTROLLER=go ./scripts/kind-up.sh

kind-up-kopf:
	CONTROLLER=kopf ./scripts/kind-up.sh

kind-e2e:
	./scripts/kind-e2e.sh

kind-down:
	./scripts/kind-down.sh

init:
	terraform init

validate:
	terraform fmt -check -recursive
	terraform validate
	@if command -v shellcheck >/dev/null 2>&1; then \
		shellcheck scripts/*.sh; \
	else \
		echo "shellcheck not installed; skipping optional shell lint"; \
	fi

plan:
	terraform plan -out=tfplan

apply:
	terraform apply tfplan

bootstrap:
	./scripts/bootstrap-microk8s.sh

destroy:
	@echo "VM deletion is protected by default. Set protect_vms=false and apply before destroy."
	terraform destroy
