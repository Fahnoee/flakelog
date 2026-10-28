{
  description = "flakelog: a readable changelog for flake.lock updates";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      forAll = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
      version = "0.1.0";
    in
    {
      packages = forAll (pkgs: rec {
        flakelog = pkgs.buildGoModule {
          pname = "flakelog";
          inherit version;
          src = self;
          vendorHash = null;
          subPackages = [ "cmd/flakelog" ];
          ldflags = [
            "-s"
            "-w"
            "-X main.version=${version}"
          ];
          meta = {
            description = "A readable changelog for flake.lock updates";
            homepage = "https://github.com/Fahnoee/flakelog";
            license = pkgs.lib.licenses.mit;
            mainProgram = "flakelog";
          };
        };
        default = flakelog;
      });

      apps = forAll (pkgs: {
        default = {
          type = "app";
          program = "${self.packages.${pkgs.stdenv.hostPlatform.system}.default}/bin/flakelog";
        };
      });

      devShells = forAll (pkgs: {
        default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
            gotools
          ];
        };
      });

      checks = forAll (pkgs: {
        flakelog = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      });

      formatter = forAll (pkgs: pkgs.nixfmt);
    };
}
