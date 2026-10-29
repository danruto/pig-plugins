{
  description = "Personal PiG extensions";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/55ba7f49ef2962b42cbd126522b7df5f95037679";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "aarch64-darwin" ];
      eachSystem = nixpkgs.lib.genAttrs systems;
    in {
      devShells = eachSystem (system:
        let pkgs = import nixpkgs { inherit system; };
        in {
          default = pkgs.mkShell {
            packages = [ pkgs.go pkgs.nodejs ];
          };
        });

      packages = eachSystem (system:
        let
          pkgs = import nixpkgs { inherit system; };
          sdkSource = pkgs.fetchzip {
            url = "https://github.com/MichaelKinsy/PiG/archive/refs/tags/v0.5.3.tar.gz";
            hash = "sha256-XK27VNd799qUCsOUidYPo/leleMWtoan5gNuke9geqk=";
          };
          status = pkgs.buildNpmPackage {
            pname = "pig-status";
            version = "0.3.0";
            src = pkgs.lib.cleanSourceWith {
              src = ./.;
              filter = path: type: !(builtins.elem (baseNameOf path) [ ".git" "node_modules" ".direnv" "result" ]);
            };
            npmDepsHash = "sha256-9aYRKI29hwkFkXr2J99FDZ6ytfwQWUC+GFnYdV5PEHY=";
            npmFlags = [ "--legacy-peer-deps" "--ignore-scripts" ];
            nativeBuildInputs = [ pkgs.go ];
            env = {
              CGO_ENABLED = "0";
              GOTOOLCHAIN = "local";
              GOPROXY = "off";
              GOSUMDB = "off";
            };
            buildPhase = ''
              runHook preBuild
              export GOCACHE="$TMPDIR/go-cache"
              export GOMAXPROCS="$NIX_BUILD_CORES"
              export GOFLAGS="-p=$NIX_BUILD_CORES"
              cd extensions/pig-status
              go mod edit -replace github.com/MichaelKinsy/PiG/extensions/sdk=${sdkSource}/extensions/sdk
              go build -trimpath -ldflags="-s -w" -o "$TMPDIR/pig-status" .
              cd ../..
              runHook postBuild
            '';
            doCheck = true;
            checkPhase = ''
              runHook preCheck
              npm run typecheck
              node --test test/test-usage.mjs test/test-docs.mjs
              (cd extensions/pig-status && go test ./... && go vet ./...)
              runHook postCheck
            '';
            installPhase = ''
              runHook preInstall
              npm prune --omit=dev --ignore-scripts --legacy-peer-deps
              mkdir -p "$out/extensions" "$out/status" "$out/bin"
              install -m755 "$TMPDIR/pig-status" "$out/bin/pig-status"
              cp extensions/pig-status-usage.ts "$out/extensions/"
              cp status/usage.ts "$out/status/"
              cp -r node_modules "$out/"
              cp LICENSE THIRD_PARTY_NOTICES.md "$out/"
              node --input-type=module -e '
                import fs from "node:fs";
                const pkg = JSON.parse(fs.readFileSync("package.json"));
                pkg.pi.extensions[0] = "bin/pig-status";
                fs.writeFileSync(process.argv[1], JSON.stringify(pkg));
              ' "$out/package.json"
              runHook postInstall
            '';
            meta = {
              description = "Compact PiG footer with integrated provider usage";
              license = pkgs.lib.licenses.mit;
              platforms = systems;
            };
          };
        in { pig-status = status; default = status; });
    };
}
