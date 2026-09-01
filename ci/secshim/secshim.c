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
 * The stubs only need to exist so dyld can bind them; unit tests never
 * evaluate real certificate trust. Returning NULL makes any accidental
 * caller see an empty result rather than undefined behavior.
 */

/* CFArrayRef SecTrustCopyCertificateChain(SecTrustRef); macOS 12.0+ */
void *SecTrustCopyCertificateChain(void *trust) {
	(void)trust;
	return 0;
}
