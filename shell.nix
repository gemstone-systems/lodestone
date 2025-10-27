{
  mkShellNoCC,
  callPackage,

  # extra tooling
  go,
  gopls,
  goreleaser,
}:
let
  defaultPackage = callPackage ./default.nix { };
in
mkShellNoCC {
  inputsFrom = [ defaultPackage ];

  packages = [
    go
    gopls
    goreleaser
  ];
}
