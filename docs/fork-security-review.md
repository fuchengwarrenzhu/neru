# Fork security review — 2026-10-24

Initial baseline, not a full security audit. No app or permission changes were made.

## Scope

- Fork: `fuchengwarrenzhu/neru`; upstream: `y3owk1n/neru`.
- Reviewed source baseline: `bec261e62392c31032c19222438d61f0a7a0f917`.
- Installed app: `/Applications/Neru.app`, v1.59.0, commit `1907128407f3`, built with Go 1.26.6.

## Checks and findings

- OSV version checks against all 25 third-party module requirements in both the baseline and v1.59.0 found `GO-2026-6222` / `CVE-2026-46603` in `golang.org/x/image v0.43.0`, fixed in v0.45.0. It concerns excessive allocation decoding crafted VP8L images. The module is an indirect requirement; binary scanning did not report it in the installed macOS app. Do not treat module presence as proof of a reachable desktop vulnerability.
- `go mod verify`: passed for the local module cache.
- Website `npm audit --package-lock-only --ignore-scripts --json`: zero reported vulnerabilities. These are website dependencies, not the desktop runtime.
- Source `govulncheck@v1.6.0 ./...` using the local Go 1.27.1 toolchain returned four standard-library findings (`GO-2026-6617`, `GO-2026-6613`, `GO-2026-6611`, `GO-2026-6603`), fixed in Go 1.27.2. It also reported package/module findings without calls to the affected symbols. These source results concern the local toolchain, not the installed release's compiler.
- Binary `govulncheck@v1.6.0 -mode=binary` against the installed app returned 13 standard-library findings for Go 1.26.6. Reported fixes require Go 1.26.9 or 1.26.10, depending on the advisory. Binary symbol matches do not establish an attacker-controlled path; HTTP/TLS findings need contextual reachability review. Source baseline already requests toolchain Go 1.26.10; that does not update the installed app.
- `codesign -dv --verbose=2` reports an ad-hoc signature and no TeamIdentifier. `spctl --assess --type execute --verbose=2` rejects the app. This is a distribution trust limitation, not evidence of malware. No Gatekeeper bypass was performed.
- Source inspection confirms a per-user local socket, `0600` socket / `0700` directory permissions, and kernel-derived same-user peer checks. These exclude other ordinary user accounts; they do not isolate processes running as the same user.

## Security implications

Accessibility access lets this app inspect UI metadata and control input. Its native Objective-C/CGo layer and config-driven command execution deserve review alongside dependencies. The upstream security policy states that there is no built-in telemetry or outbound network communication; this review did not monitor live network traffic or audit every source file. Configured external commands can perform network access independently.

The semantic target selector (`internal/adapter/jev`, added after this baseline) makes stdlib `net/http` and TLS reachable in the shipped app for the first time, only when a binding opts in to semantic hint search, so the HTTP/TLS findings above apply to it and its builds require the patched toolchain (Go 1.26.10 or later, or 1.27.2 or later).

Before treating a build as cleared, use a patched Go toolchain, reassess the listed findings for the intended platform, review the native bridge and relevant incoming diff, and verify release provenance. Keep third-party module updates distinct from build-only/other-platform dependencies. Native memory safety, malicious dependencies, CI supply-chain compromise, and new unpublished vulnerabilities are outside what these scans can rule out.

References: [upstream security policy](https://github.com/y3owk1n/neru/security), [VP8L advisory](https://pkg.go.dev/vuln/GO-2026-6222), [Go vulnerability database](https://vuln.go.dev/).
