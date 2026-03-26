IMAGE ?= litellm/docker-desktop-extension
TAG ?= latest
BUN_CONFIG_REGISTRY ?= https://registry.npmjs.org

BUILDER = buildx_builder

build:
	docker build --build-arg BUN_CONFIG_REGISTRY=$(BUN_CONFIG_REGISTRY) --tag=$(IMAGE):$(TAG) .

install: build
	docker extension install $(IMAGE):$(TAG)

update: build
	docker extension update $(IMAGE):$(TAG)

remove:
	docker extension rm $(IMAGE):$(TAG)

validate: build
	docker extension validate $(IMAGE):$(TAG)

debug:
	docker extension dev debug $(IMAGE):$(TAG)

.PHONY: build install update remove validate debug
