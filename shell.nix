{
  mkShellNoCC,
  callPackage,

  # extra tooling
  go,
  gopls,
  goreleaser,
  gcc,
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
    gcc
  ];
}
