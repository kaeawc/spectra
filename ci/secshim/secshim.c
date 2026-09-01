/*
 * CI-only Security.framework symbol stubs for Darling runs
 * (scripts/darling-ci.sh). Never part of macOS builds.
 *
 * Go 1.24+ targets macOS 12 and dyld-binds Security.framework symbols that
 * Darling's macOS 11.7-era framework lacks, so every binary importing
 * crypto/x509 aborts at load with "Symbol not found". This dylib is
 * cross-linked with zig, injected via DYLD_INSERT_LIBRARIES, and resolved
 * via DYLD_FORCE_FLAT_NAMESPACE (the imports are two-level-bound to
 * Security.framework, which an inserted library cannot otherwise satisfy).
 *
 * The stub must have ZERO undefined symbols: the inserted dylib loads into
 * every process, including pure-Go test binaries that link only libSystem.
 * Referencing framework data symbols (e.g. kCFTypeArrayCallBacks) aborts
 * those at load, and implementing the function via Darling's real trust
 * APIs SIGABRTs the processes that do link Security — both were tried.
 * Returning NULL makes Go's system-root loading see an empty chain and
 * fall back gracefully, which is the empirically best behavior.
 */

/* CFArrayRef SecTrustCopyCertificateChain(SecTrustRef); macOS 12.0+ */
void *SecTrustCopyCertificateChain(void *trust) {
	(void)trust;
	return 0;
}
