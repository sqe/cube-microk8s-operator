.PHONY: build build-go build-kopf test test-go test-kopf generate manifests manifests-go manifests-kopf verify kind-up kind-up-go kind-up-kopf kind-e2e kind-down init validate plan apply bootstrap destroy

VERSION ?= 0.1.0
IMG ?= ghcr.io/sqe/cube-operator:v$(VERSION)
BUNDLE_IMG ?= ghcr.io/sqe/cube-operator-bundle:v$(VERSION)
CATALOG_IMG ?= ghcr.io/sqe/cube-operator-catalog:v$(VERSION)
PLATFORMS ?= linux/amd64,linux/arm64
OPERATOR_SDK ?= operator-sdk
OPM ?= opm

.PHONY: bundle bundle-validate bundle-build bundle-push image-push catalog catalog-push

bundle:
	VERSION=$(VERSION) IMG=$(IMG) python3 scripts/build-bundle.py

bundle-validate: bundle
	$(OPERATOR_SDK) bundle validate dist/olm/bundle

bundle-build: bundle-validate
	docker build -t $(BUNDLE_IMG) dist/olm/bundle

image-push:
	docker buildx build --platform $(PLATFORMS) -t $(IMG) --push .

bundle-push: bundle-validate
	docker buildx build --platform $(PLATFORMS) -t $(BUNDLE_IMG) --push dist/olm/bundle

catalog:
	mkdir -p dist/olm/catalog
	$(OPM) init cube-microk8s-operator --default-channel=alpha --output=yaml > dist/olm/catalog/index.yaml
	$(OPM) render $(BUNDLE_IMG) --output=yaml >> dist/olm/catalog/index.yaml
	printf '\n---\nschema: olm.channel\npackage: cube-microk8s-operator\nname: alpha\nentries:\n  - name: cube-microk8s-operator.v$(VERSION)\n' >> dist/olm/catalog/index.yaml
	$(OPM) validate dist/olm/catalog
	rm -f dist/olm/catalog.Dockerfile
	$(OPM) generate dockerfile dist/olm/catalog --builder-image=quay.io/operator-framework/opm:v1.74.0 --base-image=quay.io/operator-framework/opm:v1.74.0

catalog-push: catalog
	docker buildx build --platform $(PLATFORMS) -f dist/olm/catalog.Dockerfile -t $(CATALOG_IMG) --push dist/olm

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
