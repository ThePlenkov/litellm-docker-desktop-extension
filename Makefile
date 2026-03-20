IMAGE ?= litellm/docker-desktop-extension
CONFIG_SERVER_IMAGE ?= litellm/config-server
TAG ?= latest

BUILDER = buildx_builder

build:
	docker build --tag=$(CONFIG_SERVER_IMAGE):$(TAG) backend/
	docker build --tag=$(IMAGE):$(TAG) .

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
