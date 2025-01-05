.PHONY: all build

VERSION := dev
TAG := arahizzz/nix2container-buildkit:$(VERSION)
	
all: build

build:
	docker buildx build . --output type=oci,dest=build/frontend,tar=false
	
deploy:
	docker buildx build . -t $(TAG) --build-arg VERSION=$(VERSION) --build-arg BUILDER_TAG=$(TAG) --push --platform linux/amd64 --progress=plain
