# syntax = docker.io/arahizzz/nix2container-buildkit:dev

{
  inputs.nix2container.url = "github:nlewo/nix2container";

  outputs = { self, nixpkgs, nix2container }: let
    pkgs = import nixpkgs { system = "x86_64-linux"; };
    nix2containerPkgs = nix2container.packages.x86_64-linux;
  in {
    packages.x86_64-linux.default = nix2containerPkgs.nix2container.buildImage {
      name = "bash";
      maxLayers = 5;
      copyToRoot = [
        # When we want tools in /, we need to symlink them in order to
        # still have libraries in /nix/store. This behavior differs from
        # dockerTools.buildImage but this allows to avoid having files
        # in both / and /nix/store.
        pkgs.cowsay
        (pkgs.buildEnv {
          name = "root";
          paths = [ pkgs.bashInteractive pkgs.coreutils ];
          pathsToLink = [ "/bin" ];
        })
      ];
      config = {
        Cmd = [ "/bin/bash" ];
      };
    };
  };
}