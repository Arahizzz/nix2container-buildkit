# nix2container-buildkit

Custom BuildKit frontend that allows to build [nix2container](https://github.com/nlewo/nix2container) images
using native Docker tooling.

## Features
* Multi-layer builds
* Caching of nix store for faster incremental rebuilds
* Separate caching of each layer
* Support of [LLB merge operation](https://www.docker.com/blog/mergediff-building-dags-more-efficiently-and-elegantly/) to prevent needless layer rebuilds. You must enable [containerd image store](https://docs.docker.com/desktop/features/containerd/) otherwise it will fallback onto simple layer copying.

## Usage
Simply add to the top of your flake.nix:
```nix
# syntax = docker.io/arahizzz/nix2container-buildkit:0.1.0
```
See [example of flake.nix](examples/flake/flake.nix).

You can now build image using the usual Docker commands:
```bash
docker build -f flake.nix .
```
__Note:__ `flake.nix` must be present at the root of the build context.

You can also specify custom flake target, if needed:
```bash
docker build -f flake.nix --target package.x86_64-linux.customTarget .
```