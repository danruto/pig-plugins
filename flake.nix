{
  description = "Personal PiG extensions";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/55ba7f49ef2962b42cbd126522b7df5f95037679";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "aarch64-darwin" ];
      eachSystem = nixpkgs.lib.genAttrs systems;
      golangciLint = pkgs: pkgs.golangci-lint.overrideAttrs (finalAttrs: _: {
        version = "2.15.0";
        src = pkgs.fetchFromGitHub {
          owner = "golangci";
          repo = "golangci-lint";
          tag = "v${finalAttrs.version}";
          hash = "sha256-m9PoJHescRuT9DiKsb6LL+lQlc7RKAWDiRyf8pNMffA=";
        };
        vendorHash = "sha256-gWbhZc/BJwuiwyTMaR1ShEBI1lo0bGLCXsdj5n91baw=";
        ldflags = [
          "-s"
          "-w"
          "-X main.version=${finalAttrs.version}"
          "-X main.commit=v${finalAttrs.version}"
          "-X main.date=1970-01-01T00:00:00Z"
        ];
      });
    in {
      devShells = eachSystem (system:
        let pkgs = import nixpkgs { inherit system; };
        in {
          default = pkgs.mkShell {
            packages = [ pkgs.go pkgs.nodejs pkgs.python3 (golangciLint pkgs) ];
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
            version = "0.3.1";
            src = pkgs.lib.cleanSourceWith {
              src = ./.;
              filter = path: type: !(builtins.elem (baseNameOf path) [ ".git" ".pb" "node_modules" ".direnv" "result" ])
                && path != (toString ./. + "/compaction/generated");
            };
            npmDepsHash = "sha256-z9bsQCnX2vpBw7NYXEiuDvKuWQoKgZvgBLuaQLdR8Ww=";
            npmFlags = [ "--legacy-peer-deps" "--ignore-scripts" ];
            nativeBuildInputs = [ pkgs.go pkgs.python3 pkgs.patch ];
            env = {
              CGO_ENABLED = "0";
              GOTOOLCHAIN = "local";
              GOPROXY = "off";
              GOSUMDB = "off";
            };
            buildPhase = ''
              runHook preBuild
              npm run prepare:compaction
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
              PIG_STATUS_SDK=${sdkSource}/extensions/sdk bash scripts/check.sh sandbox
              runHook postCheck
            '';
            installPhase = ''
              runHook preInstall
              npm prune --omit=dev --ignore-scripts --legacy-peer-deps
              mkdir -p "$out/extensions" "$out/bin" "$out/compaction"
              install -m755 "$TMPDIR/pig-status" "$out/bin/pig-status"
              cp extensions/pig-openai-server-compaction.ts "$out/extensions/"
              cp compaction/payload.ts "$out/compaction/"
              cp -r compaction/generated "$out/compaction/"
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
