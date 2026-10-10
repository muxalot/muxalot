# NixOS package for the desktop client. The released .deb cannot be consumed
# on NixOS (no apt, non-FHS lib paths); this builds the same binary against
# nixpkgs instead. `nix run .#muxalot-desktop`, or add it to flakes input.
{
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAll = f: nixpkgs.lib.genAttrs systems (s: f (import nixpkgs { system = s; }));
      # Makefile's version scheme needs a tag; a flake source is its tree, so
      # fall back to the commit hash with a sortable "0" prefix.
      version = if self ? rev then "0.0.0-${self.shortRev}" else "0.0.0";
    in
    {
      packages = forAll (pkgs: {
        default = self.packages.${pkgs.system}.muxalot-desktop;
        muxalot-desktop = pkgs.buildGoModule {
          pname = "muxalot-desktop";
          inherit version;

          src = nixpkgs.lib.cleanSource ./.;
          modRoot = "desktop";
          vendorHash = "sha256-vp2jDVUlF7IsjnseLT175p3plFj57rruypA+cK1/f4A=";

          # Same tags as `make desktop` (DESKTOP_TAGS=gtk3, production turns DevTools off).
          tags = [ "production" "gtk3" ];
          ldflags = [ "-s" "-w" "-X main.version=${version}" ];

          # frontend/vendor/ is gitignored, so it is absent from the flake's
          # source tree; copy the web assets from the Android assets it tracks.
          # Run from desktop/ (modRoot), hence the ../app path.
          preBuild = ''
            mkdir -p frontend/vendor
            for f in xterm.js xterm.css addon-fit.js addon-web-links.js addon-unicode11.js \
              JetBrainsMonoNerdFontMono-Regular.woff2 \
              LICENSE-xterm.txt LICENSE-nerdfonts.txt; do
              cp ../app/app/src/main/assets/$f frontend/vendor/
            done
          '';

          nativeBuildInputs = with pkgs; [ pkg-config wrapGAppsHook3 ];
          buildInputs = with pkgs; [ gtk3 webkitgtk_4_1 ];

          postInstall = ''
            install -Dm0644 ../desktop/packaging/muxalot-desktop.desktop $out/share/applications/muxalot-desktop.desktop
            install -Dm0644 ../assets/logo.svg $out/share/icons/hicolor/scalable/apps/muxalot.svg
          '';

          meta = with pkgs.lib; {
            description = "Desktop client for muxalot remote tmux sessions";
            homepage = "https://github.com/muxalot/muxalot";
            license = licenses.mit;
            mainProgram = "muxalot-desktop";
            platforms = [ "x86_64-linux" "aarch64-linux" ];
          };
        };
      });

      # Same toolchain `make desktop-test` wants (README "Build from source").
      devShells = forAll (pkgs: {
        default = pkgs.mkShell {
          packages = with pkgs; [ go pkg-config gtk3 webkitgtk_4_1 gnumake tmux zip ];
        };
      });
    };
}