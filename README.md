# buildkit-frontend-template

A basic BuildKit frontend template.

## Getting started

Get the repo:

```bash
$ git clone https://github.com:arahizzz/nix2container-buildkit.git
$ cd buildkit-frontend-template
```

Rename the template to your frontend name of choice, by replacing all instances
of `arahizzz/nix2container-buildkit` with your frontend name:

```bash
$ find . \( ! -regex '.*/\..*' \) -type f -exec sed -i 's/jedevc\/buildkit-frontend-template/your-username\/your-frontend-name/g' {} +
```

## Build the frontend

To build and push the frontend:

```bash
$ make deploy DEST=docker.io/your-username/your-frontend-name
```

## Running the examples

To setup and run the examples locally, you need a version of BuildKit with
https://github.com/moby/buildkit/pull/3633 - you may need to build this
yourself.

```bash
$ BUILDX_BUILDER=dev make example EXAMPLE=basic
$ docker image inspect example-basic
[
    {
        "Id": "sha256:79dea169968780bf07791667b1131b774339c15f6b29c70364ec715741076f3f",
        ...
    }
]
```